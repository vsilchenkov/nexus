// Package grpcsender — клиент к Sender Service по gRPC.
//
// Инфраструктурный клиент, общий для сервисов-потребителей:
//   - Receiver — синхронная доставка (/v1/request/*);
//   - Web — реальный вызов target в dry-run (§55; тот же путь и клиент, что у
//     боевого трафика — иначе тест конфига врал бы про сетевую доступность).
//
// Живёт в platform, а не в adapter конкретного сервиса: копия на второго
// потребителя разъехалась бы с оригиналом, а импорт чужого adapter'а ломает
// границу сервисов (§12/§17).
//
// Простой пул из N подключений с round-robin (§9.3 ТЗ).
//
// # Балансировок здесь ДВЕ, и они про разное
//
// Пул раскладывает вызовы по N ClientConn — это про параллелизм одного
// Receiver'а (§9.3: 8 соединений × max_concurrent_streams). Внутри КАЖДОГО
// соединения работает вторая, gRPC-балансировка round_robin по адресам, что
// вернул DNS, — это уже про несколько реплик Sender'а (§93.4). Без второй пул
// из восьми соединений спокойно улёгся бы весь в одну реплику.
//
// # Обе половины контракта keepalive живут здесь
//
// Пакет называется «клиент», но серверная enforcement-политика ([ServerKeepalivePolicy])
// лежит рядом намеренно: keepalive — это ДОГОВОР двух сторон, и §82.1 случился
// ровно потому, что половины жили порознь. Клиент пинговал каждые 30 секунд,
// сервер собирался дефолтами grpc-go (допустимый интервал — 5 минут) и на
// третьем ping'е рвал соединение вместе с идущим по нему вызовом. Ни один тест
// это не ловил, потому что каждая сторона по отдельности была корректна.
// Меняешь одну — смотри на вторую в том же файле.
package grpcsender

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	// Пустой импорт обязателен для healthCheckConfig в balancerServiceConfig:
	// клиентская health-проверка регистрируется через init() этого пакета, и
	// без него gRPC молча игнорирует настройку — мёртвая реплика осталась бы в
	// ротации, а конфиг выглядел бы правильным.
	_ "google.golang.org/grpc/health"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	otelpf "nexus/internal/platform/otel"
	"nexus/internal/platform/safego"
	senderv1 "nexus/proto/sender/v1"
)

// Client — пул gRPC-соединений к Sender.
type Client struct {
	conns   []*grpc.ClientConn
	clients []senderv1.SenderServiceClient
	next    atomic.Uint64
	logger  logging.Logger

	// readyWait — потолок ожидания готовности транспорта (§109.3); 0 —
	// ожидание выключено.
	readyWait time.Duration
	observer  ReadyWaitObserver
}

// Исходы ожидания готовности (§109.3) — метка outcome у
// nexus_sender_ready_wait_total. Множество замкнутое.
const (
	// ReadyWaitReady — транспорт стал готов, вызов ушёл.
	ReadyWaitReady = "ready"
	// ReadyWaitTimeout — за ready_wait_ms готовности не случилось, клиент
	// получит 503.
	ReadyWaitTimeout = "timeout"
	// ReadyWaitCanceled — контекст вызова закончился раньше (клиент ушёл).
	ReadyWaitCanceled = "canceled"
)

// ReadyWaitObserver — счётчик ожиданий готовности (§109.3). Объявлен здесь, на
// стороне потребителя: клиенту не нужно знать про Prometheus.
type ReadyWaitObserver interface {
	IncSenderReadyWait(outcome string)
}

// Option — функциональная опция New.
type Option func(*Client)

// WithReadyWaitObserver подключает счётчик ожиданий готовности. nil
// игнорируется — ожидание работает, но видно только в debug-логе.
func WithReadyWaitObserver(o ReadyWaitObserver) Option {
	return func(c *Client) {
		if o != nil {
			c.observer = o
		}
	}
}

// defaultReadyWait — потолок ожидания, если ready_wait_ms = 0 (§109.3). В лад
// с config.defaultSenderReadyWaitMs: сюда ноль доходит только в обход
// applyDefaults (тесты, ручная сборка конфига).
const defaultReadyWait = 30 * time.Second

// readyWaitFromConfig: <0 — выключено, 0 — дефолт, >0 — как задано.
func readyWaitFromConfig(ms int) time.Duration {
	switch {
	case ms < 0:
		return 0
	case ms == 0:
		return defaultReadyWait
	}
	return time.Duration(ms) * time.Millisecond
}

// connectParams — переподключение к Sender'у (§109.3). Дефолт gRPC растит паузу
// между попытками до 120 с, и после возврата Sender'а соединения пула ещё
// десятки секунд сидели в TRANSIENT_FAILURE. Линк внутрисетевой, попытка
// дешёвая — пауза не больше 2 с.
func connectParams() grpc.ConnectParams {
	return grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  100 * time.Millisecond,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   2 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}
}

// defaultMaxMessageBytes — fallback лимита gRPC-сообщения, если в конфиге не
// задан (0). 64 МиБ; согласован с config.defaults (defaultGRPCMaxMessageBytes).
const defaultMaxMessageBytes = 64 * 1024 * 1024

