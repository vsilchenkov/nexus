package rejectlog

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// Причины потери записи (метка cause у nexus_ingress_reject_dropped_total).
const (
	// dropQueueFull — всплеск отказов обогнал сброс: очередь коллектора полна.
	dropQueueFull = "queue_full"
	// dropGroupsFull — в одном интервале сброса встретилось больше разных
	// групп, чем разрешено держать в памяти (сканер по случайным путям).
	dropGroupsFull = "groups_full"
	// dropWriteFailed — сброс в PostgreSQL не удался.
	dropWriteFailed = "write_failed"
)

// flushTimeout — потолок одной записи пачки. Больше интервала сброса, чтобы
// медленная база не рвала транзакцию на полпути, но конечен: зависший сброс
// не должен удерживать накопленное в памяти бесконечно.
const flushTimeout = 30 * time.Second

// Sample — одно наблюдение отказа, каким его видит middleware (§94.4).
//
// Тела запроса здесь нет и быть не может: только размер. Значения query в
// RawPath уже замаскированы вызывающим, Headers — отобранный белый список.
type Sample struct {
	Key       domain.RejectedGroupKey
	Status    int32
	At        time.Time
	ClientIP  string
	UserAgent string
	RawPath   string
	BodyBytes int64
	RequestID string
	Headers   map[string]string
}

// Config — параметры коллектора (секция receiver.reject_log).
type Config struct {
	// FlushInterval — как часто накопленное уходит в PostgreSQL.
	FlushInterval time.Duration
	// QueueSize — глубина очереди между обработчиком запроса и агрегатором.
	QueueSize int
	// MaxGroups — сколько разных групп держится в памяти между сбросами.
	MaxGroups int
}

// Writer — запись накопленного (реализуется Repo). Интерфейс объявлен здесь,
// на стороне потребителя: коллектор не знает, что хранилище — PostgreSQL.
type Writer interface {
	Flush(ctx context.Context, aggs []domain.RejectedAggregate) error
}

// HostResolver — неблокирующий lookup PTR-имени клиента (§67). Реализация —
// platform/rdns, общая с Sender: кеш PTR-имён у них один.
type HostResolver interface {
	Lookup(ip string) string
}

// DropSink — счётчик потерянных записей (реализуется platform/metrics).
type DropSink interface {
	IncIngressRejectDropped(cause string, n int)
}

// Collector агрегирует отказы в памяти и сбрасывает их в хранилище пачками
// (§94.4).
//
// Контракт Add — НОЛЬ ожидания на пути запроса: запись кладётся в канал, а
// если он полон, отбрасывается со счётчиком. Журнал наблюдаемости не имеет
// права ни задерживать боевой трафик, ни расти в памяти без предела.
type Collector struct {
	ch      chan Sample
	cfg     Config
	writer  Writer
	hosts   HostResolver
	drops   DropSink
	clock   clock.Clock
	logger  logging.Logger
	enabled atomic.Bool
}

// Option — функциональная опция конструктора.
type Option func(*Collector)

// WithHostResolver подключает резолвер PTR-имён (§67). nil игнорируется —
// клиенты останутся без имён, но журнал будет работать.
func WithHostResolver(h HostResolver) Option {
	return func(c *Collector) {
		if h != nil {
			c.hosts = h
		}
	}
}

// WithDropSink подключает счётчик потерь. Без него потери остаются только в
// логе — метрика единственная показывает, что журнал неполон.
func WithDropSink(d DropSink) Option {
	return func(c *Collector) {
		if d != nil {
			c.drops = d
		}
	}
}

// WithClock подменяет источник времени (§4 CLAUDE.md): от него зависят
// first_seen/last_seen группы.
func WithClock(cl clock.Clock) Option {
	return func(c *Collector) {
		if cl != nil {
			c.clock = cl
		}
	}
}

// Дефолты конфигурации — те же значения, что подставляет platform/config.
// Продублированы здесь, чтобы коллектор, созданный в тесте с нулевым Config,
// вёл себя осмысленно, а не отбрасывал всё подряд.
const (
	defaultFlushInterval = 10 * time.Second
	defaultQueueSize     = 4096
	defaultMaxGroups     = 2000
)

