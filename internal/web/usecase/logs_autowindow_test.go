package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// autoWindowReaderMock — мок LogReader для автоокна §77.2: пишет все
// Search-запросы и отвечает через respond (по SinceMs теста видно, какое окно
// пробовалось).
type autoWindowReaderMock struct {
	logReaderMock
	searches   []port.LogQuery
	respond    func(q port.LogQuery) []*domain.LogRecord
	rangeCalls int
	rangeErr   error
}

func (m *autoWindowReaderMock) Search(_ context.Context, q port.LogQuery) ([]*domain.LogRecord, error) {
	m.searches = append(m.searches, q)
	if m.respond == nil {
		return nil, nil
	}
	return m.respond(q), nil
}

func (m *autoWindowReaderMock) DateRange(_ context.Context, _, _ string) (int64, int64, error) {
	m.rangeCalls++
	return m.rangeMin, m.rangeMax, m.rangeErr
}

func autoWindowNodes() *stubNodeRepo {
	return &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "db.t", Status: domain.NodeStatusEnabled},
	}}
}

func rowsN(n int) []*domain.LogRecord {
	out := make([]*domain.LogRecord, n)
	for i := range n {
		out[i] = &domain.LogRecord{ID: "r"}
	}
	return out
}

// Пользовательский from (SinceMs > 0) отключает автоокно: ровно один Search
// с исходной границей, DateRange не дёргается (§77.2).
func TestLogs_AutoWindow_UserFromDisables(t *testing.T) {
	t.Parallel()
	r := &autoWindowReaderMock{respond: func(port.LogQuery) []*domain.LogRecord { return nil }}
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	_, err := uc.Search(context.Background(), "n1", "", port.LogQuery{SinceMs: 12345, Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(r.searches) != 1 {
		t.Fatalf("want 1 search, got %d", len(r.searches))
	}
	if r.searches[0].SinceMs != 12345 {
		t.Fatalf("SinceMs must stay user value, got %d", r.searches[0].SinceMs)
	}
	if r.rangeCalls != 0 {
		t.Fatalf("DateRange must not be called, got %d", r.rangeCalls)
	}
}

// Полнотекстовый фильтр отключает автоокно (§77.2): совпадение может лежать где
// угодно в истории, и перебор окон умножал бы дорогие пробы. Расчёт по боевым
// замерам §77.5: шесть проб 1ч→90д ~76 с плюс финальная ~28 с — вместо одних
// только 28 с. Регресс, найденный ревизией перед сдачей: ТЗ это запрещало, а
// гейта в коде не было.
func TestLogs_AutoWindow_FullTextSearchDisables(t *testing.T) {
	t.Parallel()
	maxMs := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	r := &autoWindowReaderMock{
		// Совпадений нет — именно этот случай и порождал перебор всех окон.
		respond: func(port.LogQuery) []*domain.LogRecord { return nil },
	}
	r.rangeMin, r.rangeMax = maxMs-90*24*time.Hour.Milliseconds(), maxMs
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	_, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Q: "photo", Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(r.searches) != 1 {
		t.Fatalf("полнотекст обязан идти ОДНИМ запросом, got %d: %+v", len(r.searches), r.searches)
	}
	if r.searches[0].SinceMs != 0 {
		t.Fatalf("полнотекст не должен получать подобранную границу, got SinceMs=%d",
			r.searches[0].SinceMs)
	}
	if r.searches[0].QExpr == nil {
		t.Fatal("QExpr обязан быть распарсен и передан адаптеру")
	}
}

// Кеш DateRange не растёт неограниченно: протухшие ключи выметаются (узлы
// удаляются и переезжают на другие таблицы, а Web живёт неделями).
func TestLogs_AutoWindow_DateRangeCacheEvictsStale(t *testing.T) {
	t.Parallel()
	maxMs := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	r := &autoWindowReaderMock{
		respond: func(port.LogQuery) []*domain.LogRecord { return rowsN(50) },
	}
	r.rangeMin, r.rangeMax = maxMs-90*24*time.Hour.Milliseconds(), maxMs

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{
		"n1": {ID: "n1", ClickHouseTable: "db.t1", Status: domain.NodeStatusEnabled},
		"n2": {ID: "n2", ClickHouseTable: "db.t2", Status: domain.NodeStatusEnabled},
	}}
	uc := NewLogsUsecase(r, nodes, logging.NewNoop(),
		WithLogsClock(clock.Func(func() time.Time { return now })))

	if _, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50}); err != nil {
		t.Fatalf("search n1: %v", err)
	}
	// Время ушло за TTL — запись n1 протухла; обращение к n2 обязано её вымести.
	now = now.Add(2 * dateRangeCacheTTL)
	if _, err := uc.Search(context.Background(), "n2", "", port.LogQuery{Limit: 50}); err != nil {
		t.Fatalf("search n2: %v", err)
	}
	uc.rangeMu.Lock()
	got := len(uc.rangeCache)
	uc.rangeMu.Unlock()
	if got != 1 {
		t.Fatalf("протухшая запись должна выметаться: в кеше %d ключей", got)
	}
}

