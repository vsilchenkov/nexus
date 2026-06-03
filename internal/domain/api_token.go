package domain

import (
	"slices"
	"time"
)

// APIToken — read-only токен для интеграций (§7.14).
// Само значение токена в БД не хранится — только token_hash (SHA-256).
//
// TeamID — multi-tenancy v2 (миграция 0008). Токен ограничен одной
// командой: Receiver/Web используют его как scope (current_team_id
// сессии = token.TeamID).
type APIToken struct {
	ID         string
	UserID     string
	TeamID     string
	Name       string
	TokenHash  string
	Prefix     string
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// Доступные scope-коды (§7.14).
const (
	ScopeLogsRead    = "logs:read"
	ScopeNodesRead   = "nodes:read"
	ScopeMetricsRead = "metrics:read"
	ScopeAuditRead   = "audit:read"
)

// IsActive — токен валиден для использования.
func (t *APIToken) IsActive(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

// HasScope — true, если у токена есть указанный scope.
func (t *APIToken) HasScope(scope string) bool {
	return slices.Contains(t.Scopes, scope)
}
