package postgres

import (
	"context"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// catalogUsage — общий запрос «узлы, использующие элемент справочника» (§104).
//
// table — таблица справочника; match — предикат «узел n ссылается на строку
// table», та же константа, из которой справочник строит usage_count (§104.2),
// поэтому число в Настройках и список в диалоге не считают разное. Оба поля —
// константы кода, не пользовательский ввод.
type catalogUsage struct {
	db       DBTX
	table    string
	match    string
	notFound error
}

// catalogNodesSQL — один оператор на ответ (§104.4): существование элемента,
// total, visible и строки списка берутся из одного снимка, поэтому
// hidden = total − visible согласован со списком.
//
// $1 — id элемента, $2 — uuid[] команд скоупа, $3 — limit.
// Нет элемента → ноль строк. Элемент без видимых узлов → одна строка с NULL в
// колонках узла (LEFT JOIN LATERAL). Синтаксис совместим с PostgreSQL 12.
func catalogNodesSQL(table, match string) string {
	return `
SELECT s.total, s.visible,
       r.id::text, r.path, r.target_url, r.url_mode, r.root_method, r.status, r.team_id::text
FROM ` + table + `
CROSS JOIN LATERAL (
	SELECT count(*) AS total,
	       count(*) FILTER (WHERE n.team_id = ANY($2::uuid[])) AS visible
	FROM nodes n
	WHERE ` + match + `
) s
LEFT JOIN LATERAL (
	SELECT n.id, n.path, n.target_url, n.url_mode, n.root_method, n.status, n.team_id
	FROM nodes n
	WHERE ` + match + ` AND n.team_id = ANY($2::uuid[])
	ORDER BY n.path, n.id
	LIMIT $3
) r ON true
WHERE ` + table + `.id = $1::uuid
ORDER BY r.path, r.id`
}

func (c catalogUsage) nodesUsing(ctx context.Context, itemID string, teamIDs []string, limit int) (port.CatalogNodesPage, error) {
	if teamIDs == nil {
		// nil ушёл бы в SQL как NULL; пустой массив нагляднее и даёт тот же
		// fail-closed: `= ANY('{}')` не совпадает ни с одной командой.
		teamIDs = []string{}
	}
	rows, err := c.db.Query(ctx, catalogNodesSQL(c.table, c.match), itemID, teamIDs, max(limit, 0))
	if err != nil {
		return port.CatalogNodesPage{}, c.wrap(err)
	}
	defer rows.Close()

	page := port.CatalogNodesPage{Items: []port.CatalogNodeRef{}}
	found := false
	for rows.Next() {
		found = true
		ref, err := scanCatalogNodeRow(rows, &page)
		if err != nil {
			return port.CatalogNodesPage{}, fmt.Errorf("scan %s nodes: %w", c.table, err)
		}
		if ref != nil {
			page.Items = append(page.Items, *ref)
		}
	}
	if err := rows.Err(); err != nil {
		return port.CatalogNodesPage{}, c.wrap(err)
	}
	if !found {
		return port.CatalogNodesPage{}, c.notFound
	}
	return page, nil
}

// wrap: 22P02 — id из пути не UUID, для вызывающего это «не найдено», а не 500.
// Скоуп команд берётся из членств, его значения — всегда валидные UUID.
func (c catalogUsage) wrap(err error) error {
	if isInvalidUUID(err) {
		return c.notFound
	}
	return fmt.Errorf("%s nodes using: %w", c.table, err)
}

// scanCatalogNodeRow читает строку catalogNodesSQL: числа — в page, узел —
// в результат; nil — у элемента нет видимых узлов (NULL из LEFT JOIN).
func scanCatalogNodeRow(row rowScanner, page *port.CatalogNodesPage) (*port.CatalogNodeRef, error) {
	var id, path, target, urlMode, rootMethod, status, teamID *string
	if err := row.Scan(&page.Total, &page.Visible,
		&id, &path, &target, &urlMode, &rootMethod, &status, &teamID); err != nil {
		return nil, err
	}
	if id == nil {
		return nil, nil
	}
	return &port.CatalogNodeRef{
		ID:         *id,
		Path:       derefStr(path),
		TargetURL:  derefStr(target),
		URLMode:    domain.URLMode(derefStr(urlMode)),
		RootMethod: domain.RootMethod(derefStr(rootMethod)),
		Status:     domain.NodeStatus(derefStr(status)),
		TeamID:     derefStr(teamID),
	}, nil
}
