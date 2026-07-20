package kafka

import (
	"context"
	"errors"
	"sync"
	"time"

	"nexus/internal/platform/config"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/sender/usecase"
)

// messageProcessor — обработка одного Kafka-сообщения. Реализуется
// *usecase.AsyncProcessor (основной async-поток) и *ChLogRetryHandler
// (retry-топик CH). Определён на стороне consumer'а (CLAUDE.md §3).
type messageProcessor interface {
	Handle(ctx context.Context, value []byte, headers map[string]string) usecase.HandleResult
}

// Бэкофф повтора INSERT при недоступном CH: от retryBackoffMin до retryBackoffMax.
const (
	retryBackoffMin = 1 * time.Second
	retryBackoffMax = 15 * time.Second
)

// ChLogRetryConsumer — consumer-group для топика nexus.logs.retry (§38),
// дренит проваленные CH-батчи обратно в ClickHouse.
//
// Семантика отличается от основного async-consumer'а: при HandleRetry (CH ещё
// лежит) сообщение НЕ пропускается, а ПЕРЕОБРАБАТЫВАЕТСЯ на месте с бэкоффом до
// успеха или отмены ctx — только потом commit. Это нужно, потому что kafka-go
// FetchMessage без commit'а двигает внутренний курсор и сам по себе не
// передоставляет сообщение на том же reader'е (только при rebalance/restart).
// Retry-in-place гарантирует, что батч доедет в CH сразу после восстановления.
type ChLogRetryConsumer struct {
	cfg       *config.Config
	topic     string
	groupID   string
	handler   messageProcessor
	logger    logging.Logger
	consumers []*kafkapf.Consumer
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewChLogRetryConsumer создаёт consumer для retry-топика.
func NewChLogRetryConsumer(cfg *config.Config, topic, groupID string, handler messageProcessor, logger logging.Logger) *ChLogRetryConsumer {
	return &ChLogRetryConsumer{cfg: cfg, topic: topic, groupID: groupID, handler: handler, logger: logger}
}

// Start запускает Instances горутин. Возвращается сразу. Горутины завязаны на
// собственный производный контекст, который Stop отменяет — поэтому Stop не
// зависит от того, отменил ли вызывающий внешний ctx (иначе FetchMessage после
// Close мог бы крутиться до дедлайна внешнего ctx).
func (g *ChLogRetryConsumer) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	instances := g.cfg.Kafka.Consumer.Instances
	if instances <= 0 {
		instances = 1
	}
	for i := 0; i < instances; i++ {
		c := kafkapf.NewConsumerWithGroup(g.cfg, g.topic, g.groupID)
		g.consumers = append(g.consumers, c)
		g.wg.Add(1)
		go g.runOne(cctx, c, i)
	}
}

func (g *ChLogRetryConsumer) runOne(ctx context.Context, c *kafkapf.Consumer, idx int) {
	defer g.wg.Done()
	defer safego.Recover(g.logger, "sender.clogRetryConsumer")
	g.logger.Info("clog-retry consumer started",
		g.logger.Str("topic", g.topic),
		g.logger.Str("group", g.groupID),
		g.logger.Int("idx", idx))

	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := c.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			g.logger.Warn("clog-retry fetch failed", g.logger.Str("topic", g.topic), g.logger.Err(err))
			continue
		}
		hdrs := headersToMap(msg.Headers)
		if !g.processWithRetry(ctx, msg.Value, hdrs) {
			return // ctx отменён в середине бэкоффа — выходим без commit'а
		}
		if err := c.Commit(ctx, msg); err != nil {
			g.logger.ErrorWithOp("clog-retry commit failed", err, "kafka.clogRetry.commit",
				g.logger.Str("topic", g.topic), g.logger.Int("partition", msg.Partition))
		}
	}
}

// processWithRetry повторяет Handle на месте, пока он не вернёт не-Retry
// (Ack — успех или битое сообщение). Возвращает false, если прерван ctx
// (тогда commit не делается и батч передоставится позже). Бэкофф растёт от
// retryBackoffMin до retryBackoffMax.
func (g *ChLogRetryConsumer) processWithRetry(ctx context.Context, value []byte, hdrs map[string]string) bool {
	backoff := retryBackoffMin
	for {
		if g.handler.Handle(ctx, value, hdrs) != usecase.HandleRetry {
			return true
		}
		// CH всё ещё недоступен — ждём и пробуем ТОТ ЖЕ батч снова.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < retryBackoffMax {
			backoff *= 2
			if backoff > retryBackoffMax {
				backoff = retryBackoffMax
			}
		}
	}
}

// Stop корректно завершает все горутины: отменяет производный контекст (чтобы
// FetchMessage/бэкофф вышли немедленно), затем закрывает reader'ы и ждёт.
func (g *ChLogRetryConsumer) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
	for _, c := range g.consumers {
		_ = c.Close()
	}
	g.wg.Wait()
}

// Snapshot — lag по retry-топику (для Prometheus, как у основного consumer'а).
func (g *ChLogRetryConsumer) Snapshot() []LagSnapshot {
	out := make([]LagSnapshot, 0, len(g.consumers))
	for _, c := range g.consumers {
		s := c.Stats()
		out = append(out, LagSnapshot{Topic: s.Topic, Partition: s.Partition, Lag: s.Lag})
	}
	return out
}

// Group — имя consumer-group (для меток метрик).
func (g *ChLogRetryConsumer) Group() string { return g.groupID }
