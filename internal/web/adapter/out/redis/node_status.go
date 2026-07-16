package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	"nexus/internal/web/usecase/port"
)

// NodeStatusReaderRedis реализует port.NodeStatusReader (§46, §52): читает
// исход последнего исходящего вызова узла из Redis (ключ
// nexus:node:last_error:<path>, пишется Sender'ом — internal/platform/
// nodestatus). В отличие от Prometheus-гауджа, переживает рестарт Sender/Web →
// runtime-бейдж узла на дашборде корректен после деплоя.
type NodeStatusReaderRedis struct {
	client *goredis.Client
	logger logging.Logger
}

var _ port.NodeStatusReader = (*NodeStatusReaderRedis)(nil)

func NewNodeStatusReaderRedis(client *goredis.Client, logger logging.Logger) *NodeStatusReaderRedis {
	return &NodeStatusReaderRedis{client: client, logger: logger}
}

// GetLastOutcomes одним MGET читает исход последнего вызова для всех путей
// (кодировка — nodestatus.DecodeOutcome, вкл. legacy "1" → down). Узлы без
// ключа (nil) или с нераспознанным значением в карту НЕ кладём — вызывающий
// (applyLastOutcomes) добивает их из Prometheus-fallback'а.
func (r *NodeStatusReaderRedis) GetLastOutcomes(ctx context.Context, nodePaths []string) (map[string]domain.NodeOutcome, error) {
	if len(nodePaths) == 0 {
		return map[string]domain.NodeOutcome{}, nil
	}
	keys := make([]string, len(nodePaths))
	for i, p := range nodePaths {
		keys[i] = nodestatus.Key(p)
	}
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("nodestatus mget: %w", err)
	}
	out := make(map[string]domain.NodeOutcome, len(nodePaths))
	for i, v := range vals {
		s, sok := v.(string) // nil = ключа нет → узел не трогаем (fallback на Prometheus)
		if !sok {
			continue
		}
		outcome, ok := nodestatus.DecodeOutcome(s)
		if !ok {
			// §51.9: тихий пропуск мусорного значения — след для расследований.
			r.logger.Debug("nodestatus: unrecognized redis value, falling back to prometheus",
				r.logger.Str("node", nodePaths[i]), r.logger.Str("value", s))
			continue
		}
		out[nodePaths[i]] = outcome
	}
	return out, nil
}
