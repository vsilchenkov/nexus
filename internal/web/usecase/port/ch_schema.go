package port

import (
	"context"

	"nexus/internal/domain"
)

// CHSchemaInspector — интроспекция и ALTER существующей таблицы логов (§56).
// Определён на стороне консьюмера (CHSchemaSyncUsecase); реализация — в
// adapter/out/clickhouse.
type CHSchemaInspector interface {
	// ReadTableSchema читает снимок схемы существующей таблицы. found=false —
	// таблицы нет (узел ещё не писал логи), тогда синхронизировать нечего.
	ReadTableSchema(ctx context.Context, table string) (schema domain.CurrentTableSchema, found bool, err error)
	// ApplyAlter исполняет ALTER-операторы по порядку. Операторы построены
	// доменным PlanSchemaSync из валидированных имён — параметризации в CH нет.
	ApplyAlter(ctx context.Context, table string, statements []string) error
}
