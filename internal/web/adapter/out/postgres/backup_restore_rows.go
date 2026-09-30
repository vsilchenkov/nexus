package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain/backup"
	"nexus/internal/web/usecase/port"
)

// Явные INSERT с id из файла: Team-, User- и APIToken-репозитории привязаны к
// пулу и id выдаёт база, а восстановлению нужны id источника (на них
// ссылаются остальные строки файла). Совпавшие с целью строки usecase в набор
// не кладёт, поэтому ON CONFLICT DO NOTHING стоит только там, где конфликт —
// штатная ситуация (членство admin в Default, справочники по имени).

// restoreIdentity — команды, пользователи, членство.
func restoreIdentity(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	for _, t := range set.TeamUpdates {
		if _, err := tx.Exec(ctx, `UPDATE teams SET name = $2, external_url = $3, ch_database = $4, updated_at = now()
WHERE id = $1::uuid`, t.ID, t.Name, t.ExternalURL, t.CHDatabase); err != nil {
			return fmt.Errorf("backup restore team %s: %w", t.Slug, err)
		}
	}
	for _, t := range set.Teams {
		if _, err := tx.Exec(ctx, `INSERT INTO teams (id, slug, name, ch_database, external_url, created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			t.ID, t.Slug, t.Name, t.CHDatabase, t.ExternalURL, t.CreatedAt, t.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore team %s: %w", t.Slug, err)
		}
	}
	for _, u := range set.Users {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, login, name, email, password_hash, role, active,
	must_change_password, lang, default_team_id, created_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11)`,
			u.ID, u.Login, u.Name, u.Email, u.PasswordHash, u.Role, u.Active,
			u.MustChangePassword, u.Lang, u.DefaultTeamID, u.CreatedAt); err != nil {
			return fmt.Errorf("backup restore user %s: %w", u.Login, err)
		}
	}
	for _, m := range set.UserTeams {
		if _, err := tx.Exec(ctx, `INSERT INTO user_teams (user_id, team_id, role, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4) ON CONFLICT DO NOTHING`, m.UserID, m.TeamID, m.Role, m.CreatedAt); err != nil {
			return fmt.Errorf("backup restore membership %s/%s: %w", m.UserID, m.TeamID, err)
		}
	}
	return nil
}

