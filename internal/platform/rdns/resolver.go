// Package rdns — асинхронный reverse-DNS резолв PTR-имени клиента (§67).
//
// Заполняет колонку client_host CH-логов: IP клиента → hostname из PTR-записи
// корпоративного DNS. Контракт Lookup — НОЛЬ I/O на вызывающем пути: значение
// берётся только из L1-кеша в памяти. Промах НОВОГО IP немедленно возвращает ""
// и планирует ФОНОВЫЙ резолв (Redis-кеш → PTR-lookup), так что имя получат
// следующие записи этого IP. Протухшая запись L1 отдаёт прежнее имя и так же
// обновляется в фоне (stale-while-revalidate, §105.5). Путь доставки запроса
// никогда не ждёт ни DNS, ни Redis.
//
// Кеш двухуровневый: Redis (nexus:rdns:<ip>, общий для реплик Sender'а,
// переживает рестарты; TTL через EX) + L1 map в памяти с коротким TTL —
// чтобы реплика подхватывала обновления соседей не позже чем через
// l1TTLCap. Негативный результат (нет PTR / ошибка DNS) кешируется тоже
// (пустая строка) — иначе каждый запрос с IP без PTR порождал бы DNS-запрос.
//
// Второй этап (§106.2, opt-in, WithNetBIOS): если PTR ничего не дал, для
// IPv4 спрашивается NetBIOS Node Status — Windows-хосты домена без обратной
// DNS-записи отвечают на него своим именем. Для фоновых задач есть
// блокирующий ResolveNow (§106.3): он ждёт полную цепочку, а не только L1.
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
	// lookupNetBIOS — второй этап (§106.2); nil — этап выключен.
	lookupNetBIOS NetBIOSLookupFunc
	// netbiosSuffix — DNS-суффикс к короткому NetBIOS-имени, без точек по краям.
	netbiosSuffix string
	sf            singleflight.Group
	logger        logging.Logger
	cfg           Config

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

// NetBIOSLookupFunc — второй этап резолва (§106.2): короткое NetBIOS-имя
// IPv4-хоста или "", если уникального имени <00> у хоста нет. Ошибка —
// хост не ответил. Реализация — netbios.Client.Hostname.
type NetBIOSLookupFunc func(ctx context.Context, ip string) (string, error)

// WithNetBIOS включает второй этап: после пустого PTR для IPv4 вызывается fn,
// к найденному имени через точку дописывается suffix ("" — короткое имя как
// есть). nil fn оставляет этап выключенным.
func WithNetBIOS(fn NetBIOSLookupFunc, suffix string) Option {
	return func(r *Resolver) {
		r.lookupNetBIOS = fn
		r.netbiosSuffix = strings.Trim(suffix, ".")
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

// Lookup возвращает PTR-имя IP из L1-кеша немедленно; ни DNS, ни Redis на этом
// пути не вызываются. Промах нового IP даёт "" и планирует фоновый резолв.
// Протухшая запись отдаёт прежнее имя и тоже планирует фоновое обновление
// (§105.5): иначе первая запись после каждого истечения L1 теряла имя, и её не
// находил ни один фильтр. Не-IP значения (rabbitmq://… у pull-узлов §27.10,
// пустые строки) сразу дают "".
//
// Loopback/unspecified тоже не резолвятся: «имя клиента» для 127.0.0.1 — это
// сам хост Нексуса, а не отправитель; на дев-машинах системный резолвер к тому
// же отвечает из hosts-файла мусором (Docker Desktop пишет туда
// «127.0.0.1 kubernetes.docker.internal» — поймано на стенде).
func (r *Resolver) Lookup(ip string) string {
	if skipIP(ip) {
		return ""
	}
	r.mu.Lock()
	e, cached := r.l1[ip]
	if cached && time.Now().Before(e.expiresAt) {
		r.mu.Unlock()
		return e.host
	}
	// Промах/протухло. Один фоновый резолв на IP: pending не даёт плодить
	// горутины под нагрузкой, пока первый резолв не завершился.
	_, inFlight := r.pending[ip]
	if !inFlight {
		r.pending[ip] = struct{}{}
	}
	r.mu.Unlock()
	if !inFlight {
		if cached {
			// §51.9: раз в обновление, а не на каждый запрос — pending выше.
			r.logger.Debug("rdns: serving stale host, refresh scheduled",
				r.logger.Str("ip", ip), r.logger.Str("host", e.host))
		}
		safego.Go(r.logger, "sender.rdns", func() { r.resolve(ip) })
	}
	return e.host // "" у промаха: нулевое значение l1entry
}

// skipIP — значения, которые не резолвятся вовсе: не-IP (rabbitmq://… у
// pull-узлов §27.10, пустые строки), loopback и unspecified.
func skipIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified()
}

// resolve — фоновый резолв одного IP по заявке Lookup.
func (r *Resolver) resolve(ip string) {
	defer func() {
		r.mu.Lock()
		delete(r.pending, ip)
		r.mu.Unlock()
	}()
	// singleflight — на случай гонки двух вызовов до установки pending и
	// одновременного ResolveNow того же IP. Результат кладёт в кеши сам
	// resolveOnce, возврат не нужен.
	_, _, _ = r.sf.Do(ip, func() (any, error) {
		return r.resolveOnce(r.baseCtx, ip, true), nil
	})
}

// l1Set кладёт значение в L1 с TTL = min(cfg TTL, l1TTLCap). При переполнении
// сначала выбрасываются протухшие записи (после §105.5 они ещё отдают имя «по
// старой памяти», но потерять их дешевле, чем свежие), затем (крайний случай)
// кеш сбрасывается целиком — кардинальность реальных клиентских IP мала, ветка
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
