// Package nodecache.
//
// l2.go — second-level in-memory cache поверх обычного *Reader.
//
// §9.2 ТЗ: «Локальный second-level cache в памяти процесса (LRU, 1-5 сек
// TTL, ~1000 узлов) для самых горячих узлов — снимает нагрузку с Redis
// при пиковом RPS и спасает в момент кратковременного Redis-flutter'а».
//
// §9.4 крайний случай — «PG и Redis одновременно лежат»: при ошибке от
// downstream-reader'а L2 пытается вернуть протухшую запись (stale-hit)
// возрастом не больше StaleTTL — что позволяет Receiver продолжать
// обслуживать горячий набор узлов даже в полном Redis+PG-outage.
package nodecache

import (
	"context"
	"errors"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase/port"
)

// L2Config — параметры L2-кеша.
type L2Config struct {
	// Enabled включает L2-слой. False = декоратор работает прозрачно
	// (вызовы идут напрямую в downstream без кеша).
	Enabled bool
	// Size — ёмкость LRU (количество ключей). Рекомендация ТЗ: ~1000.
	Size int
	// TTL — время жизни «свежей» записи. Рекомендация ТЗ: 1-5 сек.
	TTL time.Duration
	// StaleTTL — максимальный возраст записи, которую можно вернуть как
	// stale-fallback при ошибке downstream. 0 = stale-fallback выключен.
	StaleTTL time.Duration
}

// L2Metrics — узкий интерфейс для метрик L2. Реализуется *metrics.Metrics
// (см. platform/metrics). На стороне consumer'а — чтобы пакет nodecache не
// зависел от prometheus напрямую (тестируемость, ISP).
type L2Metrics interface {
	IncL2Hit(kind string) // kind="fresh" | "stale"
	IncL2Miss()
	IncL2Eviction()
	SetL2Size(n int)
}

type nopMetrics struct{}

func (nopMetrics) IncL2Hit(string) {}
func (nopMetrics) IncL2Miss()      {}
func (nopMetrics) IncL2Eviction()  {}
func (nopMetrics) SetL2Size(int)   {}

// L2Reader — декоратор поверх port.NodeReader, добавляющий локальный
// LRU-кеш с TTL. Сам реализует port.NodeReader (Liskov: одинаковый контракт).
type L2Reader struct {
	inner    port.NodeReader
	cache    *LRU[*domain.Node]
	staleTTL time.Duration
	logger   logging.Logger
	mx       L2Metrics
}

var (
	_ port.NodeReader = (*L2Reader)(nil)
	_ Invalidator     = (*L2Reader)(nil)
)

// Invalidate выселяет узел из L1 (LRU) и делегирует инвалидацию внутрь (Redis
// DEL у Reader), чтобы следующий Get перечитал свежий конфиг из PG (§57).
func (r *L2Reader) Invalidate(ctx context.Context, teamSlug, path string) error {
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	r.cache.Delete(teamSlug + "/" + path)
	if inv, ok := r.inner.(Invalidator); ok {
		return inv.Invalidate(ctx, teamSlug, path)
	}
	return nil
}

// NewL2 оборачивает inner-reader L2-кешем. Если cfg.Enabled=false — возвращает
// inner без изменений (caller продолжает работать через тот же интерфейс).
//
// metrics может быть nil — тогда используется nop-реализация.
func NewL2(inner port.NodeReader, cfg L2Config, logger logging.Logger, metrics L2Metrics) port.NodeReader {
	if !cfg.Enabled {
		return inner
	}
	if metrics == nil {
		metrics = nopMetrics{}
	}
	r := &L2Reader{
		inner:    inner,
		staleTTL: cfg.StaleTTL,
		logger:   logger,
		mx:       metrics,
	}
	r.cache = NewLRU[*domain.Node](cfg.Size, cfg.TTL).WithOnEvict(metrics.IncL2Eviction)
	return r
}

// Get — fresh-hit (L2) → downstream → write-back; при ошибке downstream
// пробует stale-hit в пределах StaleTTL. Ключ L2 — "<team_slug>/<path>",
// потому что после Phase 10.1 path не глобально уникален.
func (r *L2Reader) Get(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	key := teamSlug + "/" + path
	if n, ok := r.cache.Get(key); ok {
		r.mx.IncL2Hit("fresh")
		return n, nil
	}
	r.mx.IncL2Miss()

	n, err := r.inner.Get(ctx, teamSlug, path)
	if err == nil {
		r.cache.Set(key, n)
		r.mx.SetL2Size(r.cache.Len())
		return n, nil
	}

	// Downstream вернул ошибку. Для «нет такого узла» stale-fallback не нужен:
	// узел действительно не существует, отдаём ошибку как есть.
	if errors.Is(err, domain.ErrNodeNotFound) {
		return nil, err
	}
	// Для прочих ошибок (PG+Redis одновременно лежат, таймаут и т.п.) —
	// пробуем вернуть протухшую запись в пределах StaleTTL.
	if r.staleTTL > 0 {
		if v, fresh, age, found := r.cache.GetStale(key); found && !fresh && age <= r.staleTTL {
			r.mx.IncL2Hit("stale")
			r.logger.Warn("nodecache L2 stale-hit (downstream error)",
				r.logger.Str("team", teamSlug),
				r.logger.Str("path", path),
				r.logger.Int("age_ms", int(age.Milliseconds())),
				r.logger.Err(err))
			return v, nil
		}
	}
	return nil, err
}
