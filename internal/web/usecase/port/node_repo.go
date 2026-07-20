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

// NodeTableUsage — сколько узлов делят одну ClickHouse-таблицу логов.
//
// Отдельный малый порт, а не метод NodeRepo (ISP): нужен единственному
// сценарию — переносу узла между командами, — и расширение NodeRepo сломало бы
// все стабы в unit-тестах.
//
// Подсчёт идёт по ВСЕМ командам без team-scope: таблицу могут делить узлы,
// уже разъехавшиеся по разным командам, и именно этот случай проверяется.
type NodeTableUsage interface {
	// CountByCHTable возвращает число узлов с clickhouse_table = table,
	// исключая excludeNodeID (сам переносимый узел). Пустое имя таблицы → 0.
	CountByCHTable(ctx context.Context, table, excludeNodeID string) (int, error)
}

// NodeCache — кеш для node-конфигов в Redis (§9.2: write-through, cache-aside).
//
// teamSlug обязателен во всех методах (§50): ключ кеша — "node:<team_slug>:<path>",
// потому что после §18 (multi-tenancy) path уникален только внутри команды.
// Пустой teamSlug трактуется как domain.DefaultTeamSlug.
type NodeCache interface {
	GetByPath(ctx context.Context, teamSlug, path string) (*domain.Node, error)
	Set(ctx context.Context, teamSlug string, node *domain.Node, ttl time.Duration) error
	InvalidateByPath(ctx context.Context, teamSlug, path string) error
}
