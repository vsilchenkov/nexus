package usecase

import (
	"context"
	"errors"
	"strconv"
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

// newQueueUCCap — то же, но с явным размером батча. §98.4-тестам он нужен
// маленьким: проверяется поведение ЦИКЛА, а не пропускная способность, и гонять
// двести проходов по пять тысяч ID незачем.
func newQueueUCCap(failed port.FailedLogsPurger, cancel port.QueueCancelWriter, node *domain.Node, peekCap int) (*AsyncQueueUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{}}
	if node != nil {
		nodes.nodes[node.ID] = node
	}
	uc := NewAsyncQueueUsecase(&stubPeeker{}, cancel, failed, nodes,
		NewAuditUsecase(repo, logging.NewNoop()),
		"nexus-sender", "nexus.async",
		"nexus-sender-paused", "nexus.async.paused",
		time.Hour, peekCap, logging.NewNoop())
	return uc, repo
}

// stubFailedPurger — управляемый FailedLogsPurger; фиксирует аргументы.
type stubFailedPurger struct {
	ids           []string
	capped        bool
	deleted       uint64
	idsErr        error
	delErr        error
	gotTable      string
	gotNodeID     string
	gotSince      int64
	gotUntil      int64
	gotUnresolved bool   // §79.1: спрошены записи без успешного прогона
	gotDone       string // §79.1: фильтр СТРОК не должен подмешиваться
	gotAligned    bool   // §72.4: сужение по партициям доехало
	idsCalls      int    // §79.2: набор ID обязан считаться ОДИН раз на операцию
	delCalled     bool
	delNodeID     string
	delIDs        []string // §79.2: удаляем ровно то, что отменили
	gotExcludes   []string // §81.5: исключения по маркеру причины (очистка их не применяет)
}

func (s *stubFailedPurger) FailedIDs(_ context.Context, q port.LogQuery, _ int) ([]string, bool, error) {
	s.idsCalls++
	s.gotTable, s.gotNodeID, s.gotSince, s.gotUntil = q.Table, q.NodeID, q.SinceMs, q.UntilMs
	s.gotUnresolved, s.gotDone, s.gotAligned = q.Unresolved, q.Done, q.DateCreateAligned
	s.gotExcludes = q.ExcludeReasonPrefixes
	return s.ids, s.capped, s.idsErr
}
func (s *stubFailedPurger) DeleteFailedRows(_ context.Context, q port.LogQuery, ids []string) (uint64, error) {
	s.delCalled = true
	s.delNodeID = q.NodeID
	s.delIDs = ids
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

// §79.2: набор ID считается ОДИН раз и обслуживает оба шага — tombstone'ы и
// удаление. Раньше это были два независимых прохода (FailedIDs + сплошной
// DELETE по done=0), и аудит показывал расхождение: боевое cancelled=5 при
// deleted=10. Плюс §79.1: спрашиваются записи без успешного прогона.
func TestAsyncQueue_PurgeFailed_SingleIDSetForCancelAndDelete(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	failed := &stubFailedPurger{ids: []string{"f1", "f2"}, deleted: 2}
	uc, _ := newQueueUCFull(&stubPeeker{}, cancelW, failed, asyncNodeCH())

	_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)

	assert.Equal(t, 1, failed.idsCalls, "набор ID обязан считаться один раз на операцию")
	assert.Equal(t, []string{"f1", "f2"}, cancelW.gotIDs)
	assert.Equal(t, []string{"f1", "f2"}, failed.delIDs, "удаляем ровно то, что отменили")
}

// §79.2: пустой набор — ни tombstone'ов, ни DELETE, ни записи в аудит.
// Раньше сплошной DELETE выполнялся всегда, даже когда чистить было нечего.
func TestAsyncQueue_PurgeFailed_EmptySet_NoDelete(t *testing.T) {
	t.Parallel()
	cancelW := &stubCancelWriter{}
	failed := &stubFailedPurger{}
	uc, audit := newQueueUCFull(&stubPeeker{}, cancelW, failed, asyncNodeCH())

	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Zero(t, r.Cancelled)
	assert.False(t, failed.delCalled, "нечего удалять — DELETE не отправляется")
	assert.Empty(t, cancelW.gotIDs)
	assert.Empty(t, audit.entries)
}

