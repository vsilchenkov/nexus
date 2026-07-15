package logsink

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisWriter — реализация BatchWriter поверх Redis: pipeline
// LPUSH (свежие в голове) + LTRIM до capN + EXPIRE (§51.3 ТЗ).
type RedisWriter struct {
	rdb *goredis.Client
}

var _ BatchWriter = (*RedisWriter)(nil)

// NewRedisWriter создаёт писателя поверх готового клиента Redis.
func NewRedisWriter(rdb *goredis.Client) *RedisWriter {
	return &RedisWriter{rdb: rdb}
}

// Ship атомарно (pipeline) кладёт строки в голову списка, обрезает его до capN
// и продлевает TTL ключа.
func (w *RedisWriter) Ship(ctx context.Context, key string, lines [][]byte, capN int64, ttl time.Duration) error {
	if len(lines) == 0 {
		return nil
	}
	values := make([]any, len(lines))
	for i, l := range lines {
		values[i] = l
	}
	pipe := w.rdb.Pipeline()
	pipe.LPush(ctx, key, values...)
	pipe.LTrim(ctx, key, 0, capN-1)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("logsink: redis pipeline for %s: %w", key, err)
	}
	return nil
}
