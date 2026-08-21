package domain

import (
	"strings"
	"time"
)

// ValidatePublicBaseURL проверяет публичный адрес приложения (§28, Пункт 1):
// пустая строка допустима (= не задан), иначе — абсолютный http(s)-URL без
// пути, query и хвостового слеша (только origin: scheme://host[:port]).
func ValidatePublicBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.HasSuffix(raw, "/") {
		return ErrPublicBaseURLInvalid
	}
	u, ok := AbsoluteHTTPURL(raw)
	if !ok {
		return ErrPublicBaseURLInvalid
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ErrPublicBaseURLInvalid
	}
	return nil
}

// AppSettings — динамическая часть конфига Sentry и ClickHouse, которая
// может меняться оператором через UI и переопределяет значения из YAML/env
// при старте сервиса (§8.4, §14.5 ТЗ).
//
// Все поля — указатели: nil означает «значение не задано через UI, использовать
// из YAML/env». Это позволяет hot-reload не затирать env-настройки, если
// оператор очистил поле в UI.
type AppSettings struct {
	General       GeneralSettings       `json:"general"`
	Security      SecuritySettings      `json:"security"`
	Sentry        SentrySettings        `json:"sentry"`
	ClickHouse    ClickHouseSettings    `json:"clickhouse"`
	Notifications NotificationsSettings `json:"notifications"`
	Logging       LoggingSettings       `json:"logging"`
	Mail          MailSettings          `json:"mail"` // §88: SMTP + восстановление пароля

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"` // user_id, кто последним обновил
}

// Границы уровня логирования сервисов (§51): 2=error .. 5=debug —
// та же шкала, что logging.level в YAML.
const (
	LogLevelMin = 2
	LogLevelMax = 5
)

// LoggingSettings — runtime-настройки служебного логирования (§51).
type LoggingSettings struct {
	// Level — уровень логирования всех трёх сервисов (2=error, 3=warn,
	// 4=info, 5=debug). nil = брать из YAML (cfg.Logging.Level). Применяется
	// без рестарта через reloader.SectionLogging (LevelVar в цепочке хендлеров).
	Level *int `json:"level,omitempty"`
}

// ValidateLogLevel проверяет уровень логирования (§51): [LogLevelMin, LogLevelMax].
func ValidateLogLevel(v int) error {
	if v < LogLevelMin || v > LogLevelMax {
		return ErrLogLevelInvalid
	}
	return nil
}

// Границы длительности сессии (§34.2): 5 минут .. 30 суток.
const (
	SessionTTLMinSeconds = 300            // 5 минут
	SessionTTLMaxSeconds = 30 * 24 * 3600 // 30 суток
)

// SecuritySettings — настройки безопасности, меняемые оператором (§34.2).
type SecuritySettings struct {
	// SessionTTLSeconds — длительность пользовательской сессии в секундах.
	// nil = брать из env-конфига (cfg.Redis.SessionTTLSec). Применяется к новым
	// сессиям и sliding-Touch без рестарта (через SessionTTLProvider).
	SessionTTLSeconds *int `json:"session_ttl_seconds,omitempty"`
}

// ValidateSessionTTLSeconds проверяет длительность сессии (§34.2): значение
// должно лежать в [SessionTTLMinSeconds, SessionTTLMaxSeconds].
func ValidateSessionTTLSeconds(v int) error {
	if v < SessionTTLMinSeconds || v > SessionTTLMaxSeconds {
		return ErrSessionTTLInvalid
	}
	return nil
}

// Границы срока хранения журнала отказов (§94.5).
const (
	// RejectedRetentionDisabled — сбор выключен, накопленное удаляется.
	RejectedRetentionDisabled = 0
	// RejectedRetentionDefaultDays — значение по умолчанию, когда оператор
	// ничего не задал (настройка nil).
	RejectedRetentionDefaultDays = 30
	// RejectedRetentionMaxDays — потолок: журнал агрегированный, но растёт с
	// числом уникальных клиентов, и год хранения ему ни к чему.
	RejectedRetentionMaxDays = 365
)

// ValidateRejectedRetentionDays проверяет срок хранения журнала отказов
// (§94.5): [RejectedRetentionDisabled, RejectedRetentionMaxDays], где 0 —
// «сбор выключен», а не «хранить вечно».
func ValidateRejectedRetentionDays(v int) error {
	if v < RejectedRetentionDisabled || v > RejectedRetentionMaxDays {
		return ErrRejectedRetentionInvalid
	}
	return nil
}

// RejectedRetentionOrDefault разворачивает настройку в действующее число дней:
// nil → RejectedRetentionDefaultDays. Вынесено в домен, чтобы Web (чистка) и
// Receiver (признак «писать или нет») понимали nil одинаково.
func RejectedRetentionOrDefault(v *int) int {
	if v == nil {
		return RejectedRetentionDefaultDays
	}
	return *v
}

