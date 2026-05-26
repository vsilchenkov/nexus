// Package port — интерфейсы зависимостей usecase-слоя Receiver Service.
package port

import (
	"context"

	"nexus/internal/domain"
)

// NodeReader — read-only доступ к конфигу узлов.
// Реализация (adapter/out/redis + adapter/out/postgres fallback) делает
// трёхуровневое чтение: in-memory LRU → Redis → PostgreSQL (§9.2 ТЗ).
type NodeReader interface {
	GetByPath(ctx context.Context, path string) (*domain.Node, error)
}
