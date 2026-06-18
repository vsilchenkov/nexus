package domain

import (
	"net/url"
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
	u, err := url.Parse(raw)
	if err != nil {
		return ErrPublicBaseURLInvalid
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
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
	Sentry        SentrySettings        `json:"sentry"`
	ClickHouse    ClickHouseSettings    `json:"clickhouse"`
	Notifications NotificationsSettings `json:"notifications"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"` // user_id, кто последним обновил
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
