package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isForeignKeyViolation сообщает, что ошибка — нарушение FK-ограничения
// (SQLSTATE 23503), напр. попытка удалить запись, на которую ссылаются.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

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

// nullUUID возвращает nil для пустой строки (→ SQL NULL для nullable UUID
// колонки) или саму строку. Без этого пустой "" привёл бы к ошибке формата
// uuid / FK-нарушению.
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullSafe возвращает s, либо пустой не-nil слайс, если s == nil.
// pgx/v5 кодирует nil-слайс как NULL, что ломает NOT NULL колонки с
// дефолтом '{}' — DEFAULT не срабатывает, потому что значение явно
// передано в INSERT. Этот helper применяется ко всем TEXT[] полям.
func nullSafe(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
