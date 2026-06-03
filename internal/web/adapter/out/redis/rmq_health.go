package redis

import (
	"context"
	"encoding/json"
	"errors"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
)

// rmqHealthKey — должен совпадать с rabbitmq.RedisHealthKey в Receiver (§27.8).
// Дублируется константой, чтобы Web не зависел от пакета receiver-адаптера.
const rmqHealthKey = "rmq:health"

// RMQHealthReaderRedis читает health-снимки Puller-воркеров, которые Receiver
// публикует в Redis-hash rmq:health (field = node path). Общий стор вместо
// нового gRPC Receiver→Web (§27.8).
type RMQHealthReaderRedis struct {
	client *goredis.Client
}

func NewRMQHealthReaderRedis(client *goredis.Client) *RMQHealthReaderRedis {
	return &RMQHealthReaderRedis{client: client}
}

// Get возвращает health узла по path. Отсутствие поля → (nil, nil): воркер ещё
// не отчитался или узел не RabbitMQAsync — UI трактует как «нет данных».
func (r *RMQHealthReaderRedis) Get(ctx context.Context, nodePath string) (*domain.RMQHealth, error) {
	data, err := r.client.HGet(ctx, rmqHealthKey, nodePath).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var h domain.RMQHealth
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
