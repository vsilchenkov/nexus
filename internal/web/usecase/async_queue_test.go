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

// stubPeeker — управляемый AsyncQueuePeeker; фиксирует аргументы ScanIDs.
// list/scan отдаются для ЛЮБОГО топика; listByTopic/scanByTopic (если заданы)
// позволяют развести основной топик и delay-топик paused-узлов (§3.6).
type stubPeeker struct {
	list        port.PeekListResult
	listByTopic map[string]port.PeekListResult
	body        port.QueueMessageBody
	scan        port.ScanIDsResult
	scanByTopic map[string]port.ScanIDsResult
	err         error
	scanFrom    time.Time
	scanTo      time.Time
	scanPath    string
	listTopics  []string
	scanTopics  []string
	bodyTopic   string
}

func (s *stubPeeker) PeekList(_ context.Context, _, topic, _ string, _, _ int) (port.PeekListResult, error) {
	s.listTopics = append(s.listTopics, topic)
	if s.listByTopic != nil {
		return s.listByTopic[topic], s.err
	}
	return s.list, s.err
}
func (s *stubPeeker) PeekBody(_ context.Context, topic string, _ int, _ int64) (port.QueueMessageBody, error) {
	s.bodyTopic = topic
	return s.body, s.err
}
func (s *stubPeeker) ScanIDs(_ context.Context, _, topic, nodePath string, from, to time.Time, _ int) (port.ScanIDsResult, error) {
	s.scanPath, s.scanFrom, s.scanTo = nodePath, from, to
	s.scanTopics = append(s.scanTopics, topic)
	if s.scanByTopic != nil {
		return s.scanByTopic[topic], s.err
	}
	return s.scan, s.err
}

// stubCancelWriter — фиксирует переданные на отмену ID.
type stubCancelWriter struct {
	gotIDs []string
	gotTTL time.Duration
	err    error
}

func (s *stubCancelWriter) Cancel(_ context.Context, ids []string, ttl time.Duration) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.gotIDs = append(s.gotIDs, ids...)
	s.gotTTL = ttl
	return len(ids), nil
}

func newQueueUC(peeker port.AsyncQueuePeeker, cancel port.QueueCancelWriter, node *domain.Node) (*AsyncQueueUsecase, *stubAuditRepo) {
	return newQueueUCFull(peeker, cancel, nil, node)
}

func newQueueUCFull(peeker port.AsyncQueuePeeker, cancel port.QueueCancelWriter, failed port.FailedLogsPurger, node *domain.Node) (*AsyncQueueUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{}}
	if node != nil {
		nodes.nodes[node.ID] = node
	}
	uc := NewAsyncQueueUsecase(peeker, cancel, failed, nodes,
		NewAuditUsecase(repo, logging.NewNoop()),
		"nexus-sender", "nexus.async",
		"nexus-sender-paused", "nexus.async.paused",
		time.Hour, 5000, logging.NewNoop())
	return uc, repo
}

// stubFailedPurger — управляемый FailedLogsPurger; фиксирует аргументы.
type stubFailedPurger struct {
	ids       []string
	capped    bool
	deleted   uint64
	idsErr    error
	delErr    error
	gotTable  string
	gotNodeID string
	gotSince  int64
	gotUntil  int64
	delCalled bool
	delNodeID string
}

func (s *stubFailedPurger) FailedIDs(_ context.Context, table, nodeID string, sinceMs, untilMs int64, _ int) ([]string, bool, error) {
	s.gotTable, s.gotNodeID, s.gotSince, s.gotUntil = table, nodeID, sinceMs, untilMs
	return s.ids, s.capped, s.idsErr
}
func (s *stubFailedPurger) DeleteFailed(_ context.Context, _, nodeID string, _, _ int64) (uint64, error) {
	s.delCalled = true
	s.delNodeID = nodeID
	return s.deleted, s.delErr
}

func asyncNode() *domain.Node {
	return &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", Status: domain.NodeStatusEnabled, RootMethod: domain.RootMethodRequestAsync}
}

func TestAsyncQueue_List_Degraded(t *testing.T) {
	t.Parallel()
	// peeker == nil → деградация (kafka_available=false, пустой список).
	uc, _ := newQueueUC(nil, &stubCancelWriter{}, asyncNode())
	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.False(t, r.KafkaAvailable)
	assert.Empty(t, r.Items)
}

