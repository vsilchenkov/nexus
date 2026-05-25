package domain

import "time"

// APIToken — read-only токен для интеграций (§7.14).
// Само значение токена в БД не хранится — только token_hash (SHA-256).
type APIToken struct {
	ID         string
	UserID     string
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
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}
