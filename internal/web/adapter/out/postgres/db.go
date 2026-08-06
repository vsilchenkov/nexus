package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nexus/internal/domain/ackspec"
)

// isForeignKeyViolation сообщает, что ошибка — нарушение FK-ограничения
// (SQLSTATE 23503), напр. попытка удалить запись, на которую ссылаются.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isInvalidUUID сообщает, что ошибка — невалидный текст для типа uuid
// (SQLSTATE 22P02, invalid_text_representation): `:id` из пути не является UUID
// и не прошёл cast `$1::uuid`. Репозитории трактуют это как «не найдено»
// (Err…NotFound → 404), а не как 500 + шум в Sentry. См. NEXUS-7 (§44).
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
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

// nullInt32 возвращает nil для нуля (→ SQL NULL) или само значение. Нужен там,
// где ноль в домене означает «не задано» и обязан отличаться в БД от заданного
// нуля — §81.3 (политика circuit breaker'а узла).
func nullInt32(v int32) any {
	if v == 0 {
		return nil
	}
	return v
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

// derefStr/derefInt32 — безопасное разыменование nullable-колонок: NULL
// (nil-указатель из pgx.Scan) превращается в zero-значение домена. Применяется
// к §27-полям rmq_*/pull_*, которые NULL для request/requestAsync.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// ackSpecArg готовит спеку ответа приёма (§83) к записи в JSONB-колонку:
// nil-спека → SQL NULL, то есть «узел отвечает как раньше». Плейсхолдер в
// запросе обязан иметь каст ::jsonb (как у ch_templates.spec), иначе pgx
// отправит []byte как bytea.
func ackSpecArg(s *ackspec.Spec) (any, error) {
	if s == nil {
		return nil, nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal async_ack_spec: %w", err)
	}
	return raw, nil
}

// scanAckSpec разбирает JSONB-колонку в спеку. NULL/пусто → nil (прежний ответ).
func scanAckSpec(raw []byte) (*ackspec.Spec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s ackspec.Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("unmarshal async_ack_spec: %w", err)
	}
	return &s, nil
}
