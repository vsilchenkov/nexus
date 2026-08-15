package domain

import "time"

// TokenPurpose — назначение одноразовой ссылки (§88.4.1).
//
// Значения обязаны совпадать со списком в CHECK-ограничении миграции
// 0036_one_time_tokens: новое назначение добавляется и туда, и сюда, иначе
// вставка упадёт на ограничении.
type TokenPurpose string

const (
	// TokenPurposePasswordReset — ссылка «задать новый пароль» (§88).
	TokenPurposePasswordReset TokenPurpose = "password_reset"
)

// OneTimeToken — выданная одноразовая ссылка.
//
// В хранилище лежит только TokenHash: сам токен существует ровно один раз — в
// письме получателя. Одноразовость обеспечивается полем UsedAt через атомарный
// UPDATE, а не удалением строки: так «истекла» и «уже использована» остаются
// различимыми.
type OneTimeToken struct {
	ID        string
	UserID    string
	Purpose   TokenPurpose
	TokenHash string
	// Payload — данные назначения. У password_reset пуст; email_change кладёт
	// сюда новый адрес, user_invite — роль и команду.
	Payload   map[string]any
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	RequestIP string
}

// Active — ссылка ещё действует на момент at: не погашена и не истекла.
// Тот же предикат, что в SQL-условии Consume, — держим их рядом, чтобы
// расхождение было заметно при чтении.
func (t OneTimeToken) Active(at time.Time) bool {
	return t.UsedAt == nil && t.ExpiresAt.After(at)
}
