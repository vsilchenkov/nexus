// Package sensitive — единый список имён чувствительных полей/заголовков и
// проверка принадлежности к нему (§14.2, §51 ТЗ). Используется маскированием
// в Sentry (platform/sentry) и в консоли служебных логов (platform/logsink):
// значения таких полей заменяются на "***" до отправки наружу.
package sensitive

import "strings"

// keys — имена полей/заголовков, значения которых маскируются. Сравнение —
// без учёта регистра, по точному совпадению или вхождению подстроки.
var keys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"api_key",
	"apikey",
	"authorization",
	"auth_credentials",
	"incoming_auth_credentials",
	"rmq_password",
	"encryption_key",
	// §90.1: DSN Sentry несёт внутри себя ключ проекта
	// (https://<key>@host/<project>), то есть является секретом целиком.
	// Подстрочное сравнение накрывает и производные имена вроде `sentry_dsn`.
	"dsn",
	"cookie",
	"set-cookie",
	"x-api-key",
	"x-auth-token",
	"x-csrf-token",
	"client_secret",
}

// Keys возвращает копию списка чувствительных имён (для тестов/диагностики).
func Keys() []string {
	out := make([]string, len(keys))
	copy(out, keys)
	return out
}

// IsSensitive сообщает, является ли имя поля/заголовка чувствительным:
// имя приводится к нижнему регистру и сравнивается с каждым ключом на
// равенство или вхождение подстроки ("X-Api-Key", "user_password" → true).
func IsSensitive(name string) bool {
	low := strings.ToLower(name)
	for _, k := range keys {
		if low == k || strings.Contains(low, k) {
			return true
		}
	}
	return false
}
