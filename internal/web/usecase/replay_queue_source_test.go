package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// §96: источник тела для повтора — оригинальный конверт в очереди Kafka.
// Журнальная копия усечена по max_body_size, и до раздела повтор отправлял
// именно её: приёмник получал оборванный JSON, отвечал 500, запись повторяли
// снова (боевой инцидент 31.08.2026, support/it_upr-vika).

// stubOriginals — управляемый port.AsyncOriginalReader.
type stubOriginals struct {
	byID   map[string]port.AsyncOriginal
	err    error
	calls  int
	gotIDs [][]string
	probes int
}

func (s *stubOriginals) FindOriginals(_ context.Context, in port.OriginalLookup) (map[string]port.AsyncOriginal, error) {
	s.calls++
	s.gotIDs = append(s.gotIDs, in.IDs)
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]port.AsyncOriginal{}
	for _, id := range in.IDs {
		if env, ok := s.byID[id]; ok {
			out[id] = env
		}
	}
	return out, nil
}

func (s *stubOriginals) ProbeOriginals(_ context.Context, in port.OriginalLookup) (map[string]bool, error) {
	s.probes++
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]bool{}
	for _, id := range in.IDs {
		if _, ok := s.byID[id]; ok {
			out[id] = true
		}
	}
	return out, nil
}

// multiLogReader отдаёт РАЗНЫЕ записи по id — массовому повтору нужен набор.
type multiLogReader struct {
	*stubLogReader
	logs map[string]*domain.LogRecord
}

func (m *multiLogReader) GetByID(_ context.Context, _, id string) (*domain.LogRecord, error) {
	if l, ok := m.logs[id]; ok {
		return l, nil
	}
	return nil, errors.New("log not found")
}

const truncMarker = domain.LogBodyTruncationMarker

func queueReplayNode() *domain.Node {
	return &domain.Node{
		ID: "n1", Path: "support/it_upr-vika", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "nexus_support.it_upr_vika",
		RootMethod:      domain.RootMethodRequestAsync,
		IncomingMethod:  domain.HTTPMethodPOST,
		ForwardHeaders:  []string{"Content-Type"},
	}
}

// truncatedLog — запись, чья журнальная копия обрезана: сохранено 20 байт при
// истинном размере 2 МБ, в хвосте маркер усечения.
func truncatedLog(id string) *domain.LogRecord {
	return &domain.LogRecord{
		ID: id, Type: domain.RootMethodRequestAsync, HTTPMethod: "POST",
		Request: `{"Данные":[{"file` + truncMarker, RequestSize: 2_045_784,
		DateRequest: time.Now().Add(-time.Hour), Done: false,
	}
}

func queueUC(node *domain.Node, logs port.LogReader, disp *stubDispatcher, orig *stubOriginals, opts ...ReplayOption) *ReplayUsecase {
	all := make([]ReplayOption, 0, 1+len(opts))
	all = append(all, WithQueueOriginals(orig, []port.QueueSource{
		{Topic: "nexus.async.dlq", Group: "nexus-dlq-reprocess", Deep: true},
	}))
	all = append(all, opts...)
	return NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		nil, nil, time.Hour, logging.NewNoop(), all...,
	)
}

// TestReplay_BodyFromQueue — тело берётся из конверта, а не из усечённой копии.
func TestReplay_BodyFromQueue(t *testing.T) {
	t.Parallel()
	full := []byte(`{"Данные":[{"file":"<полное тело на два мегабайта>"}]}`)
	log := truncatedLog("log1")
	disp := &stubDispatcher{}
	orig := &stubOriginals{byID: map[string]port.AsyncOriginal{
		"log1": {ID: "log1", Body: full, Topic: "nexus.async.dlq"},
	}}
	uc := queueUC(queueReplayNode(), &stubLogReader{log: log}, disp, orig)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	require.NoError(t, err)
	assert.Equal(t, full, disp.gotReq.Body, "в приёмник обязано уйти полное тело конверта")
	assert.NotContains(t, string(disp.gotReq.Body), truncMarker,
		"маркер усечения в теле означает, что отправлена журнальная копия")
}

