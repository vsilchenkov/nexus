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
	TeamID             string
	CreatedAt          time.Time
	LastLoginAt        *time.Time
}

// Session — серверная сессия в Redis (§7.1).
type Session struct {
	Token      string
	UserID     string
	Role       UserRole
	Lang       UserLang
	CreatedAt  time.Time
	LastSeenAt time.Time
}
