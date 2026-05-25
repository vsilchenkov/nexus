// Package kafka — Sender-сторона async-обработки. Запускает N consumer-
// горутин (= partitions), каждая блокирующе читает FetchMessage и
// делегирует Handle в usecase. Commit только после Ack/DLQed.
package kafka

import (
	"context"
	"errors"
	"sync"

	"bus/internal/platform/config"
	kafkapf "bus/internal/platform/kafka"
	"bus/internal/platform/logging"
	"bus/internal/sender/usecase"
)

// ConsumerGroup — пул из cfg.Kafka.Consumer.Instances consumer-горутин.
type ConsumerGroup struct {
	cfg       *config.Config
	processor *usecase.AsyncProcessor
	logger    logging.Logger
	topic     string

	consumers []*kafkapf.Consumer
	wg        sync.WaitGroup
}

func NewConsumerGroup(
	cfg *config.Config,
	topic string,
	processor *usecase.AsyncProcessor,
	logger logging.Logger,
) *ConsumerGroup {
	return &ConsumerGroup{
		cfg:       cfg,
		processor: processor,
		logger:    logger,
		topic:     topic,
	}
}

// Start запускает горутины. Возвращается сразу — горутины крутятся в фоне.
// Останавливаются через Stop().
func (g *ConsumerGroup) Start(ctx context.Context) {
	instances := g.cfg.Kafka.Consumer.Instances
	if instances <= 0 {
		instances = 1
	}
	for i := 0; i < instances; i++ {
		c := kafkapf.NewConsumer(g.cfg, g.topic)
		g.consumers = append(g.consumers, c)
		g.wg.Add(1)
		go g.runOne(ctx, c, i)
	}
}

func (g *ConsumerGroup) runOne(ctx context.Context, c *kafkapf.Consumer, idx int) {
	defer g.wg.Done()
	g.logger.Info("async consumer started",
		g.logger.Str("topic", g.topic),
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
			g.logger.Warn("fetch message failed",
				g.logger.Str("topic", g.topic),
				g.logger.Err(err))
			continue
		}

		res := g.processor.Handle(ctx, msg.Value)
		switch res {
		case usecase.HandleAck, usecase.HandleDLQed:
			if err := c.Commit(ctx, msg); err != nil {
				g.logger.ErrorWithOp("commit offset failed", err, "kafka.consumer.commit",
					g.logger.Str("topic", g.topic),
					g.logger.Int("partition", msg.Partition))
			}
		case usecase.HandleRetry:
			// Не коммитим — сообщение будет прочитано снова при следующем
			// FetchMessage в этом инстансе либо при rebalance. Для paused
			// это нормальное поведение (§3.6).
		}
	}
}

// Stop корректно завершает все consumer-горутины.
func (g *ConsumerGroup) Stop() {
	for _, c := range g.consumers {
		_ = c.Close()
	}
	g.wg.Wait()
}