func TestAsyncQueue_List_Happy(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{list: port.PeekListResult{Items: []port.QueueMessageMeta{{ID: "id-1"}}, Capped: false}}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())
	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.True(t, r.KafkaAvailable)
	require.Len(t, r.Items, 1)
	assert.Equal(t, "id-1", r.Items[0].ID)
}

// TestAsyncQueue_List_MergesBothTopics (§3.6): очередь узла физически
// расщеплена на основной топик и delay-топик отложенных paused-сообщений —
// список показывает оба, отсортированные по времени поступления.
func TestAsyncQueue_List_MergesBothTopics(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	peeker := &stubPeeker{listByTopic: map[string]port.PeekListResult{
		"nexus.async": {Items: []port.QueueMessageMeta{
			{ID: "fresh", Topic: "nexus.async", ReceivedAt: newer},
		}},
		"nexus.async.paused": {Items: []port.QueueMessageMeta{
			{ID: "backlog", Topic: "nexus.async.paused", ReceivedAt: older},
		}},
	}}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())

	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	require.Len(t, r.Items, 2, "видны сообщения обоих топиков")
	assert.Equal(t, "backlog", r.Items[0].ID, "старое (отложенное) — первым: оно уедет раньше")
	assert.Equal(t, "nexus.async.paused", r.Items[0].Topic, "топик проброшен для запроса тела")
	assert.Equal(t, "fresh", r.Items[1].ID)
	assert.ElementsMatch(t, []string{"nexus.async", "nexus.async.paused"}, peeker.listTopics)
}

// TestAsyncQueue_List_DeduplicatesCirculatingMessage: сообщение в delay-топике
// переносится в хвост на каждом проходе sweeper'а, поэтому один и тот же id
// встречается под разными offset'ами. Без дедупа список и счётчик «Ожидают
// отправки» раздувались бы кратно числу кругов.
func TestAsyncQueue_List_DeduplicatesCirculatingMessage(t *testing.T) {
	t.Parallel()

	peeker := &stubPeeker{listByTopic: map[string]port.PeekListResult{
		"nexus.async": {},
		"nexus.async.paused": {Items: []port.QueueMessageMeta{
			{ID: "dup", Topic: "nexus.async.paused", Offset: 10},
			{ID: "dup", Topic: "nexus.async.paused", Offset: 42}, // копия следующего круга
			{ID: "other", Topic: "nexus.async.paused", Offset: 43},
		}},
	}}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())

	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	require.Len(t, r.Items, 2, "циркулирующая копия не должна удваивать запись")
	assert.Equal(t, int64(10), r.Items[0].Offset, "остаётся первая встреченная координата")
}

// TestAsyncQueue_List_TopicFailureDegrades: недоступность одного топика не
// должна ронять всю вкладку — показываем то, что удалось прочитать.
func TestAsyncQueue_List_TopicFailureDegrades(t *testing.T) {
	t.Parallel()

	peeker := &stubPeeker{list: port.PeekListResult{}, err: errors.New("kafka down")}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())

	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.Empty(t, r.Items)
	assert.True(t, r.KafkaAvailable)
}

// TestAsyncQueue_Purge_ScansBothTopics: бэклог paused-узла лежит в delay-топике,
// поэтому «Очистить все ожидающие» обязана сканировать оба (иначе главный
// юзкейс §34.4 «очистить очередь мёртвого узла» перестаёт работать). Повторы id
// из циркуляции схлопываются.
func TestAsyncQueue_Purge_ScansBothTopics(t *testing.T) {
	t.Parallel()

	peeker := &stubPeeker{scanByTopic: map[string]port.ScanIDsResult{
		"nexus.async":        {IDs: []string{"a"}},
		"nexus.async.paused": {IDs: []string{"b", "a"}}, // "a" уже видели
	}}
	cancelW := &stubCancelWriter{}
	uc, _ := newQueueUC(peeker, cancelW, asyncNode())

	r, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 2, r.Cancelled)
	assert.Equal(t, []string{"a", "b"}, cancelW.gotIDs, "дубли id не отправляются повторно")
	assert.ElementsMatch(t, []string{"nexus.async", "nexus.async.paused"}, peeker.scanTopics)
}

