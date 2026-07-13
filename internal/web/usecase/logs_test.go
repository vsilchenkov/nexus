package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// logReaderMock — отдаёт фиксированные записи на каждый Subscribe-tick.
type logReaderMock struct {
	calls       int
	rows        []*domain.LogRecord
	err         error
	getRow      *domain.LogRecord
	getErr      error
	failedCount uint64
	countErr    error
	bodyChunk   string
	bodyTotal   int64
	lastQuery   port.LogQuery // последний Search-запрос (для проверки QExpr, §48)
	methods     []string      // ответ DistinctMethods (§48)
	rangeMin    int64         // ответ DateRange (§48)
	rangeMax    int64
}

func (m *logReaderMock) GetByID(_ context.Context, _, _ string) (*domain.LogRecord, error) {
	if m.getRow == nil && m.getErr == nil {
		return nil, domain.ErrNotFound
	}
	return m.getRow, m.getErr
}

func (m *logReaderMock) ListSince(_ context.Context, _, _ string, _ int64, _ int) ([]*domain.LogRecord, error) {
	m.calls++
	return m.rows, m.err
}

func (m *logReaderMock) Search(_ context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	m.lastQuery = q
	return m.rows, m.err
}

func (m *logReaderMock) CountErrors(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}

func (m *logReaderMock) CountFailed(_ context.Context, _, _ string, _, _ int64) (uint64, error) {
	return m.failedCount, m.countErr
}
func (m *logReaderMock) FailedIDs(_ context.Context, _, _ string, _, _ int64, _ int) ([]string, bool, error) {
	return nil, false, nil
}

func (m *logReaderMock) GetByIDPreview(_ context.Context, _, _ string, _ int) (*domain.LogRecord, int64, int64, error) {
	if m.getRow == nil {
		if m.getErr != nil {
			return nil, 0, 0, m.getErr
		}
		return nil, 0, 0, domain.ErrNotFound
	}
	return m.getRow, int64(len([]rune(m.getRow.Request))), int64(len([]rune(m.getRow.Response))), m.getErr
}

func (m *logReaderMock) GetBodyChunk(_ context.Context, _, _, _ string, _, _ int) (string, int64, error) {
	return m.bodyChunk, m.bodyTotal, m.getErr
}

func (m *logReaderMock) DistinctMethods(_ context.Context, _, _ string, _ int) ([]string, error) {
	return m.methods, m.err
}

func (m *logReaderMock) DateRange(_ context.Context, _, _ string) (int64, int64, error) {
	return m.rangeMin, m.rangeMax, m.err
}

// §43.1: узел с кривым именем CH-таблицы (legacy с дефисом) на чтении логов
// деградирует как «логи не настроены» (мягко), а не падает с «invalid table name»
// и не флудит Sentry (Sentry issue 157314).
func TestLogs_InvalidTableName_DegradesAsNotConfigured(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"bad":  {ID: "bad", ClickHouseTable: "nexus_vika_dev.yandex-delivery", Status: domain.NodeStatusEnabled},
		"good": {ID: "good", ClickHouseTable: "nexus_default.ok", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, err := uc.Search(context.Background(), "bad", "", port.LogQuery{})
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("кривое имя: want ErrNodeLogsNotConfigured (мягко), got %v", err)
	}
	if _, err := uc.Search(context.Background(), "good", "", port.LogQuery{}); err != nil {
		t.Fatalf("валидное имя: want nil, got %v", err)
	}
}

