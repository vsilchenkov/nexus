package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/receiver/usecase"
)

// RedisHealthKey — Redis-hash, куда Puller-воркеры пишут health-снимки
// (field = id узла, §101.7; value = JSON RMQHealth). Web читает оттуда для §27.8.
// Общий стор вместо нового gRPC Receiver→Web.
const RedisHealthKey = "rmq:health"

// healthTTL — снимок протухает, если воркер давно не обновлял (узел удалён/
// инстанс упал). Web трактует отсутствие поля как «нет данных».
const healthTTL = 2 * time.Minute

// HealthSink реализует usecase.HealthSink поверх Redis.
type HealthSink struct {
	client *goredis.Client
}

var _ usecase.HealthSink = (*HealthSink)(nil)

func NewHealthSink(client *goredis.Client) *HealthSink {
	return &HealthSink{client: client}
}

// Publish пишет снимок в hash и обновляет TTL ключа. Fail-soft: ошибка Redis
// не должна ронять воркер (health — вспомогательный сигнал).
func (s *HealthSink) Publish(ctx context.Context, h domain.RMQHealth) error {
	if h.NodeID == "" {
		// Поле по пути склеило бы одноимённые pull-узлы разных команд (§101.7),
		// а пустое поле — вообще все такие снимки.
		return fmt.Errorf("rmq health: node %q has no id", h.NodePath)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	pipe := s.client.TxPipeline()
	pipe.HSet(ctx, RedisHealthKey, h.NodeID, data)
	pipe.Expire(ctx, RedisHealthKey, healthTTL)
	_, err = pipe.Exec(ctx)
	return err
}
