package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// appSettingsOverlay — узкий read-only-доступ к таблице app_settings, который
// bootstrap читает один раз на старте, чтобы наложить значения из БД поверх
// YAML/env (§8.4 ТЗ). Сделан отдельной структурой, потому что
// web/usecase/AppSettingsUsecase зависит от логгера и audit'а — а bootstrap'у
// этого не нужно.
type appSettingsOverlay struct {
	Sentry struct {
		Use              *bool    `json:"use,omitempty"`
		DSN              *string  `json:"dsn,omitempty"`
		Environment      *string  `json:"environment,omitempty"`
		Level            *int     `json:"level,omitempty"`
		AttachStacktrace *bool    `json:"attach_stacktrace,omitempty"`
		EnableTracing    *bool    `json:"enable_tracing,omitempty"`
		TracesSampleRate *float64 `json:"traces_sample_rate,omitempty"`
	} `json:"sentry"`
	ClickHouse struct {
		Host             *string `json:"host,omitempty"`
		Port             *int    `json:"port,omitempty"`
		Database         *string `json:"database,omitempty"`
		User             *string `json:"user,omitempty"`
		Password         *string `json:"password,omitempty"`
		BatchSize        *int    `json:"batch_size,omitempty"`
		FlushIntervalSec *int    `json:"flush_interval_sec,omitempty"`
		BufferMaxSize    *int    `json:"buffer_max_size,omitempty"`
		Workers          *int    `json:"workers,omitempty"`
	} `json:"clickhouse"`
	// §51: runtime-уровень служебного логирования (nil = YAML-уровень).
	Logging struct {
		Level *int `json:"level,omitempty"`
	} `json:"logging"`
}

// ApplyAppSettings читает singleton-строку app_settings и накладывает
// заданные поля поверх cfg. Это позволяет оператору крутить настройки
// через UI и видеть их при следующем рестарте (hot-reload — Phase 6.3.2,
// через Redis pub/sub).
//
// Ошибка чтения логируется, но не валит сервис: app_settings —
// опциональный слой поверх обязательного env-конфига.
func ApplyAppSettings(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, logger logging.Logger) {
	o, err := readAppSettings(ctx, pool)
	if err != nil {
		logger.Warn("app_settings overlay skipped",
			logger.Err(err))
		return
	}
	overlaySentry(cfg, o)
	overlayClickHouse(cfg, o)
	logger.Info("app_settings overlay applied")
}

func readAppSettings(ctx context.Context, pool *pgxpool.Pool) (*appSettingsOverlay, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE id = 1`).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return &appSettingsOverlay{}, nil
		}
		return nil, fmt.Errorf("query app_settings: %w", err)
	}
	return decodeAppSettings(raw)
}

// decodeAppSettings разбирает JSONB-значение app_settings в overlay.
// Чистая функция — извлечена из readAppSettings ради unit-тестов без pgxpool.
func decodeAppSettings(raw []byte) (*appSettingsOverlay, error) {
	o := &appSettingsOverlay{}
	if len(raw) == 0 || string(raw) == "{}" {
		return o, nil
	}
	if err := json.Unmarshal(raw, o); err != nil {
		return nil, fmt.Errorf("decode app_settings: %w", err)
	}
	return o, nil
}

func overlaySentry(cfg *config.Config, o *appSettingsOverlay) {
	s := o.Sentry
	if s.Use != nil {
		cfg.Sentry.Use = *s.Use
	}
	if s.DSN != nil {
		cfg.Sentry.Dsn = *s.DSN
	}
	if s.Environment != nil {
		cfg.Sentry.Environment = *s.Environment
	}
	if s.Level != nil {
		cfg.Sentry.Level = *s.Level
	}
	if s.AttachStacktrace != nil {
		cfg.Sentry.AttachStacktrace = *s.AttachStacktrace
	}
	if s.EnableTracing != nil {
		cfg.Sentry.EnableTracing = *s.EnableTracing
	}
	if s.TracesSampleRate != nil {
		cfg.Sentry.TracesSampleRate = *s.TracesSampleRate
	}
}

func overlayClickHouse(cfg *config.Config, o *appSettingsOverlay) {
	c := o.ClickHouse
	if c.Host != nil {
		cfg.ClickHouse.Host = *c.Host
	}
	if c.Port != nil {
		cfg.ClickHouse.Port = *c.Port
	}
	if c.Database != nil {
		cfg.ClickHouse.Database = *c.Database
	}
	if c.User != nil {
		cfg.ClickHouse.User = *c.User
	}
	if c.Password != nil {
		cfg.ClickHouse.Password = *c.Password
	}
	if c.BatchSize != nil {
		cfg.ClickHouse.BatchSize = *c.BatchSize
	}
	if c.FlushIntervalSec != nil {
		cfg.ClickHouse.FlushIntervalSec = *c.FlushIntervalSec
	}
	if c.BufferMaxSize != nil {
		cfg.ClickHouse.BufferMaxSize = *c.BufferMaxSize
	}
	if c.Workers != nil {
		cfg.ClickHouse.Workers = *c.Workers
	}
}
