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

func ptr[T any](v T) *T { return &v }

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
			wantDSN: ptr("https://key@sentry.example.com/52"),
			wantPwd: ptr("ch-secret"),
		},
		{
			name:    "legacy plaintext проходит как есть",
			raw:     `{"sentry":{"dsn":"https://key@sentry.example.com/52"},"clickhouse":{"password":"ch-secret"}}`,
			wantDSN: ptr("https://key@sentry.example.com/52"),
			wantPwd: ptr("ch-secret"),
		},
		{
			name:    "смесь: DSN зашифрован, пароль ещё нет",
			raw:     `{"sentry":{"dsn":"` + encDSN + `"},"clickhouse":{"password":"ch-secret"}}`,
			wantDSN: ptr("https://key@sentry.example.com/52"),
			wantPwd: ptr("ch-secret"),
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
