// Package rdns — асинхронный reverse-DNS резолв PTR-имени клиента (§67).
//
// Заполняет колонку client_host CH-логов: IP клиента → hostname из PTR-записи
// корпоративного DNS. Контракт Lookup — НОЛЬ I/O на вызывающем пути: значение
// берётся только из L1-кеша в памяти; промах немедленно возвращает "" и
// планирует ФОНОВЫЙ резолв (Redis-кеш → PTR-lookup), так что имя получат
// следующие записи этого IP. Путь доставки запроса никогда не ждёт ни DNS,
// ни Redis.
//
// Кеш двухуровневый: Redis (nexus:rdns:<ip>, общий для реплик Sender'а,
// переживает рестарты; TTL через EX) + L1 map в памяти с коротким TTL —
// чтобы реплика подхватывала обновления соседей не позже чем через
// l1TTLCap. Негативный результат (нет PTR / ошибка DNS) кешируется тоже
// (пустая строка) — иначе каждый запрос с IP без PTR порождал бы DNS-запрос.
//
// Fail-open всюду: Redis недоступен → работаем в режиме «только L1» с
// Debug-логом; ошибка DNS → негативный кеш. Кросс-репличный дедуп резолвов
// сознательно не делается (§67): singleflight покрывает конкуренцию внутри
// процесса, а худший межрепличный случай — по одному PTR-запросу на IP с
// реплики раз в TTL.
package rdns

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

const (
	// keyPrefix — префикс Redis-ключей кеша (конвенция nexus:*).
	keyPrefix = "nexus:rdns:"
	// l1TTLCap — потолок TTL записи L1: не дольше этого реплика держит
	// значение, не перечитывая Redis (подхват обновлений соседей).
	l1TTLCap = 5 * time.Minute
	// l1Cap — защитный потолок числа записей L1. Кардинальность клиентских
	// IP — сотни; потолок страхует от мусорных значений ClientIP.
	l1Cap = 10_000
	// redisOpTimeout — таймаут одной операции Redis в фоновом резолве.
	redisOpTimeout = 2 * time.Second
)

// Config — параметры резолвера (из sender.rdns, §67).
type Config struct {
	Timeout     time.Duration // таймаут одного PTR-lookup
	CacheTTL    time.Duration // TTL позитивного кеша (Redis)
	NegativeTTL time.Duration // TTL негативного кеша (Redis)
}

// Resolver — реализация usecase.HostResolver (sender). Создаётся один на
// процесс, потокобезопасен.
type Resolver struct {
	baseCtx context.Context // ctx приложения: shutdown гасит фоновые резолвы
	redis   *goredis.Client // nil допустим → режим «только L1»
	// lookupAddr — шов для тестов; по умолчанию net.DefaultResolver.LookupAddr.
	lookupAddr func(ctx context.Context, ip string) ([]string, error)
	sf         singleflight.Group
	logger     logging.Logger
	cfg        Config

	mu      sync.Mutex
	l1      map[string]l1entry
	pending map[string]struct{} // IP с уже запущенным фоновым резолвом
}

type l1entry struct {
	host      string
	expiresAt time.Time
}

// Option — функциональная опция конструктора Resolver.
type Option func(*Resolver)

// WithLookupFunc заменяет функцию PTR-lookup (по умолчанию —
// net.DefaultResolver.LookupAddr). Инъекция DNS-зависимости для тестов.
func WithLookupFunc(fn func(ctx context.Context, ip string) ([]string, error)) Option {
	return func(r *Resolver) {
		if fn != nil {
			r.lookupAddr = fn
		}
	}
}

