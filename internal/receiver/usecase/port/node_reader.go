// Package port — интерфейсы зависимостей usecase-слоя Receiver Service.
package port

import (
	"context"

	"nexus/internal/domain"
)

// NodeReader — read-only доступ к конфигу узлов.
// Реализация (adapter/out/redis + adapter/out/postgres fallback) делает
// трёхуровневое чтение: in-memory LRU → Redis → PostgreSQL (§9.2 ТЗ).
//
// Get принимает пару (teamSlug, path), потому что после Phase 10.1 пара
// UNIQUE(team_id, path) — две команды могут иметь одинаковый path.
// teamSlug="" эквивалентен domain.DefaultTeamSlug (legacy URL без
// /<team_slug>/ + unit-тесты).
type NodeReader interface {
	Get(ctx context.Context, teamSlug, path string) (*domain.Node, error)
}
