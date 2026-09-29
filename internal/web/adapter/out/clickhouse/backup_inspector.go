package clickhouse

import (
	"context"
	"errors"
	"fmt"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ownershipChecker — вердикт владения базой §70 (clickhouse.Guard).
type ownershipChecker interface {
	Check(ctx context.Context, db string) (chpf.Verdict, *chpf.Owner, error)
}

// BackupInspectorCH — опрос ClickHouse для плана восстановления конфигурации
// (§110.6): чья база и сколько в ней таблиц, есть ли таблица узла. Только
// чтение: базы и таблицы создаёт фаза применения.
type BackupInspectorCH struct {
	conn   ConnProvider
	owners ownershipChecker
	logger logging.Logger
}

var _ port.CHDatabaseInspector = (*BackupInspectorCH)(nil)

func NewBackupInspector(conn ConnProvider, owners ownershipChecker, logger logging.Logger) *BackupInspectorCH {
	return &BackupInspectorCH{conn: conn, owners: owners, logger: logger}
}

// InspectDatabase — вердикт маркера владения и число таблиц без маркера.
func (i *BackupInspectorCH) InspectDatabase(ctx context.Context, db string) (port.CHDatabaseState, error) {
	v, owner, err := i.owners.Check(ctx, db)
	if err != nil {
		return port.CHDatabaseState{Ownership: port.CHOwnershipUnknown}, fmt.Errorf("inspect database %s: %w", db, err)
	}
	st := port.CHDatabaseState{Ownership: ownershipOf(v)}
	if v == chpf.VerdictForeign && owner != nil {
		st.Owner = owner.InstanceID
	}
	if v == chpf.VerdictNoDatabase {
		return st, nil
	}
	if st.Tables, err = i.countTables(ctx, db); err != nil {
		return port.CHDatabaseState{Ownership: port.CHOwnershipUnknown}, err
	}
	return st, nil
}

// TableExists — есть ли таблица "<db>.<table>".
func (i *BackupInspectorCH) TableExists(ctx context.Context, table string) (bool, error) {
	return tableExists(ctx, i.conn, table)
}

func (i *BackupInspectorCH) countTables(ctx context.Context, db string) (int, error) {
	conn := i.conn.Conn()
	if conn == nil {
		return 0, errors.New("clickhouse conn is nil")
	}
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name != ?",
		db, chpf.MarkerTable).Scan(&cnt); err != nil {
		return 0, fmt.Errorf("count tables in %s: %w", db, err)
	}
	return int(cnt), nil
}

func ownershipOf(v chpf.Verdict) port.CHOwnership {
	switch v {
	case chpf.VerdictNoDatabase:
		return port.CHOwnershipNone
	case chpf.VerdictOwned:
		return port.CHOwnershipOwned
	case chpf.VerdictUnclaimed:
		return port.CHOwnershipUnclaimed
	case chpf.VerdictForeign:
		return port.CHOwnershipForeign
	case chpf.VerdictConflict:
		return port.CHOwnershipConflict
	default:
		return port.CHOwnershipUnknown
	}
}
