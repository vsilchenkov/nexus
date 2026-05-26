package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

const sessionPrefix = "session:"

type SessionRepoRedis struct {
	client *goredis.Client
}

var _ port.SessionRepo = (*SessionRepoRedis)(nil)

func NewSessionRepoRedis(client *goredis.Client) *SessionRepoRedis {
	return &SessionRepoRedis{client: client}
}

func sessionKey(token string) string { return sessionPrefix + token }

func (r *SessionRepoRedis) Create(ctx context.Context, s *domain.Session, ttl time.Duration) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := r.client.Set(ctx, sessionKey(s.Token), data, ttl).Err(); err != nil {
		return fmt.Errorf("set session: %w", err)
	}
	return nil
}

func (r *SessionRepoRedis) Get(ctx context.Context, token string) (*domain.Session, error) {
	data, err := r.client.Get(ctx, sessionKey(token)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	var s domain.Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &s, nil
}

func (r *SessionRepoRedis) Touch(ctx context.Context, token string, ttl time.Duration) error {
	return r.client.Expire(ctx, sessionKey(token), ttl).Err()
}

func (r *SessionRepoRedis) Delete(ctx context.Context, token string) error {
	return r.client.Del(ctx, sessionKey(token)).Err()
}

// DeleteByUser — SCAN session:* + проверка user_id. O(n) от количества
// активных сессий — выполняется редко (смена роли, отключение, смена
// пароля; §7.1 ТЗ).
func (r *SessionRepoRedis) DeleteByUser(ctx context.Context, userID string) (int, error) {
	var cursor uint64
	deleted := 0
	for {
		keys, next, err := r.client.Scan(ctx, cursor, sessionPrefix+"*", 100).Result()
		if err != nil {
			return deleted, fmt.Errorf("scan sessions: %w", err)
		}
		for _, k := range keys {
			data, err := r.client.Get(ctx, k).Bytes()
			if err != nil {
				continue
			}
			var s domain.Session
			if err := json.Unmarshal(data, &s); err != nil {
				continue
			}
			if s.UserID == userID {
				if err := r.client.Del(ctx, k).Err(); err == nil {
					deleted++
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return deleted, nil
}
