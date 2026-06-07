package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/web/usecase/port"
)

// kafkaCachePrefix — префикс ключей кеша Kafka-метаданных в Redis.
const kafkaCachePrefix = "kafka:cache:"

// KafkaCacheRedis кеширует ответы Kafka Admin (топики/брокеры) в Redis с общим
// TTL (§3.2 spec, по умолчанию 30с). Значения сериализуются в JSON.
type KafkaCacheRedis struct {
	client *goredis.Client
	ttl    time.Duration
}

var _ port.KafkaCache = (*KafkaCacheRedis)(nil)

// NewKafkaCacheRedis создаёт кеш с заданным TTL (<=0 → 30с).
func NewKafkaCacheRedis(client *goredis.Client, ttl time.Duration) *KafkaCacheRedis {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &KafkaCacheRedis{client: client, ttl: ttl}
}

// Get снимает значение ключа в dst (указатель). Отсутствие/просрочка →
// (false, nil). Ошибка десериализации трактуется как промах (не фатально).
func (c *KafkaCacheRedis) Get(ctx context.Context, key string, dst any) (bool, error) {
	data, err := c.client.Get(ctx, kafkaCachePrefix+key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, err
	}
	// Битый/несовместимый кеш трактуем как промах — usecase сходит в Kafka.
	if json.Unmarshal(data, dst) != nil {
		return false, nil //nolint:nilerr // повреждённый кеш = промах, не ошибка вызывающего
	}
	return true, nil
}

// Set сериализует val в JSON и кладёт под ключ с TTL.
func (c *KafkaCacheRedis) Set(ctx context.Context, key string, val any) error {
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, kafkaCachePrefix+key, data, c.ttl).Err()
}