func TestLogs_ListSinceForwardsToReader(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{rows: []*domain.LogRecord{
		{ID: "1", DateRequest: time.Now()},
		{ID: "2", DateRequest: time.Now()},
	}}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	rows, err := uc.ListSince(context.Background(), "n1", "", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestLogs_CountFailed_ForwardsToReader(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{failedCount: 42}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", TeamID: "team1", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	n, err := uc.CountFailed(context.Background(), "n1", "team1", 0, 0)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if n != 42 {
		t.Fatalf("want 42, got %d", n)
	}
}

func TestLogs_CountFailed_TeamScopeAndNoTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", TeamID: "team1", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{failedCount: 5}, nodes, logging.NewNoop())

	// Чужая команда → 404.
	if _, err := uc.CountFailed(context.Background(), "n1", "other", 0, 0); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
	// Нет таблицы → ErrNodeLogsNotConfigured.
	if _, err := uc.CountFailed(context.Background(), "n1", "team1", 0, 0); !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_GetByID_ReturnsFullRecord(t *testing.T) {
	t.Parallel()
	want := &domain.LogRecord{ID: "log-1", Request: `{"big":"body"}`, Response: `{"ok":true}`}
	r := &logReaderMock{getRow: want}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	got, err := uc.GetByID(context.Background(), "n1", "", "log-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Request != want.Request || got.Response != want.Response {
		t.Fatalf("bodies not returned: %+v", got)
	}
}

func TestLogs_GetByID_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "n1", "", "log-1")
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_GetByID_NodeNotFound(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "nope", "", "log-1")
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_GetByID_RecordNotFound(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{getErr: domain.ErrNotFound}, nodes, logging.NewNoop())
	_, err := uc.GetByID(context.Background(), "n1", "", "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLogs_ListSince_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, err := uc.ListSince(context.Background(), "n1", "", 0, 100)
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_Subscribe_NoCHTable(t *testing.T) {
	t.Parallel()
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(&logReaderMock{}, nodes, logging.NewNoop())

	_, _, err := uc.Subscribe(context.Background(), "n1", "", port.LogQuery{})
	if !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_ListSince_NodeNotFound(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, err := uc.ListSince(context.Background(), "nope", "", 0, 100)
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

func TestLogs_Subscribe_NodeMissing(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())
	_, _, err := uc.Subscribe(context.Background(), "nope", "", port.LogQuery{})
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

// §48: невалидное поисковое выражение → ErrBadQuery ещё ДО резолва узла
// (даже если узла нет / логи не настроены — 400 приоритетнее).
func TestLogs_Search_BadQuery(t *testing.T) {
	t.Parallel()
	uc := NewLogsUsecase(&logReaderMock{}, &stubNodeRepo{nodes: map[string]*domain.Node{}}, logging.NewNoop())

	_, err := uc.Search(context.Background(), "nope", "", port.LogQuery{Q: "(", QRegex: true})
	if !errors.Is(err, logsearch.ErrBadQuery) {
		t.Fatalf("regex: want ErrBadQuery (до ErrNodeNotFound), got %v", err)
	}
	_, err = uc.Search(context.Background(), "nope", "", port.LogQuery{Q: "a & & b"})
	if !errors.Is(err, logsearch.ErrBadQuery) {
		t.Fatalf("мини-язык: want ErrBadQuery, got %v", err)
	}
	_, _, err = uc.Subscribe(context.Background(), "nope", "", port.LogQuery{Q: "(", QRegex: true})
	if !errors.Is(err, logsearch.ErrBadQuery) {
		t.Fatalf("subscribe: want ErrBadQuery, got %v", err)
	}
}

// §48: Search парсит сырое Q по флагам и передаёт адаптеру готовый QExpr.
func TestLogs_Search_FillsQExpr(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	if _, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Q: "a & -url:b", QCase: true}); err != nil {
		t.Fatalf("search: %v", err)
	}
	e := r.lastQuery.QExpr
	if e == nil {
		t.Fatal("QExpr не заполнен")
	}
	if !e.CaseSensitive || e.Regex || e.WholeWord {
		t.Fatalf("флаги не проброшены: %+v", e)
	}
	if len(e.Groups) != 1 || len(e.Groups[0]) != 2 {
		t.Fatalf("want 1 группа из 2 термов, got %+v", e.Groups)
	}
	// Пустое Q → фильтр выключен.
	if _, err := uc.Search(context.Background(), "n1", "", port.LogQuery{}); err != nil {
		t.Fatalf("search empty: %v", err)
	}
	if r.lastQuery.QExpr != nil {
		t.Fatal("пустое Q: QExpr должен быть nil")
	}
}

// §48: зеркало SQL-фильтра для live-tail — method + QExpr (скоупы/негация/
// parameters). Тела в live пустые (ListSince) — здесь матчинг по url/params.
func TestMatchLogFilter_Extended(t *testing.T) {
	t.Parallel()
	rec := &domain.LogRecord{
		URL:        "/v1/orders",
		Method:     "v1/orders",
		Parameters: "id=42&debug=1",
		Status:     200,
		Done:       true,
	}
	mustExpr := func(q string, opts logsearch.Options) *logsearch.Expr {
		t.Helper()
		e, err := logsearch.Parse(q, opts)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		return e
	}
	tests := []struct {
		name string
		q    port.LogQuery
		want bool
	}{
		{name: "без фильтров", q: port.LogQuery{}, want: true},
		{name: "method совпал", q: port.LogQuery{Method: "v1/orders"}, want: true},
		{name: "method не совпал", q: port.LogQuery{Method: "v1/parcels"}, want: false},
		{name: "q по url", q: port.LogQuery{QExpr: mustExpr("orders", logsearch.Options{})}, want: true},
		{name: "q по parameters", q: port.LogQuery{QExpr: mustExpr("debug=1", logsearch.Options{})}, want: true},
		{name: "q AND: один терм мимо", q: port.LogQuery{QExpr: mustExpr("orders & nope", logsearch.Options{})}, want: false},
		{name: "q OR", q: port.LogQuery{QExpr: mustExpr("nope | debug", logsearch.Options{})}, want: true},
		{name: "негация присутствующего", q: port.LogQuery{QExpr: mustExpr("-orders", logsearch.Options{})}, want: false},
		{name: "скоуп params: найден", q: port.LogQuery{QExpr: mustExpr("params:debug", logsearch.Options{})}, want: true},
		{name: "скоуп url: не найден (текст в params)", q: port.LogQuery{QExpr: mustExpr("url:debug", logsearch.Options{})}, want: false},
		{name: "method + q вместе", q: port.LogQuery{Method: "v1/orders", QExpr: mustExpr("debug", logsearch.Options{})}, want: true},
	}
	for _, tt := range tests {
		if got := matchLogFilter(rec, tt.q); got != tt.want {
			t.Errorf("%s: want %v, got %v", tt.name, tt.want, got)
		}
	}
}

// §48.3: фасеты Methods/DateRange — форвардинг к reader'у, team scope и
// деградация «логи не настроены» как у остальных читающих методов.
func TestLogs_Facets_ForwardAndScope(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{methods: []string{"a", "b"}, rangeMin: 100, rangeMax: 200}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1":    {ID: "n1", ClickHouseTable: "t.t", TeamID: "team1", Status: domain.NodeStatusEnabled},
		"notab": {ID: "notab", ClickHouseTable: "", TeamID: "team1", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())

	ms, err := uc.Methods(context.Background(), "n1", "team1")
	if err != nil || len(ms) != 2 {
		t.Fatalf("methods: want 2, got %v / %v", ms, err)
	}
	lo, hi, err := uc.DateRange(context.Background(), "n1", "team1")
	if err != nil || lo != 100 || hi != 200 {
		t.Fatalf("date range: want 100/200, got %d/%d / %v", lo, hi, err)
	}
	// Чужая команда → 404.
	if _, err := uc.Methods(context.Background(), "n1", "other"); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("methods team scope: want ErrNodeNotFound, got %v", err)
	}
	if _, _, err := uc.DateRange(context.Background(), "n1", "other"); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("range team scope: want ErrNodeNotFound, got %v", err)
	}
	// Нет таблицы → ErrNodeLogsNotConfigured.
	if _, err := uc.Methods(context.Background(), "notab", "team1"); !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("methods no table: want ErrNodeLogsNotConfigured, got %v", err)
	}
	if _, _, err := uc.DateRange(context.Background(), "notab", "team1"); !errors.Is(err, domain.ErrNodeLogsNotConfigured) {
		t.Fatalf("range no table: want ErrNodeLogsNotConfigured, got %v", err)
	}
}

func TestLogs_Subscribe_ClosesOnCtxCancel(t *testing.T) {
	t.Parallel()
	r := &logReaderMock{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "t.t", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop())
	uc.pollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	ch, _, err := uc.Subscribe(ctx, "n1", "", port.LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	// Канал должен закрыться в разумное время после cancel.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}
