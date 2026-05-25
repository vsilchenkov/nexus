package domain

import "time"

// AppSettings — динамическая часть конфига Sentry и ClickHouse, которая
// может меняться оператором через UI и переопределяет значения из YAML/env
// при старте сервиса (§8.4, §14.5 ТЗ).
//
// Все поля — указатели: nil означает «значение не задано через UI, использовать
// из YAML/env». Это позволяет hot-reload не затирать env-настройки, если
// оператор очистил поле в UI.
type AppSettings struct {
	Sentry     SentrySettings     `json:"sentry"`
	ClickHouse ClickHouseSettings `json:"clickhouse"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"` // user_id, кто последним обновил
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
