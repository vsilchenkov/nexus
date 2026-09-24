package sender

import (
	"context"
	"log/slog"
	"time"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// startupPhases — длительности фаз старта Sender'а (§109.2). Пока gRPC-порт
// закрыт, sync-трафик Receiver'а отказывает целиком, поэтому строка «sender
// startup phases» показывает, куда ушло время до открытия порта — и на стенде,
// и на бою после выката.
type startupPhases struct {
	started time.Time
	last    time.Time
	attrs   []slog.Attr
}

func newStartupPhases(now time.Time) *startupPhases {
	return &startupPhases{started: now, last: now}
}

// mark закрывает фазу name: её длительность — от предыдущей отметки.
func (p *startupPhases) mark(name string, now time.Time) {
	p.attrs = append(p.attrs, slog.Int64(name+"_ms", now.Sub(p.last).Milliseconds()))
	p.last = now
}

// fields — отмеченные фазы и until_grpc_ms: всё время от начала Start до
// открытого порта.
func (p *startupPhases) fields(now time.Time) []slog.Attr {
	return append(p.attrs[:len(p.attrs):len(p.attrs)],
		slog.Int64("until_grpc_ms", now.Sub(p.started).Milliseconds()))
}

// logStartupPhases пишет фазы старта, когда gRPC-порт уже слушает. Вызывается
// один раз из startGRPC; без Start (тесты адаптера) фаз нет — строки тоже.
func (a *App) logStartupPhases(now time.Time) {
	if a.startup == nil {
		return
	}
	a.logger.Info("sender startup phases", a.startup.fields(now)...)
}

// startBodySizeBackfill (§42.10) запускает разовый backfill размеров тел в
// фоне (§109.2). Его guard-count на каждом старте — полный проход по колонкам
// тел каждой лог-таблицы, а синхронный старт держал из-за него закрытым
// gRPC-порт: выкат Sender'а стоил sync-трафику десятков секунд отказа
// (Sentry NEXUS-T/S). Колонки к этому моменту уже добавлены синхронно
// (ensureCHSchema), так что INSERT по новой схеме от фона не зависит.
func (a *App) startBodySizeBackfill(ctx context.Context, tables []string) {
	if a.chMgr == nil || len(tables) == 0 {
		return
	}
	conn := a.chMgr.Conn()
	a.bodySizeBackfillDone = safego.Go(a.logger, "sender.bodySizeBackfill", func() {
		runBodySizeBackfill(ctx, conn, tables, a.logger)
	})
}

// runBodySizeBackfill — тело фоновой задачи: backfill + debug-строка с исходом
// (§51.9: фон молчит, а «почему долго стартует ClickHouse» потом не восстановить).
func runBodySizeBackfill(ctx context.Context, conn chdrv.Conn, tables []string, logger logging.Logger) {
	started := time.Now()
	chpf.BackfillBodySizes(ctx, conn, tables, logger)
	logger.Debug("backfill body sizes: finished",
		logger.Int("tables", len(tables)),
		logger.Int("duration_ms", int(time.Since(started).Milliseconds())),
		logger.Str("ctx_err", errString(ctx.Err())))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
