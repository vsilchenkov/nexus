package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePublicBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty ok", "", false},
		{"https origin", "https://nexus.example.com", false},
		{"http origin with port", "http://localhost:8000", false},
		{"trailing slash", "https://nexus.example.com/", true},
		{"with path", "https://nexus.example.com/api", true},
		{"with query", "https://nexus.example.com?x=1", true},
		{"no scheme", "nexus.example.com", true},
		{"ftp scheme", "ftp://nexus.example.com", true},
		{"scheme only", "https://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePublicBaseURL(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrPublicBaseURLInvalid)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateLogLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      int
		wantErr bool
	}{
		{"error min", LogLevelMin, false},
		{"warn", 3, false},
		{"info", 4, false},
		{"debug max", LogLevelMax, false},
		{"below min", 1, true},
		{"above max", 6, true},
		{"zero", 0, true},
		{"negative", -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateLogLevel(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrLogLevelInvalid)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateSessionTTLSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      int
		wantErr bool
	}{
		{"below min", SessionTTLMinSeconds - 1, true},
		{"at min", SessionTTLMinSeconds, false},
		{"middle", 86400, false},
		{"at max", SessionTTLMaxSeconds, false},
		{"above max", SessionTTLMaxSeconds + 1, true},
		{"zero", 0, true},
		{"negative", -10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSessionTTLSeconds(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrSessionTTLInvalid)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Рабочие лимиты размера тела (§97). Верхняя граница не константа — её задаёт
// потолок из конфига, поэтому проверяем обе стороны диапазона и случай
// «потолок не задан».
func TestValidateMaxBodyBytes(t *testing.T) {
	t.Parallel()

	const cap300 = 300 << 20

	tests := []struct {
		name       string
		value      int
		maxAllowed int
		wantErr    bool
	}{
		{name: "default fits under cap", value: BodyLimitDefaultBytes, maxAllowed: cap300},
		{name: "minimum allowed", value: BodyLimitMinBytes, maxAllowed: cap300},
		{name: "exactly at cap", value: cap300, maxAllowed: cap300},
		{name: "one byte over cap", value: cap300 + 1, maxAllowed: cap300, wantErr: true},
		{name: "below minimum", value: BodyLimitMinBytes - 1, maxAllowed: cap300, wantErr: true},
		{name: "zero rejected", value: 0, maxAllowed: cap300, wantErr: true},
		{name: "negative rejected", value: -1, maxAllowed: cap300, wantErr: true},
		{name: "cap not configured checks lower bound only", value: 1 << 30, maxAllowed: 0},
		{name: "cap not configured still rejects tiny", value: 1, maxAllowed: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMaxBodyBytes(tt.value, tt.maxAllowed)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrMaxBodyBytesInvalid)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidateMaxAsyncBodyBytes — тот же контракт, но своя sentinel-ошибка:
// оператор должен видеть, какое именно поле формы не прошло.
func TestValidateMaxAsyncBodyBytes(t *testing.T) {
	t.Parallel()

	const cap300 = 300 << 20

	require.NoError(t, ValidateMaxAsyncBodyBytes(BodyLimitDefaultBytes, cap300))
	require.ErrorIs(t, ValidateMaxAsyncBodyBytes(cap300+1, cap300), ErrMaxAsyncBodyBytesInvalid)
	require.ErrorIs(t, ValidateMaxAsyncBodyBytes(0, cap300), ErrMaxAsyncBodyBytesInvalid)
}

func TestBodyLimitOrDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, BodyLimitDefaultBytes, BodyLimitOrDefault(nil), "nil = настройка не задана")

	v := 42 << 20
	assert.Equal(t, v, BodyLimitOrDefault(&v))
}

// TestClampBodyLimit: значение из БД может оказаться выше потолка, если конфиг
// снизили уже после сохранения настройки. Работаем по потолку и сообщаем, что
// зажали, — вызывающий пишет об этом в лог.
func TestClampBodyLimit(t *testing.T) {
	t.Parallel()

	const cap300 = 300 << 20

	got, clamped := ClampBodyLimit(100<<20, cap300)
	assert.Equal(t, 100<<20, got)
	assert.False(t, clamped)

	got, clamped = ClampBodyLimit(cap300, cap300)
	assert.Equal(t, cap300, got)
	assert.False(t, clamped, "равное потолку не зажимается")

	got, clamped = ClampBodyLimit(400<<20, cap300)
	assert.Equal(t, cap300, got)
	assert.True(t, clamped)

	got, clamped = ClampBodyLimit(400<<20, 0)
	assert.Equal(t, 400<<20, got)
	assert.False(t, clamped, "потолок не задан — не зажимаем")
}
