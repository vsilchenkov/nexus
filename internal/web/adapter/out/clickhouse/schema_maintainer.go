package clickhouse

import (
	"context"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// SchemaMaintainerCH доводит существующие таблицы логов до текущей схемы —
// те же ADD COLUMN IF NOT EXISTS, что выполняются на старте Web (§37, §39,
// §42.10, §67). Нужен восстановлению конфигурации (§110.6): уцелевшая таблица
// старой версии иначе ломала бы чтение логов до рестарта сервисов.
//
// Backfill размеров тел (§109.2) здесь не запускается: он тяжёлый и выполнится
// в фоне на следующем старте Web; SELECT от него не зависит.
type SchemaMaintainerCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.CHSchemaMaintainer = (*SchemaMaintainerCH)(nil)

func NewSchemaMaintainer(conn ConnProvider, logger logging.Logger) *SchemaMaintainerCH {
	return &SchemaMaintainerCH{conn: conn, logger: logger}
}

// EnsureColumns — ошибки отдельных ALTER логируются внутри Ensure*: как и на
// старте, недоведённая таблица — деградация чтения, а не отказ операции.
func (m *SchemaMaintainerCH) EnsureColumns(ctx context.Context, tables []string) {
	conn := m.conn.Conn()
	if conn == nil {
		m.logger.Warn("ensure ch columns skipped: clickhouse conn is nil", m.logger.Int("tables", len(tables)))
		return
	}
	chpf.EnsureNodeIDColumn(ctx, conn, tables, m.logger)
	chpf.EnsureHTTPMethodColumn(ctx, conn, tables, m.logger)
	chpf.EnsureClientHostColumn(ctx, conn, tables, m.logger)
	chpf.EnsureBodySizeColumns(ctx, conn, tables, m.logger)
}
