package domain

import "time"

// AuditEntry — запись журнала действий пользователя (§7.13).
type AuditEntry struct {
	ID         string
	UserID     string // пустая строка = system
	UserLogin  string
	Action     string // машинно-читаемый код: node.create, user.login.success, ...
	TargetType string
	TargetID   string
	Details    map[string]any
	IPAddress  string
	CreatedAt  time.Time
}

// Стандартные action-коды (§7.13). Список будет расти по мере добавления
// новых mutating-операций. Сейчас покрываем CRUD узлов в Phase 2.
const (
	ActionNodeCreate = "node.create"
	ActionNodeUpdate = "node.update"
	ActionNodeDelete = "node.delete"

	ActionUserLogin       = "user.login.success"
	ActionUserLoginFailed = "user.login.failed"
	ActionUserLogout      = "user.logout"
	ActionUserCreate      = "user.create"
	ActionUserUpdate      = "user.update"
	ActionUserDelete      = "user.delete"
	ActionUserPassword    = "user.password.change"

	ActionAPITokenCreate = "api_token.create"
	ActionAPITokenRevoke = "api_token.revoke"
	ActionAPITokenDelete = "api_token.delete"

	ActionNodeReplay = "node.replay"
	ActionNodeDryRun = "node.dry_run"

	ActionAppSettingsUpdate = "app_settings.update"
)
