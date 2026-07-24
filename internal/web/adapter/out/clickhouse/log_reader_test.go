package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// TestIsMissingColumnErr — §67: распознавание серверной ошибки CH «нет такой
// колонки» (внешняя таблица §64 до ручного ALTER) для мягкой деградации
// facet-запросов. Прочие Exception-коды и не-Exception ошибки не матчатся.
func TestIsMissingColumnErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"UNKNOWN_IDENTIFIER (47) с именем колонки",
			&chgo.Exception{Code: 47, Message: "Unknown expression identifier `client_host` in scope"}, true},
		{"NO_SUCH_COLUMN_IN_TABLE (16)",
			&chgo.Exception{Code: 16, Message: "No such column client_host in table"}, true},
		{"NOT_FOUND_COLUMN_IN_BLOCK (10)",
			&chgo.Exception{Code: 10, Message: "Not found column client_host in block"}, true},
		{"обёрнутая (fmt.Errorf %w)",
			fmt.Errorf("query: %w", &chgo.Exception{Code: 47, Message: "Unknown expression identifier `client_host`"}), true},
		{"код 47, но другая колонка",
			&chgo.Exception{Code: 47, Message: "Unknown expression identifier `foo`"}, false},
		{"другой код (60 unknown table)",
			&chgo.Exception{Code: 60, Message: "Table nexus_default.x does not exist (client_host)"}, false},
		{"не Exception", errors.New("client_host: connection refused"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isMissingColumnErr(tt.err, "client_host"))
		})
	}
}

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
		// §67: нет колонки client_host (внешняя таблица §64 до ручного ALTER) —
		// деградация, а не 500 (иначе поллинг «Логов» флудит Sentry до ALTER).
		{"missing client_host column (§67 deploy window)",
			&chgo.Exception{Code: 47, Message: "Unknown expression identifier `client_host` in scope"}, true},
		{"missing OTHER column stays server error",
			&chgo.Exception{Code: 47, Message: "Unknown expression identifier `foo`"}, false},
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
