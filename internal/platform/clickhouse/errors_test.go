package clickhouse

import (
	"errors"
	"fmt"
	"testing"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestIsMissingColumnErr(t *testing.T) {
	t.Parallel()
	missing := func(code int32, msg string) error { return &chgo.Exception{Code: code, Message: msg} }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unknown_identifier", err: missing(47, "Missing columns: 'client_host'"), want: true},
		{name: "no_such_column", err: missing(16, "There's no column 'client_host' in table"), want: true},
		{name: "not_found_in_block", err: missing(10, "Not found column client_host in block"), want: true},
		{name: "wrapped", err: fmt.Errorf("select: %w", missing(47, "client_host")), want: true},
		{name: "other_column", err: missing(47, "Missing columns: 'method'")},
		{name: "other_code", err: missing(60, "Table logs.x doesn't exist (client_host)")},
		{name: "not_ch_error", err: errors.New("client_host missing")},
		{name: "nil", err: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsMissingColumnErr(tt.err, "client_host"))
		})
	}
}
