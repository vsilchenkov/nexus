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
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	// Пустой импорт обязателен для healthCheckConfig в balancerServiceConfig:
	// клиентская health-проверка регистрируется через init() этого пакета, и
	// без него gRPC молча игнорирует настройку — мёртвая реплика осталась бы в
	// ротации, а конфиг выглядел бы правильным.
	_ "google.golang.org/grpc/health"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	otelpf "nexus/internal/platform/otel"
	senderv1 "nexus/proto/sender/v1"
)

// Client — пул gRPC-соединений к Sender.
type Client struct {
	conns   []*grpc.ClientConn
	clients []senderv1.SenderServiceClient
	next    atomic.Uint64
	logger  logging.Logger
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

func New(cfg *config.ReceiverSenderGRPCConfig, logger logging.Logger) (*Client, error) {
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
		conns = append(conns, cc)
		clients = append(clients, senderv1.NewSenderServiceClient(cc))
	}
	return &Client{conns: conns, clients: clients, logger: logger}, nil
}

// Send выбирает round-robin соединение и выполняет RPC.
func (c *Client) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	if len(c.clients) == 0 {
		return nil, fmt.Errorf("grpcsender: no clients")
	}
	idx := c.next.Add(1) % uint64(len(c.clients))
	return c.clients[idx].Send(ctx, req)
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
