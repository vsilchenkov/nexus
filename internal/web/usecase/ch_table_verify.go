package usecase

import (
	"context"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// §64: проверка структуры внешней (ручной) таблицы логов. Оператор указывает в
// форме узла таблицу, которой Nexus не управляет, — до сохранения нужно
// убедиться, что она пригодна: Sender сможет писать в неё INSERT, а UI читать
// логи и считать метрики.
//
// Проверка идёт по ИМЕНИ таблицы, а не по id узла: кнопка должна работать и в
// форме ещё не сохранённого узла.

// tableColumnsReader — фактические колонки таблицы с типами. Узкий интерфейс на
// стороне консьюмера (ISP): порт CHSchemaInspector §56 сюда не расширяем, здесь
// не нужны ни CODEC, ни индексы, ни TTL.
type tableColumnsReader interface {
	ReadTableColumns(ctx context.Context, table string) ([]domain.CHLogColumn, bool, error)
}

// CHTableVerifyResult — итог проверки. OK=false при отсутствии таблицы либо
// любом расхождении состава/типов колонок.
type CHTableVerifyResult struct {
	OK           bool                       `json:"ok"`
	Table        string                     `json:"table"`
	TableMissing bool                       `json:"table_missing"`
	Missing      []string                   `json:"missing"`
	Mismatched   []domain.LogColumnMismatch `json:"mismatched"`
}

// CHTableVerifyUsecase — §64. columns==nil (ClickHouse не настроен) → ErrCHUnavailable.
type CHTableVerifyUsecase struct {
	columns tableColumnsReader
	logger  logging.Logger
}

func NewCHTableVerifyUsecase(columns tableColumnsReader, logger logging.Logger) *CHTableVerifyUsecase {
	return &CHTableVerifyUsecase{columns: columns, logger: logger}
}

// Verify сверяет структуру таблицы с обязательной схемой логов (§4.3).
// Расхождения — не ошибка вызова, а содержимое результата: 200 с OK=false.
func (u *CHTableVerifyUsecase) Verify(ctx context.Context, table string) (*CHTableVerifyResult, error) {
	if u.columns == nil {
		return nil, ErrCHUnavailable
	}
	if !domain.IsValidCHTableName(table) {
		return nil, domain.ErrNodeClickHouseTableInvalid
	}

	cols, found, err := u.columns.ReadTableColumns(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("read table columns: %w", err)
	}
	if !found {
		return &CHTableVerifyResult{Table: table, TableMissing: true}, nil
	}

	res := domain.VerifyLogTableColumns(cols)
	u.logger.Debug("ch table verified",
		u.logger.Str("table", table), u.logger.Int("columns", len(cols)),
		u.logger.Int("missing", len(res.Missing)), u.logger.Int("mismatched", len(res.Mismatched)))

	return &CHTableVerifyResult{
		OK:         res.OK(),
		Table:      table,
		Missing:    res.Missing,
		Mismatched: res.Mismatched,
	}, nil
}
