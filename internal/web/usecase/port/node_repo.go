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
	TeamID string // одна команда (обычный листинг); в v1 всегда "default"
	// TeamIDs — набор команд для кросс-командного поиска (§62): при непустом
	// значении фильтр идёт по team_id = ANY(TeamIDs), а TeamID игнорируется.
	// Используется NodeUsecase.SearchAcrossTeams (поиск по всем командам
	// пользователя); обычный List передаёт только TeamID.
	TeamIDs    []string
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
	// updatedBy (§63): логин актора — снимок бампает updated_at, поэтому и автора
	// последнего изменения обновляем в лад со временем.
	UpdateAllowedHostsSnapshot(ctx context.Context, nodeID string, patterns []string, updatedBy string) error
}

// NodeTableUsage — факты о ClickHouse-таблицах логов, известные PostgreSQL:
// сколько узлов делят таблицу и какие таблицы помечены внешними (§64). На них
// стоит правило видимости записей без node_id (§61).
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

	// CountsByCHTable — карта «полное имя таблицы → число узлов на ней» по всем
	// командам. Один запрос вместо N: read-path логов спрашивает про таблицу на
	// каждый запрос метрик/журнала, а таблиц в инсталляции — десятки.
	CountsByCHTable(ctx context.Context) (map[string]int, error)

	// ExternalCHTables — множество таблиц, на которые ссылается хотя бы один узел
	// с external_table (§64). Их наполняет посторонний сервис: пустой node_id там
	// штатен, а гейт владения §70.4 неприменим — маркера `__nexus_owner` у чужой
	// БД нет и быть не может. Read-path логов различает по этому множеству
	// «чужая, потому что соседняя нода» и «чужая, потому что так задумано».
	ExternalCHTables(ctx context.Context) (map[string]struct{}, error)
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
