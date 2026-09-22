package clickhouse

import (
	"errors"
	"strings"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
)

// Коды серверных ошибок ClickHouse «нет такой колонки» (§67: деградация в
// деплой-окне и у внешних таблиц §64, где владелец ещё не выполнил ALTER).
const (
	codeNotFoundColumnInBlock = 10 // NOT_FOUND_COLUMN_IN_BLOCK
	codeNoSuchColumnInTable   = 16 // NO_SUCH_COLUMN_IN_TABLE
	codeUnknownIdentifier     = 47 // UNKNOWN_IDENTIFIER
)

// IsMissingColumnErr сообщает, что err — серверная ошибка ClickHouse «колонка
// col не существует». Такая ошибка — транзитное состояние деплой-окна (новая
// колонка ещё не добавлена), а не поломка: вызывающие деградируют мягко.
// Привязка к имени колонки обязательна — иначе под «деплой-окно» попали бы
// чужие поломки DDL.
func IsMissingColumnErr(err error, col string) bool {
	var ex *chgo.Exception
	if !errors.As(err, &ex) {
		return false
	}
	switch ex.Code {
	case codeNotFoundColumnInBlock, codeNoSuchColumnInTable, codeUnknownIdentifier:
		return strings.Contains(ex.Message, col)
	}
	return false
}
