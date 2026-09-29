package clickhouse

import (
	"context"
	"errors"
	"testing"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

type stubOwnership struct {
	verdict chpf.Verdict
	owner   *chpf.Owner
	err     error
}

func (s stubOwnership) Check(context.Context, string) (chpf.Verdict, *chpf.Owner, error) {
	return s.verdict, s.owner, s.err
}

// nilConn — ClickHouse, которого нет: до запроса таблиц дело доходить не
// должно.
type nilConn struct{}

func (nilConn) Conn() chdriver.Conn { return nil }

func TestBackupInspector_InspectDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		owners  stubOwnership
		want    port.CHDatabaseState
		wantErr bool
	}{
		{"базы нет — таблицы не считаются", stubOwnership{verdict: chpf.VerdictNoDatabase},
			port.CHDatabaseState{Ownership: port.CHOwnershipNone}, false},
		{"сбой проверки владения", stubOwnership{err: errors.New("ch down")},
			port.CHDatabaseState{Ownership: port.CHOwnershipUnknown}, true},
		{"чужая база — владелец в ответе, счёт таблиц без соединения падает",
			stubOwnership{verdict: chpf.VerdictForeign, owner: &chpf.Owner{InstanceID: "msk"}},
			port.CHDatabaseState{Ownership: port.CHOwnershipUnknown}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			i := NewBackupInspector(nilConn{}, tt.owners, logging.NewNoop())
			got, err := i.InspectDatabase(context.Background(), "nexus_acme")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOwnershipOf(t *testing.T) {
	t.Parallel()
	for v, want := range map[chpf.Verdict]port.CHOwnership{
		chpf.VerdictNoDatabase: port.CHOwnershipNone,
		chpf.VerdictOwned:      port.CHOwnershipOwned,
		chpf.VerdictUnclaimed:  port.CHOwnershipUnclaimed,
		chpf.VerdictForeign:    port.CHOwnershipForeign,
		chpf.VerdictConflict:   port.CHOwnershipConflict,
		chpf.VerdictUnknown:    port.CHOwnershipUnknown,
	} {
		assert.Equal(t, want, ownershipOf(v), v.String())
	}
}
