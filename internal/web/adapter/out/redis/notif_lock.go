package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// notifLockKey — распределённый лок планировщика уведомлений (§20.4). При
// нескольких репликах Web cron сработает в каждой; лок гарантирует ровно одну
// отправку на тик. Не освобождается вручную — полагаемся на TTL (исключает
// удаление чужого лока).
const notifLockKey = "nexus:notif:lock"

// NotifLockRedis — Redis SETNX-лок для планировщика уведомлений.
type NotifLockRedis struct {
	client *goredis.Client
}

func NewNotifLock(client *goredis.Client) *NotifLockRedis {
	return &NotifLockRedis{client: client}
}

// TryLock пытается захватить лок на ttl. Возвращает true, если лок взят
// (эта реплика выполняет цикл), false — если уже захвачен другой репликой.
func (r *NotifLockRedis) TryLock(ctx context.Context, ttl time.Duration) (bool, error) {
	ok, err := r.client.SetNX(ctx, notifLockKey, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("notif lock: %w", err)
	}
	return ok, nil
}
