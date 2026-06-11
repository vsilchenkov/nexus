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
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ErrorSink — приёмник событий «проверка лимита упала, пропущено fail-open»
// (Phase AUD.8, D.4). Интерфейс на стороне consumer'а: metrics.Metrics
// реализует его методом IncRateLimitCheckError(scope).
type ErrorSink interface {
	IncRateLimitCheckError(scope string)
}

type Limiter struct {
	client *goredis.Client
	sink   ErrorSink
}

// Option — функциональные опции Limiter.
type Option func(*Limiter)

// WithErrorSink включает учёт сбоев проверки лимита (Prometheus-счётчик
// nexus_ratelimit_check_errors_total): лимиты fail-open по §9.4, и без
// этого сигнала их фактическое отключение при лежащем Redis невидимо.
func WithErrorSink(s ErrorSink) Option {
	return func(l *Limiter) { l.sink = s }
}

func New(client *goredis.Client, opts ...Option) *Limiter {
	l := &Limiter{client: client}
	for _, o := range opts {
		o(l)
	}
	return l
}

// keyScope — префикс ключа до первого ':' (login, replay, kafka, ...);
// ограничивает кардинальность метрики.
func keyScope(key string) string {
	if i := strings.IndexByte(key, ':'); i > 0 {
		return key[:i]
	}
	return key
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
		if l.sink != nil {
			l.sink.IncRateLimitCheckError(keyScope(key))
		}
		return true, err
	}
	if int(incr.Val()) > limitPerMin {
		return false, nil
	}
	return true, nil
}
