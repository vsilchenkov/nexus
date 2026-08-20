package domain_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// TestRejectReasonForStatus: резервное отображение статуса в причину (§94.2).
// Оно работает там, где отказ вернул не доменный слой (413 из чтения тела, 429
// из middleware лимита) и причину в контекст никто не положил.
func TestRejectReasonForStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   domain.RejectReason
	}{
		{name: "404 node not found", status: http.StatusNotFound, want: domain.RejectReasonNodeNotFound},
		{name: "405 method", status: http.StatusMethodNotAllowed, want: domain.RejectReasonMethodNotAllowed},
		{name: "401 auth", status: http.StatusUnauthorized, want: domain.RejectReasonUnauthorized},
		{name: "403 allowlist", status: http.StatusForbidden, want: domain.RejectReasonURLNotAllowed},
		{name: "413 body", status: http.StatusRequestEntityTooLarge, want: domain.RejectReasonBodyTooLarge},
		{name: "429 rate limit", status: http.StatusTooManyRequests, want: domain.RejectReasonRateLimited},
		{name: "503 disabled", status: http.StatusServiceUnavailable, want: domain.RejectReasonNodeDisabled},
		{name: "508 loop", status: http.StatusLoopDetected, want: domain.RejectReasonLoopDetected},
		{name: "400 bad request", status: http.StatusBadRequest, want: domain.RejectReasonBadRequest},
		// Неописанный статус не приписывается чужой причине: ненулевой счётчик
		// «other» — сигнал, что появилась ветка отказа мимо §94.2.
		{name: "418 unknown", status: http.StatusTeapot, want: domain.RejectReasonOther},
		{name: "451 unknown", status: http.StatusUnavailableForLegalReasons, want: domain.RejectReasonOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := domain.RejectReasonForStatus(tt.status)
			assert.Equal(t, tt.want, got)
			assert.True(t, got.Valid(), "выведенная причина обязана быть известной")
		})
	}
}

func TestRejectReasonValid(t *testing.T) {
	t.Parallel()

	assert.True(t, domain.RejectReasonNodeNotFound.Valid())
	assert.True(t, domain.RejectReasonOther.Valid())
	assert.False(t, domain.RejectReason("").Valid())
	assert.False(t, domain.RejectReason("node not found").Valid())
}

// TestRejectedGroupKeyNormalize: ключ приводится к тому виду, в котором он
// лежит в БД. Пустой слог обязан стать default (иначе один узел давал бы две
// группы в зависимости от формы адреса §78.1), а длинные поля — обрезаться до
// колонок миграции 0040.
func TestRejectedGroupKeyNormalize(t *testing.T) {
	t.Parallel()

	t.Run("empty slug becomes default", func(t *testing.T) {
		t.Parallel()
		k := domain.RejectedGroupKey{NodePath: "telephony", Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST"}
		k.Normalize()
		assert.Equal(t, domain.DefaultTeamSlug, k.TeamSlug)
	})

	t.Run("long path truncated", func(t *testing.T) {
		t.Parallel()
		k := domain.RejectedGroupKey{
			TeamSlug:   strings.Repeat("s", domain.RejectedTeamSlugMaxLen+10),
			NodePath:   strings.Repeat("p", domain.RejectedNodePathMaxLen+100),
			Reason:     domain.RejectReasonNodeNotFound,
			HTTPMethod: strings.Repeat("M", domain.RejectedMethodMaxLen+5),
		}
		k.Normalize()
		assert.Len(t, k.TeamSlug, domain.RejectedTeamSlugMaxLen)
		assert.Len(t, k.NodePath, domain.RejectedNodePathMaxLen)
		assert.Len(t, k.HTTPMethod, domain.RejectedMethodMaxLen)
	})

	t.Run("multibyte path truncated by runes, not bytes", func(t *testing.T) {
		t.Parallel()
		k := domain.RejectedGroupKey{
			TeamSlug: "vika",
			// Кириллица: обрезка по байтам порвала бы символ посередине, и в БД
			// уехала бы невалидная UTF-8 строка.
			NodePath: strings.Repeat("я", domain.RejectedNodePathMaxLen+50),
			Reason:   domain.RejectReasonNodeNotFound, HTTPMethod: "GET",
		}
		k.Normalize()
		require.True(t, len([]rune(k.NodePath)) == domain.RejectedNodePathMaxLen)
		assert.Equal(t, strings.Repeat("я", domain.RejectedNodePathMaxLen), k.NodePath)
	})

	t.Run("unknown reason becomes other", func(t *testing.T) {
		t.Parallel()
		k := domain.RejectedGroupKey{TeamSlug: "vika", NodePath: "p", Reason: "нечто", HTTPMethod: "GET"}
		k.Normalize()
		assert.Equal(t, domain.RejectReasonOther, k.Reason)
	})
}

// TestValidateRejectedRetentionDays: 0 допустим и означает «сбор выключен»
// (§94.5), отрицательное и больше года — нет.
func TestValidateRejectedRetentionDays(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		days    int
		wantErr bool
	}{
		{name: "zero disables collection", days: 0},
		{name: "one day", days: 1},
		{name: "default", days: domain.RejectedRetentionDefaultDays},
		{name: "max", days: domain.RejectedRetentionMaxDays},
		{name: "negative", days: -1, wantErr: true},
		{name: "above max", days: domain.RejectedRetentionMaxDays + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateRejectedRetentionDays(tt.days)
			if tt.wantErr {
				require.ErrorIs(t, err, domain.ErrRejectedRetentionInvalid)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRejectedRetentionOrDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, domain.RejectedRetentionDefaultDays, domain.RejectedRetentionOrDefault(nil))

	zero := 0
	assert.Equal(t, 0, domain.RejectedRetentionOrDefault(&zero),
		"явный ноль — это «выключено», а не «настройка не задана»")

	seven := 7
	assert.Equal(t, 7, domain.RejectedRetentionOrDefault(&seven))
}

func TestRejectedGroupResolved(t *testing.T) {
	t.Parallel()

	var g domain.RejectedGroup
	assert.False(t, g.Resolved())

	now := time.Now()
	g.ResolvedAt = &now
	assert.True(t, g.Resolved())
}
