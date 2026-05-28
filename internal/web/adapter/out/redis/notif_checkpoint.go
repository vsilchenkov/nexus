package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// notifLastCheckKey — unix-ms времени последней проверки ошибок (§20.3).
// Окно подсчёта ошибок: (last_check, now]. Хранится в Redis ради
// multi-instance и переживания рестартов Web.
const notifLastCheckKey = "nexus:notif:last_check"

// NotifCheckpointRedis — хранилище отметки последней проверки уведомлений.
type NotifCheckpointRedis struct {
	client *goredis.Client
}

func NewNotifCheckpoint(client *goredis.Client) *NotifCheckpointRedis {
	return &NotifCheckpointRedis{client: client}
}

// Get возвращает unix-ms последней проверки или 0, если отметки ещё нет.
func (r *NotifCheckpointRedis) Get(ctx context.Context) (int64, error) {
	v, err := r.client.Get(ctx, notifLastCheckKey).Int64()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("get notif checkpoint: %w", err)
	}
	return v, nil
}

// Set сохраняет отметку (unix-ms). Без TTL — отметка живёт между запусками.
func (r *NotifCheckpointRedis) Set(ctx context.Context, ms int64) error {
	if err := r.client.Set(ctx, notifLastCheckKey, ms, 0).Err(); err != nil {
		return fmt.Errorf("set notif checkpoint: %w", err)
	}
	return nil
}
