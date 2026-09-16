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
// nexus:node:last_error:id:<node_id>, пишется Sender'ом — internal/platform/
// nodestatus). В отличие от Prometheus-гауджа, переживает рестарт Sender/Web →
// runtime-бейдж узла на дашборде корректен после деплоя.
type NodeStatusReaderRedis struct {
	client   *goredis.Client
	migrator *nodestatus.LegacyMigrator
	logger   logging.Logger
}

var _ port.NodeStatusReader = (*NodeStatusReaderRedis)(nil)

func NewNodeStatusReaderRedis(client *goredis.Client, logger logging.Logger) *NodeStatusReaderRedis {
	return &NodeStatusReaderRedis{client: client, migrator: nodestatus.NewLegacyMigrator(client), logger: logger}
}

// GetLastOutcomes одним MGET читает исход последнего вызова для всех узлов по их id
// (кодировка — nodestatus.DecodeOutcome, вкл. legacy "1" → down). Узлы без
// ключа (nil) или с нераспознанным значением в карту НЕ кладём — вызывающий
// (applyLastOutcomes) добивает их из Prometheus-fallback'а.
func (r *NodeStatusReaderRedis) GetLastOutcomes(ctx context.Context, nodeIDs []string) (map[string]domain.NodeOutcome, error) {
	if len(nodeIDs) == 0 {
		return map[string]domain.NodeOutcome{}, nil
	}
	keys := make([]string, len(nodeIDs))
	for i, p := range nodeIDs {
		keys[i] = nodestatus.Key(p)
	}
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("nodestatus mget: %w", err)
	}
	out := make(map[string]domain.NodeOutcome, len(nodeIDs))
	for i, v := range vals {
		s, sok := v.(string) // nil = ключа нет → узел не трогаем (fallback на Prometheus)
		if !sok {
			continue
		}
		outcome, ok := nodestatus.DecodeOutcome(s)
		if !ok {
			// §51.9: тихий пропуск мусорного значения — след для расследований.
			r.logger.Debug("nodestatus: unrecognized redis value, falling back to prometheus",
				r.logger.Str("node_id", nodeIDs[i]), r.logger.Str("value", s))
			continue
		}
		out[nodeIDs[i]] = outcome
	}
	return out, nil
}

var _ port.NodeStatusResetter = (*NodeStatusReaderRedis)(nil)

// ClearLastOutcome удаляет ключ исхода последнего вызова узла (§81.4.2).
//
// Именно удаление, а не запись «ok»: после ручного сброса breaker'а исход
// последнего вызова НЕИЗВЕСТЕН, и записать успех было бы неправдой. Отсутствие
// ключа читатель уже обрабатывает штатно — GetLastOutcomes пропускает такие
// узлы, и бейдж добирается из Prometheus-fallback'а.
//
// Без этого сброс выглядел бы безрезультатным: breaker закрыт, а узел на
// дашборде остаётся «Down» до следующего фактического вызова — у sync-узла без
// трафика это может не случиться никогда (TTL ключа 30 суток).
func (r *NodeStatusReaderRedis) ClearLastOutcome(ctx context.Context, nodeID string) error {
	if err := r.client.Del(ctx, nodestatus.Key(nodeID)).Err(); err != nil {
		return fmt.Errorf("nodestatus del: %w", err)
	}
	return nil
}

var _ port.NodeStatusMigrator = (*NodeStatusReaderRedis)(nil)

// LegacyMigrated — выполнен ли перенос исходов с ключей по пути на ключи по id
// (§101.6).
func (r *NodeStatusReaderRedis) LegacyMigrated(ctx context.Context) (bool, error) {
	return r.migrator.Migrated(ctx)
}

// LegacyOutcomePaths — пути узлов, у которых остался ключ исхода прежнего формата.
func (r *NodeStatusReaderRedis) LegacyOutcomePaths(ctx context.Context) ([]string, error) {
	return r.migrator.LegacyPaths(ctx)
}

// MigrateLegacyOutcomes переносит исходы на ключи по id и ставит отметку о
// переносе (nodestatus.LegacyMigrator.Migrate).
func (r *NodeStatusReaderRedis) MigrateLegacyOutcomes(ctx context.Context, idsByPath map[string]string) (int, error) {
	return r.migrator.Migrate(ctx, idsByPath)
}
