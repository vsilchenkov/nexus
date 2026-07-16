// Package sentry — инициализация Sentry SDK с маскированием чувствительных полей
// (§14.2 ТЗ). Использует BeforeSend и BeforeBreadcrumb для очистки данных
// перед отправкой.
package sentry

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"

	"nexus/internal/platform/config"
	"nexus/internal/platform/sensitive"
)

// maxSentryValueBytes — кап длины ЛЮБОГО строкового значения, уходящего в Sentry
// (§42). Защищает событие от раздувания большим телом запроса/ответа, если оно
// вдруг просочилось в текст ошибки / exception value / extra / breadcrumb. 8 КиБ
// с запасом перекрывают нормальные сообщения, но рубят мегабайтные payload'ы
// (а 36-МиБ gRPC-тело тем более) — Sentry-события остаются лёгкими.
const maxSentryValueBytes = 8 * 1024

// sensitiveKeys — имена полей/заголовков, значения которых стираются в Sentry.
// Источник истины — platform/sensitive (§51: тот же список маскирует служебные
// логи в logsink).
var sensitiveKeys = sensitive.Keys()

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
		// §42: транзакции производительности (performance/tracing) идут ОТДЕЛЬНЫМ
		// хуком, не через BeforeSend. Без него спаны/контексты транзакции не
		// маскируются и не режутся — большое тело/секрет могли бы уехать в Sentry
		// performance. Применяем тот же scrubbing + чистку спанов.
		BeforeSendTransaction: beforeSendTransaction,
		BeforeBreadcrumb:      beforeBreadcrumb,
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
		if event.Breadcrumbs[i] == nil {
			continue
		}
		event.Breadcrumbs[i].Data = maskAny(event.Breadcrumbs[i].Data)
	}
	// §42: режем большие строки, чтобы тело запроса/ответа не уезжало в Sentry
	// целиком (через message / exception value / extra / breadcrumb).
	capEventStrings(event)
	return event
}

// beforeSendTransaction — scrubbing transaction-событий performance/tracing.
// Тот же набор, что и для ошибок (request/tags/breadcrumbs/contexts + кап
// больших строк), плюс спаны транзакции: tags маскируем, data маскируем+режем,
// description режем (§42).
func beforeSendTransaction(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	event = beforeSend(event, hint)
	if event == nil {
		return nil
	}
	for _, sp := range event.Spans {
		if sp == nil {
			continue
		}
		sp.Tags = maskMap(sp.Tags)
		sp.Data = truncateAny(maskAny(sp.Data))
		sp.Description = truncateValue(sp.Description)
	}
	return event
}

func beforeBreadcrumb(b *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
	if b == nil {
		return nil
	}
	b.Data = truncateAny(maskAny(b.Data))
	b.Message = truncateValue(b.Message)
	return b
}

// capEventStrings режет до maxSentryValueBytes все строковые поля события, куда
// может просочиться большое тело: message, значения exception, extra, сообщения
// и data-значения breadcrumb'ов (§42).
func capEventStrings(event *sentry.Event) {
	event.Message = truncateValue(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = truncateValue(event.Exception[i].Value)
	}
	// Contexts — куда slog-интеграция кладёт структурированные поля лога (в т.ч.
	// потенциально тело). Context — алиас map[string]any.
	for name := range event.Contexts {
		event.Contexts[name] = truncateAny(event.Contexts[name])
	}
	for i := range event.Breadcrumbs {
		if event.Breadcrumbs[i] == nil {
			continue
		}
		event.Breadcrumbs[i].Message = truncateValue(event.Breadcrumbs[i].Message)
		event.Breadcrumbs[i].Data = truncateAny(event.Breadcrumbs[i].Data)
	}
}

// truncateValue обрезает строку до maxSentryValueBytes БАЙТ по границе руны
// (чтобы не порвать UTF-8) и дописывает маркер с исходным размером.
func truncateValue(s string) string {
	if len(s) <= maxSentryValueBytes {
		return s
	}
	cut := maxSentryValueBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("…(truncated, %d bytes total)", len(s))
}

// truncateAny режет строковые значения map'а до maxSentryValueBytes; нестроковые
// значения не трогает. Применяется к extra/breadcrumb-data.
func truncateAny(m map[string]any) map[string]any {
	if len(m) == 0 {
		return m
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			m[k] = truncateValue(s)
		}
	}
	return m
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
	return sensitive.IsSensitive(name)
}