// balancerServiceConfig — политика балансировки соединения (§93.4).
//
// round_robin вместо дефолтного pick_first: при двух репликах Sender'а за одним
// именем pick_first выбирает ПЕРВЫЙ отвечающий адрес и держится за него, то
// есть весь трафик Receiver'а идёт в одну реплику, а вторая простаивает. Хуже
// того, это неотличимо от нормы по метрикам самого Receiver'а.
//
// healthCheckConfig включает клиентскую health-проверку по gRPC (Watch на
// grpc.health.v1.Health, который Sender регистрирует в app.go): реплика,
// ответившая NOT_SERVING, исключается из ротации ДО того, как в неё улетит
// запрос. Это и есть дренаж на стороне gRPC — у Sender'а нет балансировщика,
// который вывел бы его из ротации снаружи.
//
// Пустой serviceName означает «здоровье сервера целиком», как его и выставляет
// Sender (SetServingStatus("", SERVING)).
const balancerServiceConfig = `{
	"loadBalancingConfig": [{"round_robin": {}}],
	"healthCheckConfig": {"serviceName": ""}
}`

// resolverTarget приводит адрес из конфига к gRPC-target с DNS-резолвером.
//
// Резолвер обязателен именно как dns: он периодически перечитывает запись и
// видит ОБА адреса за общим сетевым алиасом пары реплик (deploy/docker-compose.ha.yml).
// Схему в адресе не трогаем — оператор мог задать target явно (dns:///, unix://,
// собственный резолвер), и переписать его значило бы молча сломать настройку.
func resolverTarget(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "dns:///" + addr
}

// New создаёт пул из cfg.PoolSize соединений. Соединения ленивые: подключение
// начинается с первого вызова.
func New(cfg *config.ReceiverSenderGRPCConfig, logger logging.Logger, opts ...Option) (*Client, error) {
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 1
	}
	maxMsg := cfg.MaxMessageBytes
	if maxMsg <= 0 {
		maxMsg = defaultMaxMessageBytes
	}
	conns := make([]*grpc.ClientConn, 0, cfg.PoolSize)
	clients := make([]senderv1.SenderServiceClient, 0, cfg.PoolSize)
	for i := 0; i < cfg.PoolSize; i++ {
		cc, err := grpc.NewClient(
			resolverTarget(cfg.Addr),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			// §93.4: round_robin по всем адресам, что вернул резолвер, плюс
			// исключение реплик, отвечающих NOT_SERVING. См. balancerServiceConfig.
			grpc.WithDefaultServiceConfig(balancerServiceConfig),
			grpc.WithConnectParams(connectParams()), // §109.3
			// §109.3: момент отправки вызова — для честного 503 и таймера
			// ожидания готовности. См. sendTracker.
			grpc.WithStatsHandler(sendTracker{}),
			// §109.3: канал не уходит в idle. pick предпочитает готовые
			// соединения, и канал, простоявший 30 минут (дефолт gRPC), обратно
			// бы уже не вернулся — весь sync шёл бы по одному соединению пула.
			// Простой соединения стоит лишь keepalive-пингов.
			grpc.WithIdleTimeout(0),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:    time.Duration(cfg.KeepaliveTimeSec) * time.Second,
				Timeout: time.Duration(cfg.KeepaliveTimeoutSec) * time.Second,
			}),
			// §42: лимит размера сообщения (оба направления). Дефолт recv 4 МиБ
			// рубит большой ответ апстрима (Sender→Receiver) ResourceExhausted.
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(maxMsg),
				grpc.MaxCallSendMsgSize(maxMsg),
			),
			// OTel client-span + traceparent injection в outgoing metadata
			// (§16 ТЗ, Phase 8.3). При Enable=false — no-op.
			grpc.WithUnaryInterceptor(otelpf.UnaryClientInterceptor()),
		)
		if err != nil {
			for _, prev := range conns {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("grpc.NewClient %s: %w", cfg.Addr, err)
		}
		// grpc.NewClient ленивый: без Connect канал ждёт первого вызова, а
		// первый вызов при этом ждёт соединения — и засчитывается ожиданием.
		cc.Connect()
		conns = append(conns, cc)
		clients = append(clients, senderv1.NewSenderServiceClient(cc))
	}
	c := &Client{conns: conns, clients: clients, logger: logger, readyWait: readyWaitFromConfig(cfg.ReadyWaitMs)}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Send выбирает соединение пула и выполняет RPC.
