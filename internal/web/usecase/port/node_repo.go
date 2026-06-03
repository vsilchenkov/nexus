// Package port — интерфейсы зависимостей usecase-слоя Web Service (§17.2 ТЗ).
//
// Adapter'ы из internal/web/adapter/out/* реализуют эти интерфейсы.
// Usecase знают только про port — никогда про конкретные реализации.
package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// ListNodesFilter — параметры фильтрации в NodeRepo.List.
type ListNodesFilter struct {
	TeamID     string // обязательное поле; в v1 всегда "default"
	Search     string // подстрока для path / target_url
	RootMethod string // "" / "request" / "requestAsync"
	Limit      int
	Offset     int
}

// NodeRepo — CRUD-репозиторий узлов (PostgreSQL).
type NodeRepo interface {
	Get(ctx context.Context, id string) (*domain.Node, error)
	GetByPath(ctx context.Context, path string) (*domain.Node, error)
	List(ctx context.Context, f ListNodesFilter) ([]*domain.Node, error)
	Count(ctx context.Context, teamID string) (int, error)
	Create(ctx context.Context, node *domain.Node) error
	Update(ctx context.Context, node *domain.Node) error
	Delete(ctx context.Context, id string) error
	// UpdateAllowedHostsSnapshot обновляет только денормализованный снимок
	// nodes.url_allowed_hosts (§23). Используется host-allowlist usecase при
	// привязке/отвязке паттернов — не трогает остальные поля узла и креды.
	UpdateAllowedHostsSnapshot(ctx context.Context, nodeID string, patterns []string) error
}

// NodeCache — кеш для node-конфигов в Redis (§9.2: write-through, cache-aside).
type NodeCache interface {
	GetByPath(ctx context.Context, path string) (*domain.Node, error)
	Set(ctx context.Context, node *domain.Node, ttl time.Duration) error
	InvalidateByPath(ctx context.Context, path string) error
}
