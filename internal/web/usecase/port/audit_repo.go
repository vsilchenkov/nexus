package port

import (
	"context"
	"time"

	"bus/internal/domain"
)

// AuditFilter — параметры фильтрации в AuditRepo.List.
type AuditFilter struct {
	UserID     string
	Actions    []string
	TargetType string
	TargetID   string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// AuditRepo — append-only журнал (§7.13). Modify / Delete операций нет
// сознательно — журнал неизменяем по контракту.
type AuditRepo interface {
	Write(ctx context.Context, e *domain.AuditEntry) error
	List(ctx context.Context, f AuditFilter) ([]*domain.AuditEntry, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error) // только для housekeeping
}