// TestReplay_TruncatedCopyWithoutEnvelope_Rejected — конверта нет, копия
// обрезана: повтор обязан ОТКАЗАТЬ, а не отправить обрезок. Это регрессия
// боевого инцидента: до §96 запрос уходил и получал 500 на каждом повторе.
func TestReplay_TruncatedCopyWithoutEnvelope_Rejected(t *testing.T) {
	t.Parallel()
	disp := &stubDispatcher{}
	uc := queueUC(queueReplayNode(), &stubLogReader{log: truncatedLog("log1")}, disp, &stubOriginals{})

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	require.ErrorIs(t, err, ErrReplayOriginalUnavailable)
	assert.Empty(t, disp.gotReq.NodePath, "диспетчер не должен вызываться вовсе")
}

// TestReplay_SyncTruncated_Rejected — у sync-записи конверта не бывает в
// принципе (§96.10 п.1), и причина отказа обязана быть своей: ждать нечего,
// помочь может только ручное тело.
func TestReplay_SyncTruncated_Rejected(t *testing.T) {
	t.Parallel()
	node := queueReplayNode()
	node.RootMethod = domain.RootMethodRequest
	log := truncatedLog("log1")
	log.Type = domain.RootMethodRequest
	orig := &stubOriginals{}
	uc := queueUC(node, &stubLogReader{log: log}, &stubDispatcher{}, orig)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	require.ErrorIs(t, err, ErrReplayBodyTruncated)
	assert.Zero(t, orig.calls, "sync-запись искать в очереди незачем")
}

// TestReplay_IntactCopyWithoutEnvelope_UsesLog — конверта нет (истёк retention),
// но копия целая: привычный сценарий обязан продолжать работать.
func TestReplay_IntactCopyWithoutEnvelope_UsesLog(t *testing.T) {
	t.Parallel()
	log := &domain.LogRecord{
		ID: "log1", Type: domain.RootMethodRequestAsync, HTTPMethod: "POST",
		Request: `{"ok":true}`, RequestSize: int64(len(`{"ok":true}`)),
		DateRequest: time.Now(), Done: false,
	}
	disp := &stubDispatcher{}
	uc := queueUC(queueReplayNode(), &stubLogReader{log: log}, disp, &stubOriginals{})

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"ok":true}`), disp.gotReq.Body)
}

// TestReplay_BodyOverrideBeatsQueue — тело, заданное оператором руками, важнее
// любого автоматического источника.
func TestReplay_BodyOverrideBeatsQueue(t *testing.T) {
	t.Parallel()
	disp := &stubDispatcher{}
	orig := &stubOriginals{byID: map[string]port.AsyncOriginal{
		"log1": {ID: "log1", Body: []byte(`{"from":"queue"}`)},
	}}
	uc := queueUC(queueReplayNode(), &stubLogReader{log: truncatedLog("log1")}, disp, orig)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "",
		ReplayOptions{UseNodeAuth: true, BodyOverride: []byte(`{"from":"operator"}`)})
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"from":"operator"}`), disp.gotReq.Body)
}

// TestReplay_HeadersFromEnvelope — §96.5: журнал заголовков не хранит, конверт
// хранит. Берём Content-Type и forward_headers узла; авторизацию из конверта не
// тянем — Receiver соберёт её заново по актуальному конфигу.
func TestReplay_HeadersFromEnvelope(t *testing.T) {
	t.Parallel()
	node := queueReplayNode()
	node.ForwardHeaders = []string{"Content-Type", "X-Request-Id"}
	disp := &stubDispatcher{}
	orig := &stubOriginals{byID: map[string]port.AsyncOriginal{
		"log1": {ID: "log1", Body: []byte(`{}`), Headers: map[string]string{
			"Content-Type":  "application/json",
			"X-Request-Id":  "abc-123",
			"Authorization": "Basic c2VjcmV0",
			"X-Internal":    "не проброшен узлом",
		}},
	}}
	uc := queueUC(node, &stubLogReader{log: truncatedLog("log1")}, disp, orig)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: false})
	require.NoError(t, err)
	assert.Equal(t, "application/json", disp.gotReq.Headers["Content-Type"])
	assert.Equal(t, "abc-123", disp.gotReq.Headers["X-Request-Id"])
	assert.NotContains(t, disp.gotReq.Headers, "X-Internal", "узел этот заголовок не пробрасывает")
	assert.NotContains(t, disp.gotReq.Headers, "Authorization",
		"креды из конверта могли протухнуть — их подставляет Receiver по конфигу узла")
}

