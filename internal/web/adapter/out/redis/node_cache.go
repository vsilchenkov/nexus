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
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeCacheRedis реализует port.NodeCache (§5.4 ТЗ).
//
// Ключи:
//
//	node:{team_slug}:{path} — сериализованный JSON конфига узла (креды шифрованы).
//
// ВАЖНО (§50): формат ключа И формат кредов обязаны совпадать с тем, что читает
// Receiver (internal/receiver/adapter/out/nodecache). Иначе write-through и
// инвалидация из Web не доходят до ключа Receiver, и изменения узла (в т.ч.
// выключение и удаление!) вступают в силу только по истечении Redis-TTL
// (redis.node_ttl_sec, до 5 мин) вместо ~мгновенно.
//
// Грабли, ради которых написан этот абзац: до §50 ключ хардкодил
// domain.DefaultTeamSlug («в v1 команда всегда default»), и после §18
// (multi-tenancy) любая правка узла вне команды default промахивалась мимо
// ключа Receiver — на бою узел 5 минут ходил на старый target_url.
//
// Креды шифруются тем же AES-256-GCM, что и в PostgreSQL (Phase AUD.5): Receiver
// читает кеш и делает Decrypt, поэтому plaintext-запись он трактует как
// cache-miss. domain.Node за пределами адаптера всегда содержит plaintext.
type NodeCacheRedis struct {
	client *goredis.Client
	cipher *crypto.Cipher
	logger logging.Logger
}

var _ port.NodeCache = (*NodeCacheRedis)(nil)

func NewNodeCacheRedis(client *goredis.Client, cipher *crypto.Cipher, logger logging.Logger) *NodeCacheRedis {
	return &NodeCacheRedis{client: client, cipher: cipher, logger: logger}
}

// nodeKey — ключ Redis. Должен совпадать с nodeKey в
// internal/receiver/adapter/out/nodecache/reader.go.
func nodeKey(teamSlug, path string) string {
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	return "node:" + teamSlug + ":" + path
}

func (c *NodeCacheRedis) GetByPath(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	key := nodeKey(teamSlug, path)
	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("redis get %s: %w", key, err)
	}
	var n domain.Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	if n.AuthCredentials, err = c.cipher.Decrypt(n.AuthCredentials); err != nil {
		return nil, fmt.Errorf("decrypt cached auth: %w", err)
	}
	if n.IncomingAuthCredentials, err = c.cipher.Decrypt(n.IncomingAuthCredentials); err != nil {
		return nil, fmt.Errorf("decrypt cached incoming auth: %w", err)
	}
	return &n, nil
}

func (c *NodeCacheRedis) Set(ctx context.Context, teamSlug string, n *domain.Node, ttl time.Duration) error {
	cp := *n // копия: наружу отдаём Node с plaintext-кредами
	var err error
	if cp.AuthCredentials, err = c.cipher.Encrypt(cp.AuthCredentials); err != nil {
		return fmt.Errorf("encrypt auth for cache: %w", err)
	}
	if cp.IncomingAuthCredentials, err = c.cipher.Encrypt(cp.IncomingAuthCredentials); err != nil {
		return fmt.Errorf("encrypt incoming auth for cache: %w", err)
	}
	data, err := json.Marshal(&cp)
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	key := nodeKey(teamSlug, cp.Path)
	if err := c.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %s: %w", key, err)
	}
	return nil
}

func (c *NodeCacheRedis) InvalidateByPath(ctx context.Context, teamSlug, path string) error {
	key := nodeKey(teamSlug, path)
	if err := c.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del %s: %w", key, err)
	}
	return nil
}
