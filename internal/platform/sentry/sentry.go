// Package sentry — инициализация Sentry SDK с маскированием чувствительных полей
// (§14.2 ТЗ). Использует BeforeSend и BeforeBreadcrumb для очистки данных
// перед отправкой.
package sentry

import (
	"strings"
	"time"

	"github.com/getsentry/sentry-go"

	"bus/internal/platform/config"
)

// sensitiveKeys — имена полей/заголовков, значения которых стираются в Sentry.
var sensitiveKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"api_key",
	"apikey",
	"authorization",
	"auth_credentials",
	"incoming_auth_credentials",
	"encryption_key",
	"cookie",
	"set-cookie",
	"x-api-key",
	"x-auth-token",
	"x-csrf-token",
	"client_secret",
}

// Init инициализирует Sentry SDK. Если Use=false — no-op, возвращает nil.
// Имя проекта (`server_name`) и release заполняются из buildVersion/projectName.
func Init(s *config.SentrySection, projectName, version string) error {
	if !s.Use {
		return nil
	}
	opts := sentry.ClientOptions{
		Dsn:              s.Dsn,
		Environment:      s.Environment,
		AttachStacktrace: s.AttachStacktrace,
		EnableTracing:    s.EnableTracing,
		TracesSampleRate: s.TracesSampleRate,
		Debug:            s.Debug,
		Release:          version,
		ServerName:       projectName,
		BeforeSend:       beforeSend,
		BeforeBreadcrumb: beforeBreadcrumb,
	}
	return sentry.Init(opts)
}

// Flush ждёт окончания отправки в Sentry. Вызывается в defer в main.
func Flush(timeout time.Duration) {
	sentry.Flush(timeout)
}

// Reload переинициализирует Sentry SDK новыми параметрами. Используется
// для hot-reload через Redis pub/sub (§14.5 + §8.4 ТЗ): когда оператор
// меняет настройки через UI, подписчик в каждом сервисе вызывает Reload.
//
// sentry-go internally заменяет глобальный hub при повторном sentry.Init,
// так что middleware и логгер продолжают работать без переподписки.
//
// Если новый Use=false — старый hub остаётся, но клиент станет no-op.
func Reload(s *config.SentrySection, projectName, version string) error {
	return Init(s, projectName, version)
}

func beforeSend(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	if event.Request != nil {
		event.Request.Headers = maskMap(event.Request.Headers)
		event.Request.Cookies = ""
		event.Request.Data = ""
		event.Request.QueryString = ""
	}
	event.Tags = maskMap(event.Tags)
	for i := range event.Breadcrumbs {
		event.Breadcrumbs[i].Data = maskAny(event.Breadcrumbs[i].Data)
	}
	return event
}

func beforeBreadcrumb(b *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
	if b == nil {
		return nil
	}
	b.Data = maskAny(b.Data)
	return b
}

func maskMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	for k := range m {
		if isSensitive(k) {
			m[k] = "***"
		}
	}
	return m
}

func maskAny(m map[string]any) map[string]any {
	if len(m) == 0 {
		return m
	}
	for k := range m {
		if isSensitive(k) {
			m[k] = "***"
		}
	}
	return m
}

func isSensitive(name string) bool {
	low := strings.ToLower(name)
	for _, k := range sensitiveKeys {
		if low == k || strings.Contains(low, k) {
			return true
		}
	}
	return false
}
