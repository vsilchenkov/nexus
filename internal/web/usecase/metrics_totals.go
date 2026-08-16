package usecase

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"nexus/internal/domain"
)

// NodeRankRow — строка среза для порядка узлов в сквозном режиме (§86.10).
//
// Намеренно ЛЁГКАЯ: ни p95, ни спарклайна. Срез приходит на ВСЕ узлы скоупа,
// поэтому каждое лишнее поле умножается на их число, а порядок и бейдж строятся
// ровно из этих четырёх значений.
type NodeRankRow struct {
	NodeID      string
	In          uint64
	Out         uint64
	Errors      uint64
	LastOutcome domain.NodeOutcome
}

// ScopedTotals — агрегат шапки и срез по узлам скоупа (§86.4, §86.10).
//
// Едут вместе, потому что считаются одним проходом: разделять их означало бы
// второй полный проход по ClickHouse ради тех же чисел.
type ScopedTotals struct {
	Totals OverviewTotals
	Nodes  []NodeRankRow
}

// rankRows — срез для порядка из полных строк обзора.
func rankRows(items []NodeThroughputRow) []NodeRankRow {
	out := make([]NodeRankRow, 0, len(items))
	for _, it := range items {
		out = append(out, NodeRankRow{
			NodeID:      it.NodeID,
			In:          it.In,
			Out:         it.Out,
			Errors:      it.Errors,
			LastOutcome: it.LastOutcome,
		})
	}
	return out
}

// Агрегат шапки рабочего стола для сквозного режима (§86.4).
//
// В режиме одной команды шапка приезжает вместе со строками таблицы (§44.A) и
// стоит ровно столько же — отдельного запроса там нет. В сквозном режиме строки
// грузятся ПОРЦИОННО (только видимые), а шапка обязана быть суммой по ВСЕМ узлам
// скоупа, поэтому её считает отдельный запрос: полный проход по всем узлам всех
// команд пользователя.
//
// Этот проход дорог не числом узлов, а объёмом данных (замер §86.4.1: два
// «тяжёлых» узла дороже двенадцати обычных), и рабочий стол его повторяет по
// автообновлению у каждого открытого оператора. Отсюда две меры: кеш с коротким
// TTL и singleflight, схлопывающий одновременные промахи в один расчёт.

// DefaultTotalsCacheTTL — время жизни агрегата шапки.
//
// Выбрано порядка интервала автообновления рабочего стола (§44.C, дефолт 12 с):
// смысл кеша — убрать множитель «сколько операторов × сколько поллингов», а не
// показывать вчерашние числа. Верхняя граница устаревания шапки равна TTL и
// заметно меньше периода, за который она считается (минимум час).
const DefaultTotalsCacheTTL = 15 * time.Second

// totalsCacheEntry — посчитанный агрегат со срезом и момент расчёта.
type totalsCacheEntry struct {
	value    ScopedTotals
	computed time.Time
}

// totalsCache — TTL-кеш агрегата шапки.
//
// Своя реализация, а не библиотека: ключей здесь единицы (набор команд × окно ×
// режим подсчёта), вытеснение по размеру не нужно, а протухшие записи чистятся
// на чтении. Живёт полем usecase — никакого глобального состояния (§4 CLAUDE.md).
type totalsCache struct {
	mu      sync.Mutex
	entries map[string]totalsCacheEntry
	group   singleflight.Group
}

func newTotalsCache() *totalsCache {
	return &totalsCache{entries: make(map[string]totalsCacheEntry)}
}

// get возвращает непротухшую запись.
func (c *totalsCache) get(key string, now time.Time, ttl time.Duration) (ScopedTotals, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return ScopedTotals{}, false
	}
	if now.Sub(e.computed) >= ttl {
		delete(c.entries, key)
		return ScopedTotals{}, false
	}
	return e.value, true
}

func (c *totalsCache) put(key string, value ScopedTotals, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = totalsCacheEntry{value: value, computed: now}
}

