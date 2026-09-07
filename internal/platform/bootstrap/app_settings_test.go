package bootstrap

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

func TestDecodeAppSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		wantErr   bool
		wantLevel *int
	}{
		{"empty raw", "", false, nil},
		{"empty object", "{}", false, nil},
		{"no logging section", `{"sentry":{"use":true}}`, false, nil},
		{"empty logging section", `{"logging":{}}`, false, nil},
		{"level null", `{"logging":{"level":null}}`, false, nil},
		{"level set", `{"logging":{"level":5}}`, false, new(5)},
		{"level with neighbours", `{"logging":{"level":2},"clickhouse":{"host":"ch"}}`, false, new(2)},
		{"malformed json", `{"logging":`, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, err := decodeAppSettings([]byte(tc.raw), nil, logging.NewNoop())
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, o)
			if tc.wantLevel == nil {
				assert.Nil(t, o.Logging.Level)
				return
			}
			require.NotNil(t, o.Logging.Level)
			assert.Equal(t, *tc.wantLevel, *o.Logging.Level)
		})
	}
}

func TestDecodeAppSettings_KeepsExistingOverlayFields(t *testing.T) {
	t.Parallel()
	raw := `{"sentry":{"use":true,"level":2},"clickhouse":{"host":"ch1","port":9000}}`
	o, err := decodeAppSettings([]byte(raw), nil, logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, o.Sentry.Use)
	assert.True(t, *o.Sentry.Use)
	require.NotNil(t, o.ClickHouse.Host)
	assert.Equal(t, "ch1", *o.ClickHouse.Host)
}

// §90.1: overlay читает секреты, которые в БД могут лежать и шифротекстом,
// и (исторически) открытым текстом.
func TestDecodeAppSettings_DecryptsSecrets(t *testing.T) {
	t.Parallel()
	c := crypto.MustNewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	encDSN, err := c.Encrypt("https://key@sentry.example.com/52")
	require.NoError(t, err)
	encPwd, err := c.Encrypt("ch-secret")
	require.NoError(t, err)

	tests := []struct {
		name    string
		raw     string
		wantDSN *string
		wantPwd *string
	}{
		{
			name:    "шифротекст расшифровывается",
			raw:     `{"sentry":{"dsn":"` + encDSN + `"},"clickhouse":{"password":"` + encPwd + `"}}`,
			wantDSN: new("https://key@sentry.example.com/52"),
			wantPwd: new("ch-secret"),
		},
		{
			name:    "legacy plaintext проходит как есть",
			raw:     `{"sentry":{"dsn":"https://key@sentry.example.com/52"},"clickhouse":{"password":"ch-secret"}}`,
			wantDSN: new("https://key@sentry.example.com/52"),
			wantPwd: new("ch-secret"),
		},
		{
			name:    "смесь: DSN зашифрован, пароль ещё нет",
			raw:     `{"sentry":{"dsn":"` + encDSN + `"},"clickhouse":{"password":"ch-secret"}}`,
			wantDSN: new("https://key@sentry.example.com/52"),
			wantPwd: new("ch-secret"),
		},
		{
			name:    "секреты не заданы",
			raw:     `{"sentry":{"use":true},"clickhouse":{"host":"ch"}}`,
			wantDSN: nil,
			wantPwd: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, err := decodeAppSettings([]byte(tc.raw), c, logging.NewNoop())
			require.NoError(t, err)
			assert.Equal(t, tc.wantDSN, o.Sentry.DSN)
			assert.Equal(t, tc.wantPwd, o.ClickHouse.Password)
		})
	}
}

// Нерасшифровываемое поле обнуляется, а сервис поднимается на env-значении:
// уронить три сервиса из-за одного битого секрета хуже, чем деградировать.
func TestDecodeAppSettings_UndecryptableSecretFallsBackToEnv(t *testing.T) {
	t.Parallel()
	keyA := crypto.MustNewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	keyB := crypto.MustNewCipher(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))

	encByA, err := keyA.Encrypt("ch-secret")
	require.NoError(t, err)
	raw := `{"clickhouse":{"password":"` + encByA + `","host":"ch1"},"logging":{"level":5}}`

	o, err := decodeAppSettings([]byte(raw), keyB, logging.NewNoop())

	require.NoError(t, err, "битый секрет не должен валить чтение overlay")
	assert.Nil(t, o.ClickHouse.Password, "поле обнуляется — overlay его не применит")
	require.NotNil(t, o.ClickHouse.Host, "остальные поля применяются как обычно")
	assert.Equal(t, "ch1", *o.ClickHouse.Host)
	require.NotNil(t, o.Logging.Level)
	assert.Equal(t, 5, *o.Logging.Level)
}