// TestReplayFailed_SkipsUnavailableWithoutDestroyingOriginal — главная регрессия
// §96.1: запись, чей конверт недоступен, а копия обрезана, пропускается, и её
// оригинал НЕ отменяется в DLQ и НЕ вычищается из журнала. До раздела операция
// отправляла обрезок и тут же уничтожала последний след полного тела.
func TestReplayFailed_SkipsUnavailableWithoutDestroyingOriginal(t *testing.T) {
	t.Parallel()
	node := queueReplayNode()
	logs := &multiLogReader{
		stubLogReader: &stubLogReader{failedIDs: []string{"good", "lost"}},
		logs: map[string]*domain.LogRecord{
			"good": {
				ID: "good", Type: domain.RootMethodRequestAsync, HTTPMethod: "POST",
				Request: `{"ok":1}`, RequestSize: 8, DateRequest: time.Now(),
			},
			"lost": truncatedLog("lost"),
		},
	}
	cancelW := &stubCancelWriter{}
	cleaner := &stubFailedCleaner{}
	orig := &stubOriginals{byID: map[string]port.AsyncOriginal{
		"good": {ID: "good", Body: []byte(`{"ok":1}`)},
	}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		cancelW, nil, time.Hour, logging.NewNoop(),
		WithFailedCleaner(cleaner),
		WithQueueOriginals(orig, []port.QueueSource{{Topic: "nexus.async.dlq", Group: "g", Deep: true}}),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Replayed)
	assert.Equal(t, 1, res.SkippedOriginalUnavailable)
	assert.Zero(t, res.Failed, "пропуск по недоступному оригиналу — не ошибка отправки")
	assert.Equal(t, []string{"good"}, cancelW.gotIDs, "оригинал пропущенной записи обязан остаться в DLQ")
	assert.Equal(t, []string{"good"}, cleaner.gotIDs, "строки пропущенной записи обязаны остаться в журнале")
}

// TestReplayFailed_LooksUpOriginalsInChunks — конверты ищутся порциями: скан на
// каждую запись означал бы N обходов топиков очереди (§96.7).
func TestReplayFailed_LooksUpOriginalsInChunks(t *testing.T) {
	t.Parallel()
	ids := make([]string, 0, originalsChunk+5)
	logsByID := map[string]*domain.LogRecord{}
	envs := map[string]port.AsyncOriginal{}
	for i := range originalsChunk + 5 {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		ids = append(ids, id)
		logsByID[id] = &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequestAsync, HTTPMethod: "POST",
			Request: `{"ok":1}`, RequestSize: 8, DateRequest: time.Now(),
		}
		envs[id] = port.AsyncOriginal{ID: id, Body: []byte(`{"ok":1}`)}
	}
	logs := &multiLogReader{stubLogReader: &stubLogReader{failedIDs: ids}, logs: logsByID}
	orig := &stubOriginals{byID: envs}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": queueReplayNode()}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
		WithQueueOriginals(orig, []port.QueueSource{{Topic: "nexus.async.dlq", Group: "g", Deep: true}}),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, len(ids), res.Replayed)
	assert.Equal(t, 2, orig.calls, "25 записей при порции 20 — ровно два поиска")
	assert.Len(t, orig.gotIDs[0], originalsChunk)
}
