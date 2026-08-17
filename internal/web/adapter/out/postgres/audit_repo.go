package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

type AuditRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.AuditRepo = (*AuditRepoPg)(nil)

func NewAuditRepoPg(db DBTX, logger logging.Logger) *AuditRepoPg {
	return &AuditRepoPg{db: db, logger: logger}
}

func (r *AuditRepoPg) Write(ctx context.Context, e *domain.AuditEntry) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		details = []byte("{}")
	}
	const q = `
INSERT INTO user_audit (user_id, user_login, team_id, action, target_type, target_id, details, ip_address)
VALUES (NULLIF($1,'')::uuid, $2, NULLIF($3,'')::uuid, $4, $5, $6, $7::jsonb, NULLIF($8,'')::inet)
RETURNING id, created_at`
	err = r.db.QueryRow(ctx, q,
		e.UserID, e.UserLogin, e.TeamID, e.Action, e.TargetType, e.TargetID,
		string(details), e.IPAddress,
	).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return nil
}

// auditWhere собирает общую часть WHERE для List и Count: разъехавшиеся условия
// означали бы, что счётчик «из M» считает не то, что показано на экране.
func auditWhere(f port.AuditFilter) (string, []any) {
	q := ""
	args := []any{}
	if f.UserID != "" {
		q += fmt.Sprintf(" AND user_id = $%d::uuid", len(args)+1)
		args = append(args, f.UserID)
	}
	// §86.7: сквозной скоуп имеет приоритет над однокомандным — иначе в запрос
	// уехали бы два взаимоисключающих условия и выдача всегда была бы пустой.
	// §91.2: IncludeGlobal (admin) добавляет к скоупу записи без команды —
	// входы, неудачные логины, восстановление пароля.
	switch {
	case len(f.TeamIDs) > 0:
		q += fmt.Sprintf(" AND (team_id = ANY($%d::uuid[])", len(args)+1)
		args = append(args, f.TeamIDs)
		if f.IncludeGlobal {
			q += " OR team_id IS NULL"
		}
		q += ")"
	case f.TeamID != "":
		q += fmt.Sprintf(" AND (team_id = $%d::uuid", len(args)+1)
		args = append(args, f.TeamID)
		if f.IncludeGlobal {
			q += " OR team_id IS NULL"
		}
		q += ")"
	}
	if len(f.Actions) > 0 {
		q += fmt.Sprintf(" AND action = ANY($%d)", len(args)+1)
		args = append(args, f.Actions)
	}
	if f.TargetType != "" {
		q += fmt.Sprintf(" AND target_type = $%d", len(args)+1)
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		q += fmt.Sprintf(" AND target_id = $%d", len(args)+1)
		args = append(args, f.TargetID)
	}
	if f.From != nil {
		q += fmt.Sprintf(" AND created_at >= $%d", len(args)+1)
		args = append(args, *f.From)
	}
	if f.To != nil {
		q += fmt.Sprintf(" AND created_at <= $%d", len(args)+1)
		args = append(args, *f.To)
	}
	return q, args
}

func (r *AuditRepoPg) List(ctx context.Context, f port.AuditFilter) ([]*domain.AuditEntry, error) {
	q := `
SELECT id, COALESCE(user_id::text,''), user_login, COALESCE(team_id::text,''),
       action, target_type, target_id,
       details, COALESCE(ip_address::text,''), created_at
FROM user_audit WHERE 1=1`
	where, args := auditWhere(f)
	q += where

	// §91.1: keyset-курсор. Сравнение кортежей, а не отдельно по created_at:
	// метки не уникальны, и на границе страницы записи с одинаковым временем
	// либо дублировались бы, либо терялись.
	if f.BeforeTS != nil && f.BeforeID != "" {
		q += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d::uuid)", len(args)+1, len(args)+2)
		args = append(args, *f.BeforeTS, f.BeforeID)
	}
	// id в сортировке обязателен — он же второй компонент курсора.
	q += " ORDER BY created_at DESC, id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET $%d", len(args)+1)
		args = append(args, f.Offset)
	}

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit list: %w", err)
	}
	defer rows.Close()

	var out []*domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var detailsRaw []byte
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserLogin, &e.TeamID, &e.Action,
			&e.TargetType, &e.TargetID, &detailsRaw, &e.IPAddress, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("audit scan: %w", err)
		}
		_ = json.Unmarshal(detailsRaw, &e.Details)
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// Count — сколько записей подходит под фильтр целиком (§91.1). Limit, Offset и
// курсор намеренно игнорируются: счётчик отвечает на вопрос «из скольких», а не
// «сколько на этой странице».
func (r *AuditRepoPg) Count(ctx context.Context, f port.AuditFilter) (int, error) {
	where, args := auditWhere(f)
	var n int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM user_audit WHERE 1=1`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("audit count: %w", err)
	}
	return n, nil
}

func (r *AuditRepoPg) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM user_audit WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("audit retention delete: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
