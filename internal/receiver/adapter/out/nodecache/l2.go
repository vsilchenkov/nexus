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

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/receiver/usecase/port"
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

var _ port.NodeReader = (*L2Reader)(nil)

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

// GetByPath — fresh-hit (L2) → downstream → write-back; при ошибке downstream
// пробует stale-hit в пределах StaleTTL.
func (r *L2Reader) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	if n, ok := r.cache.Get(path); ok {
		r.mx.IncL2Hit("fresh")
		return n, nil
	}
	r.mx.IncL2Miss()

	n, err := r.inner.GetByPath(ctx, path)
	if err == nil {
		r.cache.Set(path, n)
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
		if v, fresh, age, found := r.cache.GetStale(path); found && !fresh && age <= r.staleTTL {
			r.mx.IncL2Hit("stale")
			r.logger.Warn("nodecache L2 stale-hit (downstream error)",
				r.logger.Str("path", path),
				r.logger.Int("age_ms", int(age.Milliseconds())),
				r.logger.Err(err))
			return v, nil
		}
	}
	return nil, err
}