// TestAsyncQueue_Body_TopicAllowList: читать через этот эндпоинт можно только
// топики очереди узла — иначе он превратился бы в универсальный ридер Kafka.
func TestAsyncQueue_Body_TopicAllowList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		topic     string
		wantTopic string
		wantErr   error
	}{
		{"пусто → основной (совместимость)", "", "nexus.async", nil},
		{"основной", "nexus.async", "nexus.async", nil},
		{"delay-топик", "nexus.async.paused", "nexus.async.paused", nil},
		{"чужой топик → отказ", "nexus.logs.retry", "", ErrAsyncQueueUnknownTopic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			peeker := &stubPeeker{body: port.QueueMessageBody{ID: "id-1"}}
			uc, _ := newQueueUC(peeker, &stubCancelWriter{}, asyncNode())

			_, err := uc.Body(context.Background(), "n1", "t1", tc.topic, 0, 1)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, peeker.bodyTopic, "к Kafka не ходим при неизвестном топике")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTopic, peeker.bodyTopic)
		})
	}
}

func TestAsyncQueue_TeamScope_NotFound(t *testing.T) {
	t.Parallel()
	uc, _ := newQueueUC(&stubPeeker{}, &stubCancelWriter{}, asyncNode())
	// Чужая команда → 404.
	_, err := uc.List(context.Background(), "n1", "other-team")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
}

func TestAsyncQueue_PurgePeriod_ForwardsWindowAndIDs(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"a", "b", "c"}, Capped: true}}
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUC(peeker, cancelW, asyncNode())

	from := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r, err := uc.PurgePeriod(context.Background(), Actor{UserID: "u"}, "n1", "t1", from, to)
	require.NoError(t, err)
	assert.Equal(t, 3, r.Cancelled)
	assert.True(t, r.Capped)

	// Окно проброшено в ScanIDs.
	assert.Equal(t, "partner/echo", peeker.scanPath)
	assert.Equal(t, from, peeker.scanFrom)
	assert.Equal(t, to, peeker.scanTo)
	// IDs форвардятся в Cancel с retention-TTL.
	assert.Equal(t, []string{"a", "b", "c"}, cancelW.gotIDs)
	assert.Equal(t, time.Hour, cancelW.gotTTL)
	// Audit-запись purge.
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionAsyncQueuePurge, audit.entries[0].Action)
}

func TestAsyncQueue_PurgeAll_ZeroWindow(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"x"}}}
	cancelW := &stubCancelWriter{}
	uc, _ := newQueueUC(peeker, cancelW, asyncNode())

	_, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.NoError(t, err)
	assert.True(t, peeker.scanFrom.IsZero(), "purge all → нулевая нижняя граница")
	assert.True(t, peeker.scanTo.IsZero(), "purge all → нулевая верхняя граница")
	assert.Equal(t, []string{"x"}, cancelW.gotIDs)
}

func TestAsyncQueue_DeleteOne(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUC(&stubPeeker{}, cancelW, asyncNode())

	err := uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "msg-7")
	require.NoError(t, err)
	assert.Equal(t, []string{"msg-7"}, cancelW.gotIDs)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionAsyncQueuePurge, audit.entries[0].Action)
}

func TestAsyncQueue_Mutations_Unavailable(t *testing.T) {
	t.Parallel()
	// cancel == nil → DeleteOne недоступен.
	uc, _ := newQueueUC(&stubPeeker{}, nil, asyncNode())
	err := uc.DeleteOne(context.Background(), Actor{UserID: "u"}, "n1", "t1", "m")
	assert.ErrorIs(t, err, ErrAsyncQueueUnavailable)

	// peeker == nil → Body недоступен.
	uc2, _ := newQueueUC(nil, &stubCancelWriter{}, asyncNode())
	_, err = uc2.Body(context.Background(), "n1", "t1", "", 0, 0)
	assert.ErrorIs(t, err, ErrAsyncQueueUnavailable)
}

func TestAsyncQueue_PurgeCancelError(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"a"}}}
	cancelW := &stubCancelWriter{err: errors.New("redis down")}
	uc, audit := newQueueUC(peeker, cancelW, asyncNode())

	_, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.Error(t, err)
	assert.Empty(t, audit.entries, "при ошибке cancel audit не пишется")
}

func asyncNodeCH() *domain.Node {
	n := asyncNode()
	n.ClickHouseTable = "nexus_default.partner_echo"
	return n
}