// Первое окно (1ч от якоря) набирает полную страницу → один Search, граница =
// якорь − 1ч. Якорь без курсора — max(date_request) узла (§77.2).
func TestLogs_AutoWindow_FirstWindowFills(t *testing.T) {
	t.Parallel()
	maxMs := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	r := &autoWindowReaderMock{
		respond: func(port.LogQuery) []*domain.LogRecord { return rowsN(50) },
	}
	r.rangeMin, r.rangeMax = maxMs-90*24*time.Hour.Milliseconds(), maxMs
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	recs, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recs) != 50 {
		t.Fatalf("want 50 rows, got %d", len(recs))
	}
	if len(r.searches) != 1 {
		t.Fatalf("want 1 search, got %d", len(r.searches))
	}
	wantSince := maxMs - time.Hour.Milliseconds()
	if r.searches[0].SinceMs != wantSince {
		t.Fatalf("SinceMs: want %d (anchor-1h), got %d", wantSince, r.searches[0].SinceMs)
	}
}

// Узкие окна недобирают → окна расширяются по порядку, ответ приходит из
// первого окна с полной страницей; якорь при скролле — курсор UntilMs.
func TestLogs_AutoWindow_WidensUntilFilled(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	day := 24 * time.Hour.Milliseconds()
	r := &autoWindowReaderMock{}
	r.rangeMin, r.rangeMax = anchor-365*day, anchor+day
	r.respond = func(q port.LogQuery) []*domain.LogRecord {
		if anchor-q.SinceMs >= day { // окно от 24ч и шире — страница полная
			return rowsN(50)
		}
		return rowsN(3)
	}
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	recs, err := uc.Search(context.Background(), "n1", "", port.LogQuery{UntilMs: anchor, BeforeID: "x", Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recs) != 50 {
		t.Fatalf("want 50 rows, got %d", len(recs))
	}
	if len(r.searches) != 3 { // 1ч → 6ч → 24ч
		t.Fatalf("want 3 searches (1h,6h,24h), got %d: %+v", len(r.searches), r.searches)
	}
	wants := []int64{
		anchor - time.Hour.Milliseconds(),
		anchor - 6*time.Hour.Milliseconds(),
		anchor - day,
	}
	for i, w := range wants {
		if r.searches[i].SinceMs != w {
			t.Fatalf("search %d: SinceMs want %d, got %d", i, w, r.searches[i].SinceMs)
		}
	}
	// Курсор обязан доехать до адаптера во всех попытках.
	for i, s := range r.searches {
		if s.UntilMs != anchor || s.BeforeID != "x" {
			t.Fatalf("search %d: keyset cursor lost: %+v", i, s)
		}
	}
}

// Инвариант конца истории (§72.2): окна покрыли весь диапазон узла, страница
// не набрана → финальная попытка ВСЕГДА без нижней границы. Недобор после неё —
// настоящий конец истории, а не артефакт узкого окна.
func TestLogs_AutoWindow_FinalAttemptUnbounded(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	r := &autoWindowReaderMock{
		respond: func(port.LogQuery) []*domain.LogRecord { return rowsN(7) },
	}
	// Вся история узла — 2 часа: окно 6ч уже уходит ниже min.
	r.rangeMin, r.rangeMax = anchor-2*time.Hour.Milliseconds(), anchor
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	recs, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recs) != 7 {
		t.Fatalf("want 7 rows, got %d", len(recs))
	}
	last := r.searches[len(r.searches)-1]
	if last.SinceMs != 0 {
		t.Fatalf("final attempt must be unbounded, got SinceMs=%d", last.SinceMs)
	}
	// 1ч (внутри истории) → 6ч уже ниже min → финальная. Итого 2 запроса.
	if len(r.searches) != 2 {
		t.Fatalf("want 2 searches (1h, unbounded), got %d", len(r.searches))
	}
}

// Пустая таблица (DateRange = 0,0) — сразу прямой Search без перебора окон.
func TestLogs_AutoWindow_EmptyTableDirect(t *testing.T) {
	t.Parallel()
	r := &autoWindowReaderMock{respond: func(port.LogQuery) []*domain.LogRecord { return nil }}
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	recs, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("want empty, got %d", len(recs))
	}
	if len(r.searches) != 1 || r.searches[0].SinceMs != 0 {
		t.Fatalf("want single unbounded search, got %+v", r.searches)
	}
}

// Ошибка DateRange не пробрасывается: автоокно деградирует в прямой Search,
// который сам классифицирует недоступность CH.
func TestLogs_AutoWindow_RangeErrorFallsBack(t *testing.T) {
	t.Parallel()
	r := &autoWindowReaderMock{
		rangeErr: errors.New("ch down"),
		respond:  func(port.LogQuery) []*domain.LogRecord { return rowsN(2) },
	}
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	recs, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 rows, got %d", len(recs))
	}
	if len(r.searches) != 1 || r.searches[0].SinceMs != 0 {
		t.Fatalf("want single unbounded search, got %+v", r.searches)
	}
}

// Кеш DateRange: повторный Search в пределах TTL не дёргает DateRange заново.
func TestLogs_AutoWindow_DateRangeCached(t *testing.T) {
	t.Parallel()
	maxMs := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	r := &autoWindowReaderMock{
		respond: func(port.LogQuery) []*domain.LogRecord { return rowsN(50) },
	}
	r.rangeMin, r.rangeMax = maxMs-90*24*time.Hour.Milliseconds(), maxMs
	uc := NewLogsUsecase(r, autoWindowNodes(), logging.NewNoop())

	for range 3 {
		if _, err := uc.Search(context.Background(), "n1", "", port.LogQuery{Limit: 50}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}
	if r.rangeCalls != 1 {
		t.Fatalf("DateRange must be cached: want 1 call, got %d", r.rangeCalls)
	}
}
