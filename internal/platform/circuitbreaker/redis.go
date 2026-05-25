// Package circuitbreaker — Redis-backed CB (§9.5 ТЗ).
//
// Состояние per-key (обычно key = node_path) с тремя положениями:
//   closed    — обычная работа;
//   open      — после consecutive failures >= threshold; запросы
//               отбрасываются на cooldown секунд;
//   half_open — после cooldown один пробный запрос; success → closed,
//               failure → opens обратно.
//
// Реализация — два Redis-поля per key: failures (int) + state (string)
// + opened_at (unix sec).
package circuitbreaker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

type Breaker struct {
	client    *goredis.Client
	threshold int
	cooldown  time.Duration
}

func New(client *goredis.Client, threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{client: client, threshold: threshold, cooldown: cooldown}
}

// Allow возвращает true, если breaker разрешает запрос.
// open + cooldown ещё не истёк → false (запрос блокируется);
// иначе true (включая half_open — один пробный запрос).
func (b *Breaker) Allow(ctx context.Context, key string) (bool, error) {
	k := "circuit:" + key
	res, err := b.client.HGetAll(ctx, k).Result()
	if err != nil {
		// fail-open: считаем closed (§9.4 ТЗ)
		return true, err
	}
	state := State(res["state"])
	if state == "" || state == StateClosed {
		return true, nil
	}
	if state == StateOpen {
		ts, _ := strconv.ParseInt(res["opened_at"], 10, 64)
		openedAt := time.Unix(ts, 0)
		if time.Since(openedAt) >= b.cooldown {
			// Переводим в half_open — пускаем один пробный.
			_ = b.client.HSet(ctx, k, "state", string(StateHalfOpen)).Err()
			return true, nil
		}
		return false, nil
	}
	// half_open: пускаем — следующий RecordSuccess/Failure определит судьбу.
	return true, nil
}

// RecordSuccess сбрасывает счётчик ошибок и устанавливает closed.
func (b *Breaker) RecordSuccess(ctx context.Context, key string) error {
	k := "circuit:" + key
	pipe := b.client.TxPipeline()
	pipe.HSet(ctx, k, "state", string(StateClosed), "failures", 0)
	pipe.HDel(ctx, k, "opened_at")
	pipe.Expire(ctx, k, 10*time.Minute) // авто-чистка
	_, err := pipe.Exec(ctx)
	return err
}

// RecordFailure инкрементирует счётчик, при достижении порога — open.
func (b *Breaker) RecordFailure(ctx context.Context, key string) error {
	k := "circuit:" + key
	failures, err := b.client.HIncrBy(ctx, k, "failures", 1).Result()
	if err != nil {
		return err
	}
	_ = b.client.Expire(ctx, k, 10*time.Minute).Err()
	if int(failures) >= b.threshold {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		return b.client.HSet(ctx, k, "state", string(StateOpen), "opened_at", ts).Err()
	}
	return nil
}

// State возвращает текущее состояние (для диагностики/UI).
func (b *Breaker) State(ctx context.Context, key string) (State, error) {
	v, err := b.client.HGet(ctx, "circuit:"+key, "state").Result()
	if err != nil {
		if err == goredis.Nil {
			return StateClosed, nil
		}
		return "", fmt.Errorf("hget: %w", err)
	}
	if v == "" {
		return StateClosed, nil
	}
	return State(v), nil
}
