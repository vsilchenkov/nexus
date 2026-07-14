package domain

import "time"

// AuditEntry — запись журнала действий пользователя (§7.13).
//
// TeamID — UUID команды, в контексте которой выполнено действие
// (multi-tenancy v2, миграция 0008). Пустая строка = глобальное
// действие админа (например, создание новой команды до того, как
// admin вошёл в её scope).
type AuditEntry struct {
	ID         string
	UserID     string // пустая строка = system
	UserLogin  string
	TeamID     string
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
	ActionNodeMove   = "node.move"

	// §34.4: очистка/удаление сообщений async-очереди Kafka.
	ActionAsyncQueuePurge = "async_queue.purge"

	ActionAppSettingsUpdate = "app_settings.update"

	ActionCHTemplateCreate = "ch_template.create"
	ActionCHTemplateUpdate = "ch_template.update"
	ActionCHTemplateDelete = "ch_template.delete"

	ActionTeamSwitch = "team.switch"

	ActionTeamCreate       = "team.create"
	ActionTeamUpdate       = "team.update"
	ActionTeamDelete       = "team.delete"
	ActionTeamMemberAdd    = "team.member.add"
	ActionTeamMemberRemove = "team.member.remove"
	ActionTeamMemberRole   = "team.member.role"

	// Справочник заголовков (§24).
	ActionHeaderCreate = "header.create"
	ActionHeaderUpdate = "header.update"
	ActionHeaderDelete = "header.delete"

	// Справочник полей запроса (§41).
	ActionRequestFieldCreate = "request_field.create"

	// Каталог разрешённых хостов (§23).
	ActionHostCreate = "host.create"
	ActionHostUpdate = "host.update"
	ActionHostDelete = "host.delete"
	ActionHostAttach = "host.attach"
	ActionHostDetach = "host.detach"
)
