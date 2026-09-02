package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
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
	// §94.5: срок хранения журнала отказов (0 — сбор выключается) и §97:
	// рабочие лимиты размера тела. Из всей секции general не-Web-сервисам
	// нужны только эти поля.
	General struct {
		RejectedRetentionDays *int `json:"rejected_retention_days,omitempty"`
		MaxBodyBytes          *int `json:"max_body_bytes,omitempty"`
		MaxAsyncBodyBytes     *int `json:"max_async_body_bytes,omitempty"`
	} `json:"general"`
}

// ApplyAppSettings читает singleton-строку app_settings и накладывает
// заданные поля поверх cfg. Это позволяет оператору крутить настройки
// через UI и видеть их при следующем рестарте (hot-reload — Phase 6.3.2,
// через Redis pub/sub).
//
// Ошибка чтения логируется, но не валит сервис: app_settings —
// опциональный слой поверх обязательного env-конфига.
func ApplyAppSettings(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, cipher *crypto.Cipher, logger logging.Logger) {
	o, err := readAppSettings(ctx, pool, cipher, logger)
	if err != nil {
		logger.Warn("app_settings overlay skipped",
			logger.Err(err))
		return
	}
	overlaySentry(cfg, o)
	overlayClickHouse(cfg, o)
	logger.Info("app_settings overlay applied")
}

func readAppSettings(ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) (*appSettingsOverlay, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE id = 1`).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return &appSettingsOverlay{}, nil
		}
		return nil, fmt.Errorf("query app_settings: %w", err)
	}
	return decodeAppSettings(raw, cipher, logger)
}

// decodeAppSettings разбирает JSONB-значение app_settings в overlay и
// расшифровывает секреты (§90.1).
// Чистая функция — извлечена из readAppSettings ради unit-тестов без pgxpool.
func decodeAppSettings(raw []byte, cipher *crypto.Cipher, logger logging.Logger) (*appSettingsOverlay, error) {
	o := &appSettingsOverlay{}
	if len(raw) == 0 || string(raw) == "{}" {
		return o, nil
	}
	if err := json.Unmarshal(raw, o); err != nil {
		return nil, fmt.Errorf("decode app_settings: %w", err)
	}
	decryptOverlaySecrets(o, cipher, logger)
	return o, nil
}

// decryptOverlaySecrets расшифровывает секреты overlay'я (§90.1). Секретов в
// нём два: DSN Sentry и пароль ClickHouse.
//
// Нерасшифровываемое поле ЗАНУЛЯЕТСЯ, а сервис продолжает подниматься на
// значении из env/YAML. Это осознанно мягче, чем в web-репозитории (там
// ошибка): overlay — опциональный слой поверх обязательного env-конфига
// (см. контракт ApplyAppSettings), и ронять Receiver с Sender'ом из-за одного
// битого поля значило бы устроить простой там, где рабочее значение чаще
// всего уже лежит в окружении. Молчаливой деградации при этом нет: на каждое
// поле пишется Error, а последствие видно и само (CH не подключается,
// Sentry молчит).
func decryptOverlaySecrets(o *appSettingsOverlay, cipher *crypto.Cipher, logger logging.Logger) {
	if cipher == nil {
		return
	}
	fields := []struct {
		name string
		ptr  **string
	}{
		{"sentry.dsn", &o.Sentry.DSN},
		{"clickhouse.password", &o.ClickHouse.Password},
	}
	for _, f := range fields {
		if *f.ptr == nil {
			continue
		}
		plain, wasEncrypted, err := cipher.DecryptLenient(**f.ptr)
		if err != nil {
			// Имя поля обязательно: без него по логу не понять, что именно
			// деградировало — Sentry или подключение к ClickHouse.
			logger.Error("app_settings secret decrypt failed, falling back to env",
				logger.Str("field", f.name), logger.Err(err))
			*f.ptr = nil
			continue
		}
		if !wasEncrypted {
			// §90.1: значение ещё не «дозрело» — лежит открытым текстом и
			// зашифруется при следующем сохранении настроек или ротации ключа.
			logger.Debug("app_settings: plaintext secret read as is",
				logger.Str("field", f.name))
			continue
		}
		*f.ptr = &plain
	}
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
