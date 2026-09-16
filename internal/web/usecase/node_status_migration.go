package usecase

import (
	"context"
	"fmt"
	"time"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// nodeStatusMigrationTimeout — бюджет переноса. Проход — SCAN по Redis, один
// запрос в PostgreSQL и один конвейер скриптов; минуты хватает с запасом, а
// зависший перенос не должен держать горутину до остановки Web.
const nodeStatusMigrationTimeout = time.Minute

// NodeStatusMigration — перенос исходов узлов со старых ключей Redis по пути на
// ключи по id (§101.6). Выполняется один раз, в фоне при старте Web.
//
// Переносятся только пути, однозначные во всём инстансе: исход неоднозначного
// пути — смесь узлов разных команд, и отдать его любому из них значило бы
// соврать. Такие узлы до первого вызова показывают «нет данных».
type NodeStatusMigration struct {
	store  port.NodeStatusMigrator
	nodes  port.NodePathResolver
	logger logging.Logger
}

func NewNodeStatusMigration(store port.NodeStatusMigrator, nodes port.NodePathResolver, logger logging.Logger) *NodeStatusMigration {
	return &NodeStatusMigration{store: store, nodes: nodes, logger: logger}
}

// Run выполняет перенос, если он ещё не выполнен.
//
// Ошибка для Web не фатальна, её логирует вызывающий: без переноса бейджи
// простаивающих узлов пусты до первого вызова. Отметка о переносе при ошибке не
// ставится, и следующий старт попробует снова.
func (m *NodeStatusMigration) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nodeStatusMigrationTimeout)
	defer cancel()

	done, err := m.store.LegacyMigrated(ctx)
	if err != nil {
		return fmt.Errorf("node status migration: %w", err)
	}
	if done {
		m.logger.Debug("node status migration: already done, skipped")
		return nil
	}

	paths, err := m.store.LegacyOutcomePaths(ctx)
	if err != nil {
		return fmt.Errorf("node status migration: %w", err)
	}
	ids := map[string]string{}
	if len(paths) > 0 {
		if ids, err = m.nodes.IDsByPaths(ctx, paths); err != nil {
			return fmt.Errorf("node status migration: resolve paths: %w", err)
		}
	}

	moved, err := m.store.MigrateLegacyOutcomes(ctx, ids)
	if err != nil {
		return fmt.Errorf("node status migration: %w", err)
	}
	// Info, а не debug: событие случается один раз на инсталляцию, и после выката
	// оператор проверяет по нему, что бейджи простаивающих узлов не потерялись.
	m.logger.Info("node status: legacy outcome keys migrated to node id keys",
		m.logger.Int("legacy_paths", len(paths)),
		m.logger.Int("unique_paths", len(ids)),
		m.logger.Int("moved", moved))
	return nil
}
