package clickhouse

import (
	"context"
	"fmt"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// SchemaMaintainerCH доводит существующие таблицы логов до текущей схемы —
// те же ADD COLUMN IF NOT EXISTS, что выполняются на старте Web (§37, §39,
// §42.10, §67). Нужен восстановлению конфигурации (§110.6): уцелевшая таблица
// старой версии иначе ломала бы чтение логов до рестарта сервисов.
//
// Backfill размеров тел (§42.10) здесь не запускается: он тяжёлый и
// выполнится в фоне на следующем старте Web (§109.2); SELECT от него не
// зависит.
type SchemaMaintainerCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.CHSchemaMaintainer = (*SchemaMaintainerCH)(nil)

func NewSchemaMaintainer(conn ConnProvider, logger logging.Logger) *SchemaMaintainerCH {
	return &SchemaMaintainerCH{conn: conn, logger: logger}
}

// EnsureColumns — в отличие от старта, ошибка любой таблицы возвращается:
// восстановление, доложившее «таблицы дополнены», не должно оставить узлы с
// падающим чтением логов.
func (m *SchemaMaintainerCH) EnsureColumns(ctx context.Context, tables []string) error {
	if err := chpf.EnsureLogColumns(ctx, m.conn.Conn(), tables, m.logger); err != nil {
		return fmt.Errorf("ensure log columns: %w", err)
	}
	return nil
}
