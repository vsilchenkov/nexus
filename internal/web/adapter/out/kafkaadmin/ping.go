package kafkaadmin

import (
	"context"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"golang.org/x/sync/errgroup"

	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// Ping опрашивает каждый брокер ApiVersions-запросом и замеряет отклик (§4.5
// spec, §114.4). Брокеры пингуются параллельно; warn выставляется при задержке выше
// порога warnPingMs. Порядок результатов — как в конфиге брокеров.
func (c *Client) Ping(ctx context.Context) ([]port.BrokerPing, error) {
	res := make([]port.BrokerPing, len(c.brokers))
	g, gctx := errgroup.WithContext(ctx)
	for i, addr := range c.brokers {
		g.Go(func() error {
			// §30.2 / CLAUDE.md §1: гасим панику горутины, не роняем процесс.
			defer safego.Recover(c.logger, "web.kafkaPing")
			res[i] = c.pingBroker(gctx, addr)
			return nil
		})
	}
	// Горутины не возвращают ошибок (фиксируются в BrokerPing.OK) — Wait
	// нужен лишь как барьер; ошибку игнорируем осознанно.
	_ = g.Wait()
	return res, nil
}

// pingBroker подключается к одному брокеру и выполняет ApiVersions, измеряя
// длительность. Ошибка → OK=false; задержка выше порога → warn.
//
// §114.4: не Metadata — её kafka-go отдаёт из кеша транспорта, и упавший
// брокер отвечал «OK» с задержкой в доли миллисекунды. ApiVersions уходит на
// брокер всегда.
func (c *Client) pingBroker(ctx context.Context, addr string) port.BrokerPing {
	kc := &kafka.Client{Addr: kafka.TCP(addr), Timeout: c.timeout}
	start := time.Now()
	resp, err := kc.ApiVersions(ctx, &kafka.ApiVersionsRequest{})
	if err == nil && resp.Error != nil {
		err = resp.Error
	}
	elapsed := time.Since(start).Milliseconds()
	bp := port.BrokerPing{Addr: addr, ElapsedMs: elapsed, OK: err == nil}
	switch {
	case err != nil:
		bp.Warn = "unreachable"
	case elapsed >= c.warnPingMs:
		bp.Warn = "elevated latency"
	}
	return bp
}
