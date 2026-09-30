package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain/backup"
)

// readCatalogs — шаблоны ClickHouse, группы, каталоги, маски, инстансы.
func readCatalogs(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	var err error
	if s.CHTemplates, err = queryRows(ctx, tx, `SELECT id, name, description, spec, is_default, created_at, updated_at
FROM ch_templates ORDER BY created_at, id`, func(row pgx.Rows, t *backup.CHTemplate) error {
		return row.Scan(&t.ID, &t.Name, &t.Description, &t.Spec, &t.IsDefault, &t.CreatedAt, &t.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot ch_templates: %w", err)
	}
	if s.NodeGroups, err = queryRows(ctx, tx, `SELECT id, name, description, sort_order, created_by, updated_by,
	created_at, updated_at
FROM node_groups ORDER BY sort_order, created_at`, func(row pgx.Rows, g *backup.NodeGroup) error {
		return row.Scan(&g.ID, &g.Name, &g.Description, &g.SortOrder, &g.CreatedBy, &g.UpdatedBy,
			&g.CreatedAt, &g.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot node_groups: %w", err)
	}
	if s.Hosts, err = queryRows(ctx, tx, `SELECT id, pattern, kind, description, created_by, created_at, updated_at
FROM host_allowlist ORDER BY created_at, id`, func(row pgx.Rows, h *backup.Host) error {
		return row.Scan(&h.ID, &h.Pattern, &h.Kind, &h.Description, &h.CreatedBy, &h.CreatedAt, &h.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot host_allowlist: %w", err)
	}
	if s.Headers, err = readCatalogEntries(ctx, tx, "headers_catalog"); err != nil {
		return err
	}
	if s.RequestFields, err = readCatalogEntries(ctx, tx, "request_fields_catalog"); err != nil {
		return err
	}
	return readMasksAndPeers(ctx, tx, s)
}

// readCatalogEntries — справочник заголовков или полей запроса. Имя таблицы —
// из фиксированного набора вызывающего, не из входа.
func readCatalogEntries(ctx context.Context, tx pgx.Tx, table string) ([]backup.CatalogEntry, error) {
	out, err := queryRows(ctx, tx, `SELECT id, name, description, created_by, created_at, updated_at
FROM `+table+` ORDER BY created_at, id`, func(row pgx.Rows, e *backup.CatalogEntry) error {
		return row.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("backup snapshot %s: %w", table, err)
	}
	return out, nil
}

func readMasksAndPeers(ctx context.Context, tx pgx.Tx, s *backup.Snapshot) error {
	var err error
	if s.LogMasks, err = queryRows(ctx, tx, `SELECT id, pattern, replacement, description, enabled, sort_order,
	created_by, updated_by, created_at, updated_at
FROM log_mask_patterns ORDER BY sort_order, created_at`, func(row pgx.Rows, m *backup.LogMask) error {
		return row.Scan(&m.ID, &m.Pattern, &m.Replacement, &m.Description, &m.Enabled, &m.SortOrder,
			&m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot log_mask_patterns: %w", err)
	}
	if s.PeerInstances, err = queryRows(ctx, tx, `SELECT id, title, base_url, comment, created_by, updated_by,
	created_at, updated_at
FROM peer_instances ORDER BY created_at, id`, func(row pgx.Rows, p *backup.PeerInstance) error {
		return row.Scan(&p.ID, &p.Title, &p.BaseURL, &p.Comment, &p.CreatedBy, &p.UpdatedBy,
			&p.CreatedAt, &p.UpdatedAt)
	}); err != nil {
		return fmt.Errorf("backup snapshot peer_instances: %w", err)
	}
	return nil
}