func TestApplyLogLevelFromOverlay(t *testing.T) {
	t.Parallel()
	newCtl := func(fallback int) *LogController {
		_, ctl := buildLogger(&logging.Config{Level: fallback}, &logging.SentryConfig{}, "web", "")
		return ctl
	}
	overlayWith := func(level *int) *appSettingsOverlay {
		o := &appSettingsOverlay{}
		o.Logging.Level = level
		return o
	}

	tests := []struct {
		name     string
		fallback int
		level    *int
		want     slog.Level
	}{
		{"level 2 -> error", 4, new(2), slog.LevelError},
		{"level 5 -> debug", 4, new(5), slog.LevelDebug},
		{"nil -> fallback yaml info", 4, nil, slog.LevelInfo},
		{"nil -> fallback yaml warn", 3, nil, slog.LevelWarn},
		{"invalid 6 -> fallback", 3, new(6), slog.LevelWarn},
		{"invalid 0 -> fallback", 2, new(0), slog.LevelError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctl := newCtl(tc.fallback)
			applyLogLevelFromOverlay(overlayWith(tc.level), ctl)
			assert.Equal(t, tc.want, ctl.Level.Level())
		})
	}
}

func TestApplyLogLevelFromOverlay_IdempotentAndNilSafe(t *testing.T) {
	t.Parallel()
	_, ctl := buildLogger(&logging.Config{Level: 4}, &logging.SentryConfig{}, "web", "")

	o := &appSettingsOverlay{}
	o.Logging.Level = new(2)
	applyLogLevelFromOverlay(o, ctl)
	applyLogLevelFromOverlay(o, ctl) // повторное применение — без эффекта
	assert.Equal(t, slog.LevelError, ctl.Level.Level())

	applyLogLevelFromOverlay(nil, ctl) // nil-overlay → откат на fallback
	assert.Equal(t, slog.LevelInfo, ctl.Level.Level())

	assert.NotPanics(t, func() {
		applyLogLevelFromOverlay(o, nil) // nil-контроллер — no-op
	})
}

// §97: Receiver применяет рабочие лимиты тела из app_settings, поэтому оба поля
// обязаны быть в узком overlay. Без них reloader молча ставил бы дефолт, и
// настройка из интерфейса не доезжала бы до приёма запросов.
func TestDecodeAppSettings_BodyLimits(t *testing.T) {
	t.Parallel()

	raw := `{"general":{"max_body_bytes":157286400,"max_async_body_bytes":104857600,"rejected_retention_days":7}}`
	o, err := decodeAppSettings([]byte(raw), nil, logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, o)

	require.NotNil(t, o.General.MaxBodyBytes)
	assert.Equal(t, 157286400, *o.General.MaxBodyBytes)
	require.NotNil(t, o.General.MaxAsyncBodyBytes)
	assert.Equal(t, 104857600, *o.General.MaxAsyncBodyBytes)
	require.NotNil(t, o.General.RejectedRetentionDays, "соседнее поле секции не потеряно")
	assert.Equal(t, 7, *o.General.RejectedRetentionDays)
}

// Настройка не задана — поля nil, вызывающий разворачивает их в дефолт домена.
func TestDecodeAppSettings_BodyLimitsAbsent(t *testing.T) {
	t.Parallel()

	o, err := decodeAppSettings([]byte(`{"general":{}}`), nil, logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Nil(t, o.General.MaxBodyBytes)
	assert.Nil(t, o.General.MaxAsyncBodyBytes)
}

// §98.5: глобальная политика защиты узла обязана быть видна узкому overlay.
// Без этих полей reloader молча ставил бы конфигурационные значения поверх
// заданных в интерфейсе — ровно та грабля, что проверялась в §97.
func TestDecodeAppSettings_BreakerPolicy(t *testing.T) {
	t.Parallel()

	raw := `{"general":{"circuit_breaker_threshold":2,"circuit_breaker_cooldown_sec":5,"max_body_bytes":157286400}}`
	o, err := decodeAppSettings([]byte(raw), nil, logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, o)

	require.NotNil(t, o.General.CircuitBreakerThreshold)
	assert.Equal(t, 2, *o.General.CircuitBreakerThreshold)
	require.NotNil(t, o.General.CircuitBreakerCooldownSec)
	assert.Equal(t, 5, *o.General.CircuitBreakerCooldownSec)
	require.NotNil(t, o.General.MaxBodyBytes, "соседнее поле секции не потеряно")
}

// Не задано — nil, и вызывающий разворачивает их в значения конфигурации.
func TestDecodeAppSettings_BreakerPolicyAbsent(t *testing.T) {
	t.Parallel()

	o, err := decodeAppSettings([]byte(`{"general":{}}`), nil, logging.NewNoop())
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Nil(t, o.General.CircuitBreakerThreshold)
	assert.Nil(t, o.General.CircuitBreakerCooldownSec)
}
