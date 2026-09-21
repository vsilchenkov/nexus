package port

import (
	"context"

	"nexus/internal/domain"
)

// CatalogNodeRef — узел, ссылающийся на элемент справочника (§104): ровно
// поля строки диалога. Кредов и прочей конфигурации узла здесь нет, поэтому
// адаптер ничего не расшифровывает.
type CatalogNodeRef struct {
	ID         string
	Path       string
	TargetURL  string
	URLMode    domain.URLMode
	RootMethod domain.RootMethod
	Status     domain.NodeStatus
	TeamID     string
}

// CatalogNodesPage — узлы, использующие один элемент справочника (§104.3).
// Все поля — из одного снимка БД.
type CatalogNodesPage struct {
	// Items — видимые узлы (команды из скоупа), не больше limit, по path, id.
	Items []CatalogNodeRef
	// Visible — сколько видимых узлов всего; больше len(Items) — список усечён.
	Visible int
	// Total — узлы всех команд тем же предикатом, что usage_count справочника.
	Total int
}

// CatalogNodeLister — узлы, использующие элемент справочника (§104.4).
//
// teamIDs — скоуп видимости (команды-членства). Пустой скоуп означает «видимых
// нет» (fail-closed): Items пуст, Visible = 0, но Total считается — он нужен
// для числа узлов в чужих командах. Несуществующий или не-UUID itemID —
// sentinel «не найдено» своего справочника (domain.ErrNodeGroupNotFound,
// ErrHeaderNotFound, ErrHostNotFound).
type CatalogNodeLister interface {
	NodesUsing(ctx context.Context, itemID string, teamIDs []string, limit int) (CatalogNodesPage, error)
}
