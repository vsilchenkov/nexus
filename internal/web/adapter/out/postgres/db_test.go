package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsInvalidUUID проверяет, что хелпер распознаёт pg SQLSTATE 22P02
// (invalid_text_representation — не-UUID в `$1::uuid`) и только его, в т.ч.
// когда ошибка обёрнута через %w (NEXUS-7, §43.I).
func TestIsInvalidUUID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "invalid uuid 22P02", err: &pgconn.PgError{Code: "22P02"}, want: true},
		{name: "wrapped 22P02", err: fmt.Errorf("scan node: %w", &pgconn.PgError{Code: "22P02"}), want: true},
		{name: "fk violation 23503", err: &pgconn.PgError{Code: "23503"}, want: false},
		{name: "other pg error", err: &pgconn.PgError{Code: "23505"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isInvalidUUID(tt.err); got != tt.want {
				t.Fatalf("isInvalidUUID(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
