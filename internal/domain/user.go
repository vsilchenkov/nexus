package domain

import "time"

// User — пользователь UI (§7.1, §7.9).
// PasswordHash хранится только в БД; в Go-структуре поле остаётся, но
// устанавливается только при создании/смене пароля и никогда не
// возвращается в API-ответах.
type User struct {
	ID                 string
	Login              string
	Email              string
	PasswordHash       string
	Role               UserRole
	Active             bool
	MustChangePassword bool
	Lang               UserLang
	// DefaultTeamID — UUID команды, выставляемой текущей при логине.
	// Реальная видимость команд — через user_teams membership (multi-tenancy
	// v2, §16 ТЗ). До миграции 0008 это поле называлось TeamID и хранило
	// строку 'default'.
	DefaultTeamID string
	CreatedAt     time.Time
	LastLoginAt   *time.Time
}

// Session — серверная сессия в Redis (§7.1).
//
// CurrentTeamID — UUID команды, в контексте которой работает сессия
// (multi-tenancy v2, §16 ТЗ). Выставляется при логине из DefaultTeamID
// и меняется через POST /api/me/switch-team. Для API-токенов (которые
// привязаны к одной команде) равен api_tokens.team_id.
type Session struct {
	Token         string
	UserID        string
	Role          UserRole
	Lang          UserLang
	CurrentTeamID string
	// MustChangePassword — копия флага пользователя на момент входа (§7.1, П18).
	// Пока true, middleware RequirePasswordChanged блокирует все эндпоинты,
	// кроме смены собственного пароля. Сбрасывается при следующем входе после
	// смены пароля (ChangeOwnPassword инвалидирует все сессии пользователя).
	MustChangePassword bool
	CreatedAt          time.Time
	LastSeenAt         time.Time
}