// GeneralSettings — общесистемные настройки приложения (§28, Пункт 1).
type GeneralSettings struct {
	// PublicBaseURL — публичный адрес, под которым опубликован Web (origin без
	// хвостового слеша, напр. https://nexus.example.com). Если задан, UI
	// формирует полный адрес узла от него вместо window.location.origin.
	// nil/"" = не задан (UI берёт origin браузера). Не секрет — Get() не маскирует.
	PublicBaseURL *string `json:"public_base_url,omitempty"`

	// VersionOverride — ручное переопределение отображаемой версии (§34.3).
	// Применяется и редактируется ТОЛЬКО при включённом web.allow_version_override
	// (dev/staging); в проде флаг выключен → значение игнорируется, а запись
	// отклоняется (ErrVersionOverrideForbidden). nil/"" = версия из git (ldflags).
	VersionOverride *string `json:"version_override,omitempty"`

	// MetricsRefetchMs — интервал автообновления метрик на дашборде и страницах
	// узлов, мс (§44.C). nil = дефолт MetricsRefetchDefaultMs. Диапазон
	// [MetricsRefetchMinMs, MetricsRefetchMaxMs]. Отдаётся всем авторизованным
	// через /api/settings/public (не секрет).
	MetricsRefetchMs *int `json:"metrics_refetch_ms,omitempty"`

	// RejectedRetentionDays — срок хранения журнала отказов на входе (§94.5),
	// в днях. nil = дефолт RejectedRetentionDefaultDays. Ноль — особое значение:
	// сбор ВЫКЛЮЧЕН и накопленное удаляется (одна ручка вместо пары
	// «тумблер + срок»). Не секрет — Get() не маскирует.
	RejectedRetentionDays *int `json:"rejected_retention_days,omitempty"`

	// MetricsApproxCounts — режим подсчёта уникальных запросов в KPI узлов и
	// счётчиках дашборда (§44-perf). nil/false = ТОЧНО (countDistinct/uniqExact,
	// дефолт); true = ПРИБЛИЗИТЕЛЬНО (uniq/uniqIf, HyperLogLog: ~3× дешевле по CPU,
	// ошибка ~0.3%). Оператор включает приблизительный режим, когда узлов/данных
	// много и точный distinct упирает ClickHouse в 100% CPU. Не секрет.
	MetricsApproxCounts *bool `json:"metrics_approx_counts,omitempty"`
}

// Интервал автообновления метрик (§44.C): дефолт 12с, диапазон 1с..120с.
const (
	MetricsRefetchDefaultMs = 12000
	MetricsRefetchMinMs     = 1000
	MetricsRefetchMaxMs     = 120000
)

// ValidateMetricsRefetchMs проверяет интервал автообновления метрик (§44.C):
// значение в [MetricsRefetchMinMs, MetricsRefetchMaxMs].
func ValidateMetricsRefetchMs(v int) error {
	if v < MetricsRefetchMinMs || v > MetricsRefetchMaxMs {
		return ErrMetricsRefetchInvalid
	}
	return nil
}

// NotificationsSettings — настройки уведомлений операторам (§20).
type NotificationsSettings struct {
	Telegram TelegramSettings `json:"telegram"`
}

// TelegramSettings — уведомления в Telegram-бот (§20.2). Все поля —
// указатели (nil = не задано). bot_token маскируется в Get(). cron —
// стандартное 5-полевое выражение (валидируется в usecase).
type TelegramSettings struct {
	Enabled  *bool   `json:"enabled,omitempty"`
	ChatID   *string `json:"chat_id,omitempty"`
	BotToken *string `json:"bot_token,omitempty"`
	Cron     *string `json:"cron,omitempty"`
}

// SentrySettings — параметры §14.4 ТЗ, доступные через Web UI.
type SentrySettings struct {
	Use              *bool    `json:"use,omitempty"`
	DSN              *string  `json:"dsn,omitempty"`
	Environment      *string  `json:"environment,omitempty"`
	Level            *int     `json:"level,omitempty"`
	AttachStacktrace *bool    `json:"attach_stacktrace,omitempty"`
	EnableTracing    *bool    `json:"enable_tracing,omitempty"`
	TracesSampleRate *float64 `json:"traces_sample_rate,omitempty"`
}

// ClickHouseSettings — параметры подключения к ClickHouse, доступные через UI.
// Адрес и креды могут меняться без рестарта (Phase 6.3.2 — hot-reload).
type ClickHouseSettings struct {
	Host             *string `json:"host,omitempty"`
	Port             *int    `json:"port,omitempty"`
	Database         *string `json:"database,omitempty"`
	User             *string `json:"user,omitempty"`
	Password         *string `json:"password,omitempty"`
	BatchSize        *int    `json:"batch_size,omitempty"`
	FlushIntervalSec *int    `json:"flush_interval_sec,omitempty"`
	BufferMaxSize    *int    `json:"buffer_max_size,omitempty"`
	Workers          *int    `json:"workers,omitempty"`
}
