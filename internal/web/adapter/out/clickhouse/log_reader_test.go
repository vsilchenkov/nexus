package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// TestClassifyCHErr — классификатор ошибок read-path: сбои ДОСТУПНОСТИ CH
// (dial refused, таймаут, отменённый контекст) помечаются
// domain.ErrLogsBackendUnavailable (для мягкой деградации), а серверные
// ошибки запроса (битый SQL, нет таблицы) — НЕТ (остаются 500, чтобы баги
// не маскировались). Во всех случаях контекст op и исходная ошибка
// сохраняются в тексте.
func TestClassifyCHErr(t *testing.T) {
	t.Parallel()

	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

	tests := []struct {
		name        string
		err         error
		wantUnavail bool
	}{
		{"dial refused (net.OpError)", dialErr, true},
		{"wrapped dial refused", fmt.Errorf("prepare batch: %w", dialErr), true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"context canceled", context.Canceled, true},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), true},
		{"server-side exception (unknown table)", errors.New("code: 60, message: Unknown table"), false},
		{"generic error", errors.New("scan failed"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyCHErr("clickhouse search", tt.err)
			assert.Equal(t, tt.wantUnavail, errors.Is(got, domain.ErrLogsBackendUnavailable),
				"unavailable classification mismatch")
			// op-контекст и исходная ошибка всегда в тексте.
			assert.ErrorContains(t, got, "clickhouse search")
			assert.ErrorIs(t, got, tt.err, "original error must remain in the chain")
		})
	}
}

// TestChUnavailable_Nil — nil не считается недоступностью.
func TestChUnavailable_Nil(t *testing.T) {
	t.Parallel()
	assert.False(t, chUnavailable(nil))
}