// §79.1: очистка спрашивает записи без успешного прогона (Unresolved), а не
// строки done=0, — иначе она отменяла бы доставку уже доставленных сообщений.
func TestAsyncQueue_PurgeFailed_AsksUnresolved(t *testing.T) {
	t.Parallel()
	failed := &stubFailedPurger{ids: []string{"f1"}, deleted: 1}
	uc, _ := newQueueUCFull(&stubPeeker{}, &stubCancelWriter{}, failed, asyncNodeCH())

	_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.True(t, failed.gotUnresolved, "want Unresolved=true")
	assert.Empty(t, failed.gotDone, "Done — фильтр СТРОК журнала, здесь он неуместен")
	assert.True(t, failed.gotAligned, "§72.4: сужение по партициям обязано доехать")
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

// §81.5: очистка «Неудачных доставок» отменённые клиентом записи НЕ щадит —
// убрать их с глаз это ровно то, что от кнопки ждут. Исключение действует
// только в массовом повторе (там повтор дал бы дубли).
func TestAsyncQueue_PurgeFailed_DoesNotExcludeClientCanceled(t *testing.T) {
	t.Parallel()
	failed := &stubFailedPurger{ids: []string{"f1"}, deleted: 1}
	uc, _ := newQueueUCFull(&stubPeeker{}, &stubCancelWriter{}, failed, asyncNodeCH())

	_, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Empty(t, failed.gotExcludes, "очистка видит всё множество недоставленных")
}

// batchFailedPurger — модель настоящего хранилища для §98.4: FailedIDs отдаёт
// не больше cap записей и сообщает, есть ли ещё, а DeleteFailedRows реально их
// убирает. Именно синхронность удаления делает цикл конечным: с асинхронной
// мутацией ClickHouse следующая выборка вернула бы те же ID (см. syncMutationCtx).
type batchFailedPurger struct {
	remaining []string
	idsCalls  int
	delCalls  int
	delSizes  []int
}

func (s *batchFailedPurger) FailedIDs(_ context.Context, _ port.LogQuery, capN int) ([]string, bool, error) {
	s.idsCalls++
	if len(s.remaining) == 0 {
		return nil, false, nil
	}
	n := min(capN, len(s.remaining))
	out := append([]string(nil), s.remaining[:n]...)
	return out, len(s.remaining) > n, nil
}

func (s *batchFailedPurger) DeleteFailedRows(_ context.Context, _ port.LogQuery, ids []string) (uint64, error) {
	s.delCalls++
	s.delSizes = append(s.delSizes, len(ids))
	gone := map[string]struct{}{}
	for _, id := range ids {
		gone[id] = struct{}{}
	}
	kept := s.remaining[:0]
	for _, id := range s.remaining {
		if _, ok := gone[id]; !ok {
			kept = append(kept, id)
		}
	}
	s.remaining = kept
	return uint64(len(ids)), nil
}

func failedIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "f" + strconv.Itoa(i)
	}
	return out
}

// §98.4: очистка убирает ВСЕ неудачные, а не первую страницу. До этого один
// вызов чистил ровно peekCap записей, а признак «остались ещё» интерфейс не
// читал — оператор считал очистку полной.
func TestAsyncQueue_PurgeFailed_ClearsEverythingInBatches(t *testing.T) {
	t.Parallel()
	// Три полных батча и остаток: 10 + 10 + 10 + 1.
	failed := &batchFailedPurger{remaining: failedIDs(31)}
	cancelW := &stubCancelWriter{}
	uc, audit := newQueueUCCap(failed, cancelW, asyncNodeCH(), 10)

	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)

	assert.Equal(t, 31, r.Cancelled, "очищено должно быть всё, а не первая страница")
	assert.False(t, r.Capped, "бюджет не исчерпан — остатка нет")
	assert.Empty(t, failed.remaining)
	assert.Equal(t, []int{10, 10, 10, 1}, failed.delSizes)
	// Инвариант §79.2 держится ВНУТРИ батча: отменяем ровно то, что удаляем.
	assert.Len(t, cancelW.gotIDs, 31)
	// Аудит — одна запись на операцию, с агрегатом и числом проходов.
	require.Len(t, audit.entries, 1)
	assert.Equal(t, uint64(31), audit.entries[0].Details["deleted"])
	assert.Equal(t, 4, audit.entries[0].Details["batches"])
	assert.Equal(t, false, audit.entries[0].Details["capped"])
}

// Бюджет прохода конечен: один клик не должен уметь держать запрос
// неограниченно долго. Упёрлись — Capped, и интерфейс просит нажать ещё раз.
func TestAsyncQueue_PurgeFailed_BudgetExhausted_ReportsCapped(t *testing.T) {
	t.Parallel()
	// На одну запись больше, чем успевает убрать бюджет проходов.
	const batch = 10
	failed := &batchFailedPurger{remaining: failedIDs(batch*purgeFailedMaxBatches + 1)}
	uc, audit := newQueueUCCap(failed, &stubCancelWriter{}, asyncNodeCH(), batch)

	r, err := uc.PurgeFailed(context.Background(), Actor{UserID: "u"}, "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)

	assert.True(t, r.Capped, "остались неудачные — операция обязана сказать об этом")
	assert.Equal(t, batch*purgeFailedMaxBatches, r.Cancelled)
	assert.Equal(t, purgeFailedMaxBatches, failed.delCalls, "проходов ровно по бюджету")
	assert.Len(t, failed.remaining, 1)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, purgeFailedMaxBatches, audit.entries[0].Details["batches"])
	assert.Equal(t, true, audit.entries[0].Details["capped"])
}
