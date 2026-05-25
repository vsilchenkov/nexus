// Package ratelimit — простой fixed-window-counter rate limiter в Redis
// (§9.5 ТЗ). Ключ: ratelimit:{key}:{minute}; INCR + EXPIRE на минуту.
//
// Достоинства: O(1) на проверку, согласованность между всеми инстансами
// Receiver (один Redis на всех). Недостатки: всплеск на стыке окон
// (для шины — приемлемо, у нас не финтех).
package ratelimit

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Limiter struct {
	client *goredis.Client
}

func New(client *goredis.Client) *Limiter {
	return &Limiter{client: client}
}

// Allow возвращает true, если в текущей минуте по ключу было <= limit
// запросов (включая текущий). При limit=0 — без ограничений.
// При недоступности Redis — true (fail-open, §9.4 ТЗ: «при сбое Redis
// rate limit временно отключается»).
func (l *Limiter) Allow(ctx context.Context, key string, limitPerMin int) (bool, error) {
	if limitPerMin <= 0 {
		return true, nil
	}
	minute := time.Now().UTC().Unix() / 60
	rkey := fmt.Sprintf("ratelimit:%s:%d", key, minute)

	pipe := l.client.TxPipeline()
	incr := pipe.Incr(ctx, rkey)
	pipe.Expire(ctx, rkey, 65*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		// fail-open: Redis недоступен → пропускаем без лимита
		return true, err
	}
	if int(incr.Val()) > limitPerMin {
		return false, nil
	}
	return true, nil
}
