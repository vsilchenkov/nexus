package usecase

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// CatalogKind — вид справочника, чьи узлы показывает диалог Настроек (§104).
type CatalogKind string

const (
	CatalogKindGroup  CatalogKind = "group"  // группы узлов (§99)
	CatalogKindHeader CatalogKind = "header" // справочник заголовков (§24)
	CatalogKindHost   CatalogKind = "host"   // разрешённые хосты (§23)
)

// maxCatalogNodes — потолок строк списка (§104.3). Число видимых узлов сверх
// него отдаётся в Visible, диалог пишет «показаны первые N из M».
const maxCatalogNodes = 500

// ErrUnknownCatalogKind — вида нет в карте usecase: ошибка сборки, а не ввода
// пользователя (вид задаёт маршрут, а не параметр запроса).
var ErrUnknownCatalogKind = errors.New("usecase: unknown catalog kind")

// CatalogNode — строка списка: узел и его команда для бейджа.
type CatalogNode struct {
	port.CatalogNodeRef
	TeamSlug string
	TeamName string
}

// CatalogNodes — узлы, использующие элемент справочника (§104.3).
type CatalogNodes struct {
	// Items — узлы команд-членств, не больше maxCatalogNodes, по path.
	Items []CatalogNode
	// Total — узлы всех команд тем же предикатом, что usage_count.
	Total int
	// Visible — все узлы команд-членств; больше len(Items) — список усечён.
	Visible int
	// Hidden — узлы чужих команд: только число, без путей и id (§86.9).
	Hidden int
}

// CatalogUsageUsecase — список узлов, использующих элемент справочника (§104).
//
// Знает только вид справочника: предикат «узел ссылается на элемент» живёт в
// адаптере рядом со счётчиком (§104.2). Новый справочник подключается записью
// в карте listers, без правки usecase.
type CatalogUsageUsecase struct {
	listers map[CatalogKind]port.CatalogNodeLister
	teams   TeamMembershipLister
	logger  logging.Logger
}

// NewCatalogUsageUsecase копирует карту: вызывающий не может подменить lister
// после сборки.
func NewCatalogUsageUsecase(listers map[CatalogKind]port.CatalogNodeLister, teams TeamMembershipLister, logger logging.Logger) *CatalogUsageUsecase {
	return &CatalogUsageUsecase{listers: maps.Clone(listers), teams: teams, logger: logger}
}

// NodesUsing — узлы, использующие элемент itemID справочника kind, глазами
// пользователя userID.
//
// Видимость задают членства (§86.2), admin её не расширяет (§86.9). Пустые
// членства — не ошибка: список пуст, а Hidden = Total. Нет элемента —
// sentinel справочника (domain.ErrNodeGroupNotFound, ErrHeaderNotFound,
// ErrHostNotFound) без обёртки смысла, errors.Is его видит.
func (u *CatalogUsageUsecase) NodesUsing(ctx context.Context, kind CatalogKind, itemID, userID string) (CatalogNodes, error) {
	lister, ok := u.listers[kind]
	if !ok {
		u.logger.Debug("catalog usage: unknown kind", u.logger.Str("kind", string(kind)))
		return CatalogNodes{}, fmt.Errorf("catalog usage %q: %w", kind, ErrUnknownCatalogKind)
	}
	teamIDs, teamByID, err := teamScope(ctx, u.teams, userID)
	if err != nil {
		return CatalogNodes{}, fmt.Errorf("catalog usage %s: %w", kind, err)
	}
	if len(teamIDs) == 0 {
		// Запрос всё равно идёт: нужны Total и 404 для несуществующего
		// элемента. Пустой скоуп адаптер трактует как «видимых нет».
		u.logger.Debug("catalog usage: user has no memberships",
			u.logger.Str("kind", string(kind)), u.logger.Str("user_id", userID))
	}
	page, err := lister.NodesUsing(ctx, itemID, teamIDs, maxCatalogNodes)
	if err != nil {
		return CatalogNodes{}, fmt.Errorf("catalog usage %s: %w", kind, err)
	}
	out := CatalogNodes{
		Items:   make([]CatalogNode, 0, len(page.Items)),
		Total:   page.Total,
		Visible: page.Visible,
		// От Visible, а не от длины списка: при усечении разность с len(Items)
		// приписала бы свои узлы чужим командам (§104.6).
		Hidden: max(0, page.Total-page.Visible),
	}
	for _, ref := range page.Items {
		tm := teamByID[ref.TeamID] // всегда есть: адаптер фильтрует по teamIDs
		out.Items = append(out.Items, CatalogNode{CatalogNodeRef: ref, TeamSlug: tm.Slug, TeamName: tm.Name})
	}
	u.logger.Debug("catalog usage: nodes listed",
		u.logger.Str("kind", string(kind)), u.logger.Str("item_id", itemID),
		u.logger.Int("teams", len(teamIDs)), u.logger.Int("total", out.Total),
		u.logger.Int("visible", out.Visible), u.logger.Int("hidden", out.Hidden),
		u.logger.Any("truncated", out.Visible > len(out.Items)))
	return out, nil
}