//
// §109.3: при включённом ожидании вызов идёт с WaitForReady — пока Sender
// перезапускается, он ждёт готового транспорта, а не падает сразу. Ожидание
// ограничено readyWait: таймер отменяет вызов, только пока тот не отправлен
// (callState), и такой вызов получает domain.ErrSenderUnavailable. Ушедший
// вызов таймер не трогает — его ограничивает лишь контекст вызывающего. До
// отправки у Sender'а ничего нет, поэтому ожидание не создаёт дублей у
// получателя — в отличие от retryPolicy gRPC, которая повторяет и уже ушедший
// вызов.
func (c *Client) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	if len(c.clients) == 0 {
		return nil, fmt.Errorf("grpcsender: no clients")
	}
	idx := c.pick()
	wasReady := c.conns[idx].GetState() == connectivity.Ready
	call := &callState{}
	callCtx, opts, stop := c.boundWait(withCallState(ctx, call), call)
	defer stop()
	started := time.Now()
	resp, err := c.clients[idx].Send(callCtx, req, opts...)
	c.observeWait(ctx, call, wasReady, started)
	if err != nil {
		return nil, translateError(ctx, err, call, c.readyWait)
	}
	return resp, nil
}

// pick — индекс соединения: первое готовое, начиная с очередного по
// round-robin; готовых нет — очередное. Обход дешёвый: GetState читает
// состояние без блокировки канала, соединений в пуле единицы.
//
// Пропущенное соединение в IDLE будится Connect: из IDLE канал выходит только
// по вызову или Connect, а вызовов ему, пока рядом есть готовое, pick не даёт —
// без побудки пул схлопывался до одного соединения (ревизия §109).
func (c *Client) pick() int {
	n := uint64(len(c.conns))
	start := c.next.Add(1)
	for i := range n {
		j := (start + i) % n
		switch c.conns[j].GetState() {
		case connectivity.Ready:
			return int(j)
		case connectivity.Idle:
			c.conns[j].Connect()
		}
	}
	return int(start % n)
}

// boundWait включает ограниченное ожидание готовности (§109.3):
// WaitForReady плюс таймер readyWait, отменяющий вызов, пока тот не отправлен.
// Ожидание выключено — вызов fail-fast, как до §109.3. Возвращённый stop
// обязателен к вызову: он гасит таймер и контекст.
func (c *Client) boundWait(ctx context.Context, call *callState) (context.Context, []grpc.CallOption, func()) {
	if c.readyWait <= 0 {
		return ctx, nil, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(c.readyWait, func() {
		defer safego.Recover(c.logger, "grpcsender.readyWaitExpire")
		if call.expire() {
			cancel()
		}
	})
	return ctx, []grpc.CallOption{grpc.WaitForReady(true)}, func() {
		timer.Stop()
		cancel()
	}
}

// observeWait учитывает ожидание готовности: счётчик исходов и debug-строка
// (§51.9: иначе «почему вызов шёл 12 секунд» не объяснить). Вызов, ушедший
// сразу, не учитывается.
func (c *Client) observeWait(ctx context.Context, call *callState, wasReady bool, started time.Time) {
	if c.readyWait <= 0 {
		return
	}
	outcome := waitOutcome(ctx, call, wasReady)
	if outcome == "" {
		return
	}
	if c.observer != nil {
		c.observer.IncSenderReadyWait(outcome)
	}
	c.logger.Debug("grpcsender: waited for sender connection",
		c.logger.Str("outcome", outcome),
		c.logger.Any("was_ready", wasReady),
		c.logger.Int("waited_ms", int(call.waited(started).Milliseconds())))
}

// waitOutcome — исход ожидания готовности; "" — ожидания не было. Ждал вызов,
// начатый на неготовом соединении, либо тот, чей выбор транспорта
// блокировался: второе ловит соединение, которое после падения Sender'а ещё
// числилось READY.
func waitOutcome(ctx context.Context, call *callState, wasReady bool) string {
	waited := !wasReady || call.pickDelayed()
	switch {
	case call.expired():
		return ReadyWaitTimeout
	case !waited:
		return ""
	case call.sent():
		return ReadyWaitReady
	case ctx.Err() != nil:
		return ReadyWaitCanceled
	}
	return "" // не отправлен по иной причине — это не исход ожидания
}

// translateError переводит транспортные коды gRPC в ошибки, которые слои выше
// понимают без импорта gRPC. Исходная ошибка остаётся в цепочке: код и текст
// видны в логах как прежде.
//
//   - Canceled при отменённом контексте вызывающего → context.Canceled
//     (§109.4): статус-ошибка gRPC причину не оборачивает, и уход клиента
//     выглядел сбоем шины.
//   - readyWait истёк до отправки, либо Unavailable у НЕотправленного вызова →
//     domain.ErrSenderUnavailable (§109.3): до Sender'а вызов не дошёл.
//     Unavailable у отправленного — обрыв посреди вызова (урок §82.1), исход у
//     получателя неизвестен; он остаётся как есть — 502 и ERROR.
func translateError(ctx context.Context, err error, call *callState, readyWait time.Duration) error {
	switch code := status.Code(err); {
	case code == codes.Canceled && errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("%w: %w", context.Canceled, err)
	case call.expired():
		return fmt.Errorf("%w: not sent within ready_wait %s: %w", domain.ErrSenderUnavailable, readyWait, err)
	case code == codes.Unavailable && !call.sent():
		return fmt.Errorf("%w: %w", domain.ErrSenderUnavailable, err)
	}
	return err
}

func (c *Client) Close() error {
	var firstErr error
	for _, cc := range c.conns {
		if err := cc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
