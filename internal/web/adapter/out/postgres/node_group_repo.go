package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeGroupRepoPg — PG-реализация port.NodeGroupRepo (§99). usage_count
// считается коррелированным подзапросом по nodes.group_id (индекс
// nodes_group_id_idx), в таблице не хранится — см. §99.2.
type NodeGroupRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.NodeGroupRepo = (*NodeGroupRepoPg)(nil)

func NewNodeGroupRepoPg(db DBTX, logger logging.Logger) *NodeGroupRepoPg {
	return &NodeGroupRepoPg{db: db, logger: logger}
}

// groupUsageExpr — on-read usage_count: число узлов, привязанных к группе.
// Считает узлы ВСЕЙ инсталляции, включая чужие команды — справочник глобальный,
// и защита от удаления обязана видеть все ссылки, а не только видимые
// смотрящему (§99.9).
const groupUsageExpr = `(SELECT count(*) FROM nodes n WHERE n.group_id = node_groups.id)`

const groupColumns = `id, name, description, sort_order, created_by, updated_by, created_at, updated_at, ` + groupUsageExpr + ` AS usage_count`

func (r *NodeGroupRepoPg) scan(row rowScanner) (*domain.NodeGroup, error) {
	var g domain.NodeGroup
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.SortOrder,
		&g.CreatedBy, &g.UpdatedBy, &g.CreatedAt, &g.UpdatedAt, &g.UsageCount)
	if err != nil {
		// 22P02: `:id` из пути не UUID — для вызывающего это «не найдено»,
		// а не 500 с шумом в Sentry (тот же приём, что у остальных репозиториев).
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, domain.ErrNodeGroupNotFound
		}
		return nil, fmt.Errorf("scan node_groups: %w", err)
	}
	return &g, nil
}

func (r *NodeGroupRepoPg) List(ctx context.Context, q string, limit int) ([]*domain.NodeGroup, error) {
	if limit <= 0 {
		limit = 200
	}
	// Поиск ПОДСТРОКИ (%q%), а не префикса: имя группы — свободный текст из
	// нескольких слов, и «обмен» обязано находить «1С Обмен».
	rows, err := r.db.Query(ctx, `
SELECT `+groupColumns+`
FROM node_groups
WHERE ($1 = '' OR name ILIKE '%' || $1 || '%')
ORDER BY sort_order, name
LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list node_groups: %w", err)
	}
	defer rows.Close()
	var out []*domain.NodeGroup
	for rows.Next() {
		g, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *NodeGroupRepoPg) Get(ctx context.Context, id string) (*domain.NodeGroup, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+groupColumns+` FROM node_groups WHERE id = $1::uuid`, id))
}

func (r *NodeGroupRepoPg) GetByName(ctx context.Context, name string) (*domain.NodeGroup, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+groupColumns+` FROM node_groups WHERE lower(name) = lower($1)`, name))
}

func (r *NodeGroupRepoPg) Create(ctx context.Context, g *domain.NodeGroup) error {
	err := r.db.QueryRow(ctx, `
INSERT INTO node_groups (name, description, sort_order, created_by, updated_by)
VALUES ($1, $2, $3, $4, $4)
RETURNING id, created_at, updated_at`,
		g.Name, g.Description, g.SortOrder, g.CreatedBy,
	).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrNodeGroupAlreadyExists
		}
		return fmt.Errorf("create node_groups: %w", err)
	}
	return nil
}

func (r *NodeGroupRepoPg) Update(ctx context.Context, g *domain.NodeGroup) error {
	tag, err := r.db.Exec(ctx, `
UPDATE node_groups
SET name = $2, description = $3, sort_order = $4, updated_by = $5, updated_at = now()
WHERE id = $1::uuid`,
		g.ID, g.Name, g.Description, g.SortOrder, g.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrNodeGroupAlreadyExists
		}
		if isInvalidUUID(err) {
			return domain.ErrNodeGroupNotFound
		}
		return fmt.Errorf("update node_groups: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNodeGroupNotFound
	}
	return nil
}

// Delete удаляет группу. На группу могут ссылаться узлы (nodes.group_id,
// ON DELETE RESTRICT) — тогда PostgreSQL вернёт 23503, и это ErrNodeGroupInUse,
// а не 500: guard-проверка usecase закрывает тот же случай раньше, но между её
// чтением и удалением узел мог быть привязан из другой вкладки.
func (r *NodeGroupRepoPg) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM node_groups WHERE id = $1::uuid`, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			r.logger.Debug("node_groups: delete rejected by FK (group is used by nodes)",
				r.logger.Str("group_id", id))
			return domain.ErrNodeGroupInUse
		}
		if isInvalidUUID(err) {
			return domain.ErrNodeGroupNotFound
		}
		return fmt.Errorf("delete node_groups: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNodeGroupNotFound
	}
	return nil
}

// Reorder присваивает sort_order по позиции в orderedIDs: (позиция+1)*10.
//
// Один UPDATE ... FROM (VALUES …) вместо N запросов — не только ради скорости:
// частично применённый порядок оставил бы справочник в состоянии, которого
// оператор не заказывал. Шкала разрежённая, чтобы между группами можно было
// вставить новую вручную.
//
// Идентификаторы приводятся к uuid явным cast'ом: в VALUES они приезжают
// текстом, и без ::uuid PostgreSQL не сравнит их с колонкой id.
func (r *NodeGroupRepoPg) Reorder(ctx context.Context, orderedIDs []string, updatedBy string) error {
	if len(orderedIDs) == 0 {
		return nil
	}
	var sb strings.Builder
	args := make([]any, 0, len(orderedIDs)+1)
	args = append(args, updatedBy)
	sb.WriteString(`UPDATE node_groups g SET sort_order = v.ord, updated_by = $1, updated_at = now()
FROM (VALUES `)
	for i, id := range orderedIDs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "($%d::uuid, %d)", i+2, (i+1)*10)
		args = append(args, id)
	}
	sb.WriteString(`) AS v(id, ord) WHERE g.id = v.id`)

	if _, err := r.db.Exec(ctx, sb.String(), args...); err != nil {
		if isInvalidUUID(err) {
			return domain.ErrNodeGroupNotFound
		}
		return fmt.Errorf("reorder node_groups: %w", err)
	}
	return nil
}
