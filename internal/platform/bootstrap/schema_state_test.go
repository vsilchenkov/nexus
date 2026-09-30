package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSchemaState_KnownVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		st   SchemaState
		want uint
	}{
		{"схема как у бинаря", SchemaState{Version: 43, BinaryVersion: 43}, 43},
		// Незавершённый откат кода (§74.3): бинарь не читает колонок новее
		// своего каталога — резервная копия не должна обещать схему БД.
		{"схема новее бинаря", SchemaState{Version: 45, Ahead: true, BinaryVersion: 43}, 43},
		{"версия бинаря неизвестна", SchemaState{Version: 45, Ahead: true}, 45},
		{"миграций не было", SchemaState{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.st.KnownVersion())
		})
	}
}
