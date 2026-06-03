// Package redis — Redis-реализации port-интерфейсов Web Service.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeCacheRedis реализует port.NodeCache (§5.4 ТЗ).
//
// Ключи:
//
//	node:{team_slug}:{path} — сериализованный JSON конфига узла (plaintext-креды).
//
// ВАЖНО: формат ключа обязан совпадать с тем, что читает Receiver
// (internal/receiver/adapter/out/nodecache: "node:<team_slug>:<path>"). Иначе
// write-through и инвалидация из Web не доходят до ключа Receiver, и изменения
// узла вступают в силу только по истечении Redis-TTL (до 5 мин) вместо ~мгновенно.
// В v1 команда всегда 'default' (team_id='default', §0), поэтому используем
// domain.DefaultTeamSlug; multi-tenancy v2 потребует резолва реального slug
// команды узла здесь (см. resolveCHDatabase в usecase/node.go как образец).
//
// Поскольку в Redis ложится plaintext (для быстрого использования
// Receiver'ом), Redis должен жить в защищённом контуре. Это сознательный
// компромисс §5.4: «Redis содержит только производные и временные данные».
type NodeCacheRedis struct {
	client *goredis.Client
	logger logging.Logger
}

var _ port.NodeCache = (*NodeCacheRedis)(nil)

func NewNodeCacheRedis(client *goredis.Client, logger logging.Logger) *NodeCacheRedis {
	return &NodeCacheRedis{client: client, logger: logger}
}

func nodeKey(path string) string { return "node:" + domain.DefaultTeamSlug + ":" + path }

func (c *NodeCacheRedis) GetByPath(ctx context.Context, path string) (*domain.Node, error) {
	data, err := c.client.Get(ctx, nodeKey(path)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("redis get %s: %w", nodeKey(path), err)
	}
	var n domain.Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	return &n, nil
}

func (c *NodeCacheRedis) Set(ctx context.Context, n *domain.Node, ttl time.Duration) error {
	data, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	if err := c.client.Set(ctx, nodeKey(n.Path), data, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %s: %w", nodeKey(n.Path), err)
	}
	return nil
}

func (c *NodeCacheRedis) InvalidateByPath(ctx context.Context, path string) error {
	if err := c.client.Del(ctx, nodeKey(path)).Err(); err != nil {
		return fmt.Errorf("redis del %s: %w", nodeKey(path), err)
	}
	return nil
}
