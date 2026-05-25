package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX — общий минимальный интерфейс над pgxpool.Pool и pgx.Tx.
//
// Используется в репозиториях: NodeRepoPg, AuditRepoPg принимают DBTX
// вместо конкретного pool, что позволяет UnitOfWorkPg создавать
// транзакционные клоны репозиториев с тем же типом.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