// restoreAccess — избранное, личные настройки, API-токены.
func restoreAccess(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	for _, f := range set.Favorites {
		if _, err := tx.Exec(ctx, `INSERT INTO user_team_favorites (user_id, team_id, position, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4) ON CONFLICT DO NOTHING`, f.UserID, f.TeamID, f.Position, f.CreatedAt); err != nil {
			return fmt.Errorf("backup restore favorite %s/%s: %w", f.UserID, f.TeamID, err)
		}
	}
	for _, p := range set.Preferences {
		if _, err := tx.Exec(ctx, `INSERT INTO user_preferences (user_id, team_id, key, value, updated_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5) ON CONFLICT DO NOTHING`, p.UserID, p.TeamID, p.Key, p.Value, p.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore preference %s/%s: %w", p.UserID, p.Key, err)
		}
	}
	for _, t := range set.APITokens {
		if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (id, user_id, team_id, name, token_hash, prefix, scopes,
	created_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)`,
			t.ID, t.UserID, t.TeamID, t.Name, t.TokenHash, t.Prefix, t.Scopes, t.CreatedAt, t.ExpiresAt); err != nil {
			return fmt.Errorf("backup restore api token %s: %w", t.Prefix, err)
		}
	}
	return nil
}

// restoreTemplates — шаблоны ClickHouse. Все пишутся без признака «по
// умолчанию», затем он переносится двумя UPDATE: частичный уникальный индекс
// ch_templates_one_default проверяется построчно, и один UPDATE, снимающий
// флаг с одной строки и ставящий другой, упал бы на промежуточном состоянии.
func restoreTemplates(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	for _, t := range set.CHTemplates {
		if _, err := tx.Exec(ctx, `INSERT INTO ch_templates (id, name, description, spec, is_default, created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, false, $5, $6)
ON CONFLICT (id) DO UPDATE SET description = EXCLUDED.description, spec = EXCLUDED.spec, updated_at = now()`,
			t.ID, t.Name, t.Description, t.Spec, t.CreatedAt, t.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore ch template %s: %w", t.Name, err)
		}
	}
	if set.DefaultTemplateID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ch_templates SET is_default = false WHERE is_default AND id <> $1::uuid`,
		set.DefaultTemplateID); err != nil {
		return fmt.Errorf("backup restore ch template default reset: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ch_templates SET is_default = true WHERE id = $1::uuid`,
		set.DefaultTemplateID); err != nil {
		return fmt.Errorf("backup restore ch template default: %w", err)
	}
	return nil
}

// restoreCatalogs — группы, хосты, справочники, маски, реестр инстансов.
func restoreCatalogs(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	for _, g := range set.NodeGroups {
		if _, err := tx.Exec(ctx, `INSERT INTO node_groups (id, name, description, sort_order, created_by, updated_by,
	created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)`,
			g.ID, g.Name, g.Description, g.SortOrder, g.CreatedBy, g.UpdatedBy, g.CreatedAt, g.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore node group %s: %w", g.Name, err)
		}
	}
	for _, h := range set.Hosts {
		if _, err := tx.Exec(ctx, `INSERT INTO host_allowlist (id, pattern, kind, description, created_by, created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			h.ID, h.Pattern, h.Kind, h.Description, h.CreatedBy, h.CreatedAt, h.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore host %s: %w", h.Pattern, err)
		}
	}
	if err := restoreCatalogEntries(ctx, tx, "headers_catalog", set.Headers); err != nil {
		return err
	}
	if err := restoreCatalogEntries(ctx, tx, "request_fields_catalog", set.RequestFields); err != nil {
		return err
	}
	if err := restoreLogMasks(ctx, tx, set); err != nil {
		return err
	}
	return restorePeers(ctx, tx, set)
}

// restoreCatalogEntries — справочник заголовков или полей запроса. Имя
// таблицы — из фиксированного набора вызывающего. Совпадение по имени без
// учёта регистра — штатное (строка цели остаётся).
func restoreCatalogEntries(ctx context.Context, tx pgx.Tx, table string, rows []backup.CatalogEntry) error {
	for _, e := range rows {
		if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (id, name, description, created_by, created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
			e.ID, e.Name, e.Description, e.CreatedBy, e.CreatedAt, e.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore %s %s: %w", table, e.Name, err)
		}
	}
	return nil
}

// restoreLogMasks — набор масок заменяется целиком (§110.6).
func restoreLogMasks(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	if !set.ReplaceLogMasks {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM log_mask_patterns`); err != nil {
		return fmt.Errorf("backup restore log masks reset: %w", err)
	}
	for _, m := range set.LogMasks {
		if _, err := tx.Exec(ctx, `INSERT INTO log_mask_patterns (id, pattern, replacement, description, enabled,
	sort_order, created_by, updated_by, created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			m.ID, m.Pattern, m.Replacement, m.Description, m.Enabled, m.SortOrder,
			m.CreatedBy, m.UpdatedBy, m.CreatedAt, m.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore log mask %s: %w", m.ID, err)
		}
	}
	return nil
}

// restorePeers — реестр инстансов §73 без кеша проб (его заполнит первая
// проверка).
func restorePeers(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	for _, p := range set.PeerInstances {
		if _, err := tx.Exec(ctx, `INSERT INTO peer_instances (id, title, base_url, comment, created_by, updated_by,
	created_at, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
			p.ID, p.Title, p.BaseURL, p.Comment, p.CreatedBy, p.UpdatedBy, p.CreatedAt, p.UpdatedAt); err != nil {
			return fmt.Errorf("backup restore peer instance %s: %w", p.ID, err)
		}
	}
	return nil
}
