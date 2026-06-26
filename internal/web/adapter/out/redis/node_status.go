package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	"nexus/internal/web/usecase/port"
)

// NodeStatusReaderRedis реализует port.NodeStatusReader (§46): читает исход
// последнего исходящего вызова узла из Redis (ключ nexus:node:last_error:<path>,
// пишется Sender'ом — internal/platform/nodestatus). В отличие от Prometheus-
// гауджа, переживает рестарт Sender/Web → статус «Down» на дашборде корректен
// после деплоя.
type NodeStatusReaderRedis struct {
	client *goredis.Client
	logger logging.Logger
}

var _ port.NodeStatusReader = (*NodeStatusReaderRedis)(nil)

func NewNodeStatusReaderRedis(client *goredis.Client, logger logging.Logger) *NodeStatusReaderRedis {
	return &NodeStatusReaderRedis{client: client, logger: logger}
}

// GetLastErrors одним MGET читает исход последнего вызова для всех путей. Значение
// "1" → последний вызов ошибочный, "0" → успех. Узлы без ключа (nil) в карту НЕ
// кладём — вызывающий (applyLastErrors) добивает их из Prometheus-fallback'а.
func (r *NodeStatusReaderRedis) GetLastErrors(ctx context.Context, nodePaths []string) (map[string]bool, error) {
	if len(nodePaths) == 0 {
		return map[string]bool{}, nil
	}
	keys := make([]string, len(nodePaths))
	for i, p := range nodePaths {
		keys[i] = nodestatus.Key(p)
	}
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("nodestatus mget: %w", err)
	}
	out := make(map[string]bool, len(nodePaths))
	for i, v := range vals {
		s, ok := v.(string) // nil = ключа нет → узел не трогаем (fallback на Prometheus)
		if !ok {
			continue
		}
		out[nodePaths[i]] = s == "1"
	}
	return out, nil
}
