package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
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
INSERT INTO user_audit (user_id, user_login, action, target_type, target_id, details, ip_address)
VALUES (NULLIF($1,'')::uuid, $2, $3, $4, $5, $6::jsonb, NULLIF($7,'')::inet)
RETURNING id, created_at`
	err = r.db.QueryRow(ctx, q,
		e.UserID, e.UserLogin, e.Action, e.TargetType, e.TargetID,
		string(details), e.IPAddress,
	).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return nil
}

func (r *AuditRepoPg) List(ctx context.Context, f port.AuditFilter) ([]*domain.AuditEntry, error) {
	q := `
SELECT id, COALESCE(user_id::text,''), user_login, action, target_type, target_id,
       details, COALESCE(ip_address::text,''), created_at
FROM user_audit WHERE 1=1`
	args := []any{}
	if f.UserID != "" {
		q += fmt.Sprintf(" AND user_id = $%d::uuid", len(args)+1)
		args = append(args, f.UserID)
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
	q += " ORDER BY created_at DESC"
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
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserLogin, &e.Action,
			&e.TargetType, &e.TargetID, &detailsRaw, &e.IPAddress, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("audit scan: %w", err)
		}
		_ = json.Unmarshal(detailsRaw, &e.Details)
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		_ = strings.TrimSpace // зарезервировано
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *AuditRepoPg) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM user_audit WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("audit retention delete: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