func TestAsyncQueue_PurgeFailed_CancelsAndDeletes(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	failed := &stubFailedPurger{ids: []string{"f1", "f2"}, deleted: 5}
	uc, audit := newQueueUCFull(&stubPeeker{}, cancelW, failed, asyncNodeCH())

	from := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", from, to)
	require.NoError(t, err)

	// 1) ID неудачных отменены (репроцессор перестанет повторять).
	assert.Equal(t, []string{"f1", "f2"}, cancelW.gotIDs)
	// 2) CH-записи удалены; Cancelled = число удалённых (видимый «очищено N»).
	assert.True(t, failed.delCalled)
	assert.Equal(t, 5, r.Cancelled)
	// Окно проброшено в FailedIDs (ms).
	assert.Equal(t, "nexus_default.partner_echo", failed.gotTable)
	assert.Equal(t, from.UnixMilli(), failed.gotSince)
	assert.Equal(t, to.UnixMilli(), failed.gotUntil)
	// Audit.
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionAsyncQueuePurge, audit.entries[0].Action)
}

func TestAsyncQueue_PurgeFailed_AllZeroWindow(t *testing.T) {
	t.Parallel()
	failed := &stubFailedPurger{ids: []string{"x"}, deleted: 1}
	uc, _ := newQueueUCFull(&stubPeeker{}, &stubCancelWriter{}, failed, asyncNodeCH())

	_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Zero(t, failed.gotSince, "purge all → нулевая нижняя граница")
	assert.Zero(t, failed.gotUntil, "purge all → нулевая верхняя граница")
}

func TestAsyncQueue_PurgeFailed_NoCH_Noop(t *testing.T) {
	t.Parallel()
	failed := &stubFailedPurger{ids: []string{"x"}, deleted: 9}
	// Узел без ClickHouseTable → чистить нечего (no-op, без вызова purger/audit).
	uc, audit := newQueueUCFull(&stubPeeker{}, &stubCancelWriter{}, failed, asyncNode())

	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Zero(t, r.Cancelled)
	assert.False(t, failed.delCalled)
	assert.Empty(t, audit.entries)
}

// syncNode — узел без очереди (root_method=request). §69.1: вкладка «Очередь»
// открыта и для таких узлов ради секции «Неудачные доставки», поэтому
// Kafka-операции обязаны сами отсекать их, не трогая брокеры.
func syncNodeCH() *domain.Node {
	n := asyncNodeCH()
	n.RootMethod = domain.RootMethodRequest
	return n
}

func TestAsyncQueue_List_SyncNode_SkipsKafka(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{list: port.PeekListResult{Items: []port.QueueMessageMeta{{ID: "id-1"}}}}
	uc, _ := newQueueUC(peeker, &stubCancelWriter{}, syncNodeCH())

	r, err := uc.List(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.Empty(t, r.Items, "у sync-узла очереди нет")
	assert.Empty(t, peeker.listTopics, "топики не сканируются: peek стоит запроса к брокеру")
}

func TestAsyncQueue_Purge_SyncNode_SkipsKafka(t *testing.T) {
	t.Parallel()
	peeker := &stubPeeker{scan: port.ScanIDsResult{IDs: []string{"a"}}}
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUC(peeker, cancelW, syncNodeCH())

	r, err := uc.PurgeAll(context.Background(), Actor{UserID: "u"}, "n1", "t1")
	require.NoError(t, err)
	assert.Zero(t, r.Cancelled)
	assert.Empty(t, cancelW.gotIDs)
	assert.Empty(t, audit.entries, "нечего чистить — нечего и аудировать")
}

// §69.1: очистка «Неудачных доставок» — единственная операция вкладки, которая
// осмысленна и для sync-узла: она читает/чистит ClickHouse, а не Kafka.
// Tombstone'ы при этом не пишутся: DLQ-репроцессора, который бы их читал, нет.
func TestAsyncQueue_PurgeFailed_SyncNode_DeletesWithoutTombstones(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	failed := &stubFailedPurger{ids: []string{"f1"}, deleted: 3}
	uc, audit := newQueueUCFull(&stubPeeker{}, cancelW, failed, syncNodeCH())

	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 3, r.Cancelled, "удалённые записи done=0 считаются как обычно")
	assert.True(t, failed.delCalled)
	assert.Empty(t, cancelW.gotIDs, "tombstone'ы для sync-узла не пишутся")
	require.Len(t, audit.entries, 1)
}

func TestAsyncQueue_PurgeFailed_DeleteError(t *testing.T) {
	t.Parallel()
	failed := &stubFailedPurger{ids: []string{"x"}, delErr: errors.New("ch down")}
	uc, audit := newQueueUCFull(&stubPeeker{}, &stubCancelWriter{}, failed, asyncNodeCH())

	_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.Error(t, err)
	assert.Empty(t, audit.entries, "при ошибке delete audit не пишется")
}
