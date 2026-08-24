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

	// Восстановление пароля по email (§88.9). Колонка action — VARCHAR(64),
	// обе строки умещаются.
	//
	// Запрос пишется на КАЖДЫЙ вызов, включая безрезультатные: наружу форма
	// отвечает одинаково (§88.4.3), и аудит — единственное место, где видно,
	// что на самом деле произошло.
	ActionUserPasswordResetRequest = "user.password_reset.request"
	ActionUserPasswordResetConfirm = "user.password_reset.confirm"

	ActionAPITokenCreate = "api_token.create"
	ActionAPITokenRevoke = "api_token.revoke"
	ActionAPITokenDelete = "api_token.delete"
	ActionAPITokenRotate = "api_token.rotate"

	ActionNodeReplay = "node.replay"
	// ActionNodeReplayPeriod — §85: массовый повтор записей журнала за период.
	// Отдельное действие, а не разновидность node.replay: фильтр и CSV-экспорт
	// журнала аудита работают по action, и «кто залил в приёмник месяц трафика»
	// по вложенному полю было бы не найти. Пишется НА КАЖДЫЙ БАТЧ — прогон
	// обрывается закрытием вкладки, и след обязан остаться от ушедшего.
	ActionNodeReplayPeriod = "node.replay_period"
	ActionNodeDryRun       = "node.dry_run"
	// §56: применение ALTER'ов синхронизации схемы CH к таблице узла.
	ActionNodeCHSchemaSync = "node.ch_schema_sync"
	ActionNodeMove         = "node.move"
	// §53: клонирование узла (отдельное от node.create действие — в журнале
	// видна провенансная связь source_node_id → новый узел).
	ActionNodeCopy = "node.copy"

	// §34.4: очистка/удаление сообщений async-очереди Kafka.
	ActionAsyncQueuePurge = "async_queue.purge"

	// §81.4.2: ручной сброс circuit breaker'а узла (снимает и персистентный
	// бейдж «Down» §52). Отдельное действие, а не разновидность purge: фильтр и
	// CSV-экспорт журнала аудита работают по action, и по вложенному полю «кто
	// снимал защиту» было бы не найти. К тому же breaker есть и у sync-узла,
	// где очереди нет вовсе.
	ActionNodeBreakerReset = "node.breaker_reset"

	ActionAppSettingsUpdate = "app_settings.update"

	// §94.6: работа с журналом отказов на входе. Оба действия меняют то, что
	// видят остальные операторы (снятая группа исчезает из выдачи), поэтому
	// подотчётны — иначе «кто убрал группу, по которой шло расследование»
	// установить нечем.
	ActionRejectedResolve = "rejected.resolve"
	ActionRejectedDelete  = "rejected.delete"
	// ActionRejectedResolveAll — массовая отметка «просмотрено» по всей области
	// видимости. Отдельное действие, а не серия rejected.resolve: одним нажатием
	// гасится весь счётчик, и в журнале это должно читаться как одно решение
	// оператора, а не как сотня отдельных.
	ActionRejectedResolveAll = "rejected.resolve_all"

	ActionCHTemplateCreate = "ch_template.create"
	ActionCHTemplateUpdate = "ch_template.update"
	ActionCHTemplateDelete = "ch_template.delete"

	// §91.2: ActionTeamSwitch («team.switch») удалён — переключение команды это
	// обычная навигация, а не подотчётное действие. Записи забивали журнал и
	// уходили со старым team_id, то есть в новой команде даже не показывались.
	// Накопленные записи вычищены миграцией 0039.

	// ActionUserFavoriteTeams — §49: пользователь заменил свой список
	// избранных команд (добавление/удаление/переупорядочивание — один PUT).
	ActionUserFavoriteTeams = "user.favorite_teams.update"

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

	// Справочник маскирования логов узлов (§95).
	ActionLogMaskCreate = "log_mask.create"
	ActionLogMaskUpdate = "log_mask.update"
	ActionLogMaskDelete = "log_mask.delete"

	// Каталог разрешённых хостов (§23).
	ActionHostCreate = "host.create"
	ActionHostUpdate = "host.update"
	ActionHostDelete = "host.delete"
	ActionHostAttach = "host.attach"
	ActionHostDetach = "host.detach"

	// Реестр соседних инстансов (§73). Пробы (проверки доступности) в журнал не
	// пишутся — это read-only операция, которую интерфейс выполняет при каждом
	// открытии вкладки; аудит забился бы шумом.
	ActionPeerInstanceCreate = "instance.create"
	ActionPeerInstanceUpdate = "instance.update"
	ActionPeerInstanceDelete = "instance.delete"
)