// New создаёт коллектор. Сбор изначально ВЫКЛЮЧЕН: включает его настройка
// срока хранения (§94.5) — до её чтения писать некуда и незачем.
func New(w Writer, cfg Config, logger logging.Logger, opts ...Option) *Collector {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.MaxGroups <= 0 {
		cfg.MaxGroups = defaultMaxGroups
	}
	c := &Collector{
		ch:     make(chan Sample, cfg.QueueSize),
		cfg:    cfg,
		writer: w,
		clock:  clock.System(),
		logger: logger,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SetEnabled включает и выключает сбор без рестарта (§94.5: срок хранения 0
// означает «журнал выключен»).
func (c *Collector) SetEnabled(v bool) { c.enabled.Store(v) }

// Enabled сообщает, ведётся ли сбор.
func (c *Collector) Enabled() bool { return c.enabled.Load() }

// Add регистрирует отказ. Никогда не блокирует и не возвращает ошибку:
// вызывающий — путь ответа клиенту, и падение журнала его не касается.
func (c *Collector) Add(s Sample) {
	if c == nil || c.writer == nil || !c.enabled.Load() {
		return
	}
	s.Key.Normalize()
	if s.At.IsZero() {
		s.At = c.clock.Now()
	}
	select {
	case c.ch <- s:
	default:
		// Очередь полна: всплеск обогнал сброс. Отбрасываем — это дешевле, чем
		// задержать ответ клиенту ради строки в журнале.
		c.dropped(dropQueueFull, 1)
	}
}

// Run ведёт агрегацию и периодический сброс. Блокируется до ctx.Done;
// запускается из app.Start в горутине.
func (c *Collector) Run(ctx context.Context) {
	defer safego.Recover(c.logger, "receiver.rejectlog")
	if c.writer == nil {
		return
	}
	tick := time.NewTicker(c.cfg.FlushInterval)
	defer tick.Stop()

	buf := map[domain.RejectedGroupKey]*aggregate{}
	for {
		select {
		case <-ctx.Done():
			// Сначала дочитываем очередь: записи, уже принятые у обработчиков,
			// теряться из-за момента остановки не должны — Add для них уже
			// вернулся, то есть отказ считается зафиксированным.
			c.drain(buf)
			// Накопленное дописываем уже отвязанным контекстом: родительский
			// отменён, а сброс должен успеть выполниться.
			c.flush(context.WithoutCancel(ctx), buf)
			return
		case s := <-c.ch:
			c.absorb(buf, s)
		case <-tick.C:
			c.flush(ctx, buf)
		}
	}
}

// drain забирает из очереди всё, что там уже лежит, не дожидаясь новых
// записей. Вызывается только при остановке.
func (c *Collector) drain(buf map[domain.RejectedGroupKey]*aggregate) {
	for {
		select {
		case s := <-c.ch:
			c.absorb(buf, s)
		default:
			return
		}
	}
}

// aggregate — состояние одной группы между сбросами.
type aggregate struct {
	key     domain.RejectedGroupKey
	status  int32
	first   time.Time
	last    time.Time
	count   int64
	clients map[string]*domain.RejectedClient
	samples []domain.RejectedSample
}

// absorb вливает наблюдение в буфер.
func (c *Collector) absorb(buf map[domain.RejectedGroupKey]*aggregate, s Sample) {
	a := buf[s.Key]
	if a == nil {
		if len(buf) >= c.cfg.MaxGroups {
			// Сканер по случайным путям иначе выел бы память: каждая его
			// попытка — новая группа. Отбрасываем новые группы, уже набранные
			// продолжают считаться.
			c.dropped(dropGroupsFull, 1)
			return
		}
		a = &aggregate{key: s.Key, first: s.At, clients: map[string]*domain.RejectedClient{}}
		buf[s.Key] = a
	}
	a.status = s.Status
	a.count++
	if s.At.Before(a.first) {
		a.first = s.At
	}
	if s.At.After(a.last) {
		a.last = s.At
	}
	c.absorbClient(a, s)
	c.absorbSample(a, s)
}

func (c *Collector) absorbClient(a *aggregate, s Sample) {
	if s.ClientIP == "" {
		return
	}
	cl := a.clients[s.ClientIP]
	if cl == nil {
		if len(a.clients) >= domain.RejectedMaxClientsPerGroup {
			// Держим тех, кто уже попал в пачку: окончательное вытеснение по
			// last_seen делает писатель, у него есть вся история группы.
			return
		}
		cl = &domain.RejectedClient{IP: s.ClientIP, FirstSeen: s.At}
		a.clients[s.ClientIP] = cl
	}
	cl.Count++
	if s.At.After(cl.LastSeen) {
		cl.LastSeen = s.At
	}
	if s.At.Before(cl.FirstSeen) {
		cl.FirstSeen = s.At
	}
	if s.UserAgent != "" {
		cl.UserAgent = s.UserAgent
	}
	// PTR-имя резолвится здесь, а не в обработчике запроса: Lookup не ходит в
	// сеть (§67 — только L1-кеш, промах планирует фоновый резолв), но и эта
	// работа боевому пути ни к чему.
	if cl.Host == "" && c.hosts != nil {
		cl.Host = c.hosts.Lookup(s.ClientIP)
	}
}

func (c *Collector) absorbSample(a *aggregate, s Sample) {
	a.samples = append(a.samples, domain.RejectedSample{
		At:         s.At,
		ClientIP:   s.ClientIP,
		HTTPMethod: s.Key.HTTPMethod,
		RawPath:    s.RawPath,
		Status:     s.Status,
		BodyBytes:  s.BodyBytes,
		RequestID:  s.RequestID,
		Headers:    s.Headers,
	})
	// В памяти держим не больше, чем переживёт запись: писатель всё равно
	// оставит в БД последние RejectedMaxSamplesPerGroup.
	if len(a.samples) > domain.RejectedMaxSamplesPerGroup {
		a.samples = slices.Delete(a.samples, 0, len(a.samples)-domain.RejectedMaxSamplesPerGroup)
	}
}

// flush записывает накопленное и очищает буфер.
//
// Буфер очищается ВСЕГДА, в том числе после ошибки записи: удержание пачки до
// починки базы означало бы рост памяти под нагрузкой ровно в тот момент, когда
// с инфраструктурой и так плохо. Потери считает метрика.
func (c *Collector) flush(ctx context.Context, buf map[domain.RejectedGroupKey]*aggregate) {
	if len(buf) == 0 {
		return
	}
	aggs := make([]domain.RejectedAggregate, 0, len(buf))
	var records int64
	for k, a := range buf {
		aggs = append(aggs, a.toDomain())
		records += a.count
		delete(buf, k)
	}

	writeCtx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	if err := c.writer.Flush(writeCtx, aggs); err != nil {
		c.dropped(dropWriteFailed, int(records))
		c.logger.Warn("rejectlog: flush failed, records dropped",
			c.logger.Int("groups", len(aggs)),
			c.logger.Int("records", int(records)),
			c.logger.Err(err))
		return
	}
	c.logger.Debug("rejectlog: flushed",
		c.logger.Int("groups", len(aggs)),
		c.logger.Int("records", int(records)))
}

func (a *aggregate) toDomain() domain.RejectedAggregate {
	clients := make([]domain.RejectedClient, 0, len(a.clients))
	for _, cl := range a.clients {
		clients = append(clients, *cl)
	}
	return domain.RejectedAggregate{
		Key:       a.key,
		Status:    a.status,
		FirstSeen: a.first,
		LastSeen:  a.last,
		Count:     a.count,
		Clients:   clients,
		Samples:   a.samples,
	}
}

func (c *Collector) dropped(cause string, n int) {
	if c.drops != nil {
		c.drops.IncIngressRejectDropped(cause, n)
	}
}