// New создаёт резолвер. ctx — контекст приложения (его отмена останавливает
// фоновые резолвы); redis может быть nil (fail-open: только L1).
func New(ctx context.Context, redis *goredis.Client, cfg Config, logger logging.Logger, opts ...Option) *Resolver {
	r := &Resolver{
		baseCtx: ctx,
		redis:   redis,
		lookupAddr: func(ctx context.Context, ip string) ([]string, error) {
			return net.DefaultResolver.LookupAddr(ctx, ip)
		},
		logger:  logger,
		cfg:     cfg,
		l1:      make(map[string]l1entry),
		pending: make(map[string]struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Lookup возвращает PTR-имя IP из L1-кеша или "" немедленно; ни DNS, ни Redis
// на этом пути не вызываются. Промах планирует фоновый резолв. Не-IP значения
// (rabbitmq://… у pull-узлов §27.10, пустые строки) сразу дают "".
func (r *Resolver) Lookup(ip string) string {
	if ip == "" || net.ParseIP(ip) == nil {
		return ""
	}
	r.mu.Lock()
	if e, ok := r.l1[ip]; ok && time.Now().Before(e.expiresAt) {
		r.mu.Unlock()
		return e.host
	}
	// Промах/протухло. Один фоновый резолв на IP: pending не даёт плодить
	// горутины под нагрузкой, пока первый резолв не завершился.
	if _, inFlight := r.pending[ip]; !inFlight {
		r.pending[ip] = struct{}{}
		r.mu.Unlock()
		safego.Go(r.logger, "sender.rdns", func() { r.resolve(ip) })
		return ""
	}
	r.mu.Unlock()
	return ""
}

// resolve — фоновый резолв одного IP: Redis-кеш → PTR-lookup → Redis SET + L1.
func (r *Resolver) resolve(ip string) {
	defer func() {
		r.mu.Lock()
		delete(r.pending, ip)
		r.mu.Unlock()
	}()
	// singleflight — на случай гонки двух вызовов до установки pending.
	// Результат кладут в кеши сами resolveOnce, возврат не нужен.
	_, _, _ = r.sf.Do(ip, func() (any, error) {
		return r.resolveOnce(ip), nil
	})
}

// resolveOnce выполняет полный цикл резолва и заполняет оба кеша.
func (r *Resolver) resolveOnce(ip string) string {
	start := time.Now()
	if r.baseCtx.Err() != nil {
		return "" // приложение останавливается
	}

	// 1) Redis-кеш. Пустая строка — валидный негативный хит (отличаем от
	// промаха по redis.Nil).
	if r.redis != nil {
		rctx, cancel := context.WithTimeout(r.baseCtx, redisOpTimeout)
		val, err := r.redis.Get(rctx, keyPrefix+ip).Result()
		cancel()
		switch {
		case err == nil:
			r.l1Set(ip, val, val != "")
			r.logger.Debug("rdns: resolved from redis cache",
				r.logger.Str("ip", ip), r.logger.Str("host", val),
				r.logger.Str("source", "redis"))
			return val
		case err != goredis.Nil:
			// §51.9: fail-open — Redis недоступен, идём в DNS напрямую.
			r.logger.Debug("rdns: redis get failed, falling back to dns",
				r.logger.Str("ip", ip), r.logger.Err(err))
		}
	}

	// 2) PTR-lookup с таймаутом.
	dctx, cancel := context.WithTimeout(r.baseCtx, r.cfg.Timeout)
	names, err := r.lookupAddr(dctx, ip)
	cancel()

	host := ""
	if err == nil && len(names) > 0 {
		host = strings.TrimSuffix(names[0], ".")
	}
	positive := host != ""
	r.l1Set(ip, host, positive)

	// 3) Redis SET с TTL (позитив/негатив). Ошибка — Debug, не фатально.
	if r.redis != nil {
		ttl := r.cfg.CacheTTL
		if !positive {
			ttl = r.cfg.NegativeTTL
		}
		rctx, rcancel := context.WithTimeout(r.baseCtx, redisOpTimeout)
		if serr := r.redis.Set(rctx, keyPrefix+ip, host, ttl).Err(); serr != nil {
			r.logger.Debug("rdns: redis set failed",
				r.logger.Str("ip", ip), r.logger.Err(serr))
		}
		rcancel()
	}

	// §51.9: исход резолва. Отсутствие PTR — норма (Debug, не Warn).
	source := "dns"
	if !positive {
		source = "dns_negative"
	}
	if err != nil {
		r.logger.Debug("rdns: ptr lookup failed (cached negative)",
			r.logger.Str("ip", ip), r.logger.Err(err),
			r.logger.Int("duration_ms", int(time.Since(start).Milliseconds())))
	} else {
		r.logger.Debug("rdns: resolved",
			r.logger.Str("ip", ip), r.logger.Str("host", host),
			r.logger.Str("source", source),
			r.logger.Int("duration_ms", int(time.Since(start).Milliseconds())))
	}
	return host
}

// l1Set кладёт значение в L1 с TTL = min(cfg TTL, l1TTLCap). При переполнении
// сначала выбрасываются протухшие записи, затем (крайний случай) кеш
// сбрасывается целиком — кардинальность реальных клиентских IP мала, ветка
// защитная.
func (r *Resolver) l1Set(ip, host string, positive bool) {
	ttl := r.cfg.CacheTTL
	if !positive {
		ttl = r.cfg.NegativeTTL
	}
	ttl = min(ttl, l1TTLCap)

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.l1) >= l1Cap {
		now := time.Now()
		for k, e := range r.l1 {
			if now.After(e.expiresAt) {
				delete(r.l1, k)
			}
		}
		if len(r.l1) >= l1Cap {
			clear(r.l1)
		}
	}
	r.l1[ip] = l1entry{host: host, expiresAt: time.Now().Add(ttl)}
}