// totalsCacheKey — ключ кеша агрегата.
//
// Границы окна КВАНТУЮТСЯ по TTL: у скользящего окна («последние 24 часа»)
// правая граница — «сейчас», и без квантования ключ был бы уникален на каждый
// запрос, то есть кеш не срабатывал бы никогда. Квантование и есть выражение
// TTL: внутри одного окна TTL все запросы делят один расчёт.
//
// Календарный период (явные from/to) квантуется тем же правилом и попадает в
// свою ячейку — его границы и так стабильны.
func totalsCacheKey(scopeIDs []string, since, until time.Time, approx bool, ttl time.Duration) string {
	var b strings.Builder
	for i, id := range scopeIDs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id)
	}
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(since.Truncate(ttl).UnixMilli(), 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(until.Truncate(ttl).UnixMilli(), 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(approx))
	return b.String()
}

// OverviewTotalsScoped — агрегат шапки по всему скоупу и срез для порядка строк
// (§86.4, §86.10).
//
// Возвращает ту же сумму, что дала бы NodesOverviewScoped без сужения по узлам,
// плюс ЛЁГКИЙ срез на каждый узел скоупа (см. NodeRankRow). Спарклайны в этом
// проходе НЕ считаются: раньше они считались и выбрасывались, а это второй
// CH-запрос на каждый узел.
//
// Срез задаёт клиенту порядок и статус, но не заменяет порционную загрузку строк:
// p95 и спарклайн по-прежнему приезжают только для видимых узлов.
//
// Кеш и singleflight включаются только при ненулевом TTL (см. WithTotalsCacheTTL).
func (u *MetricsUsecase) OverviewTotalsScoped(ctx context.Context, sc NodesScope, since, until time.Time) ScopedTotals {
	// Сужение по узлам для агрегата бессмысленно: шапка обязана считаться по
	// ВСЕМУ скоупу, иначе её значение зависело бы от прокрутки таблицы.
	sc.NodeIDs = nil

	if u.totalsTTL <= 0 || u.totalsCache == nil {
		return u.scopedTotals(ctx, sc, since, until)
	}

	scopeIDs, ok := u.totalsScopeIDs(ctx, sc)
	if !ok {
		return ScopedTotals{}
	}
	key := totalsCacheKey(scopeIDs, since, until, u.approxCounts(ctx), u.totalsTTL)
	now := u.clock.Now()
	if value, hit := u.totalsCache.get(key, now, u.totalsTTL); hit {
		u.logger.Debug("nodes totals: cache hit", u.logger.Str("key", key))
		return value
	}
	// singleflight: одновременные промахи (несколько операторов, автообновление)
	// схлопываются в один проход по ClickHouse вместо N одинаковых.
	v, _, _ := u.totalsCache.group.Do(key, func() (any, error) {
		value := u.scopedTotals(ctx, sc, since, until)
		u.totalsCache.put(key, value, u.clock.Now())
		return value, nil
	})
	value, _ := v.(ScopedTotals)
	return value
}

// scopedTotals — один проход по скоупу без спарклайнов: сумма + срез.
func (u *MetricsUsecase) scopedTotals(ctx context.Context, sc NodesScope, since, until time.Time) ScopedTotals {
	res := u.nodesOverviewScoped(ctx, sc, since, until, false)
	return ScopedTotals{Totals: res.Totals, Nodes: rankRows(res.Items)}
}

// totalsScopeIDs — стабильный набор идентификаторов скоупа для ключа кеша.
//
// ok=false означает «считать нечего» (сквозной режим без членств): расчёт не
// запускается вовсе.
func (u *MetricsUsecase) totalsScopeIDs(ctx context.Context, sc NodesScope) ([]string, bool) {
	if sc.UserID == "" {
		if sc.TeamID == "" {
			return nil, false
		}
		return []string{sc.TeamID}, true
	}
	ids, _, err := teamScope(ctx, u.teams, sc.UserID)
	if err != nil {
		u.logger.Warn("nodes totals: resolve scope failed", u.logger.Err(err))
		return nil, false
	}
	if len(ids) == 0 {
		return nil, false
	}
	// Порядок членств репозиторием не гарантирован, а ключ обязан быть
	// стабильным — иначе кеш промахивался бы на каждой перестановке.
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	return sorted, true
}
