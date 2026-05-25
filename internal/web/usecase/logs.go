package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

// LogsUsecase — чтение логов узлов из ClickHouse + SSE live-tail (§7.4 ТЗ).
type LogsUsecase struct {
	logs   port.LogReader
	nodes  port.NodeRepo
	logger logging.Logger

	pollInterval time.Duration
	streamLimit  int
}

func NewLogsUsecase(logs port.LogReader, nodes port.NodeRepo, logger logging.Logger) *LogsUsecase {
	return &LogsUsecase{
		logs:         logs,
		nodes:        nodes,
		logger:       logger,
		pollInterval: 1 * time.Second,
		streamLimit:  200,
	}
}

// ListSince — snapshot последних записей.
//
// sinceMs — Unix-миллисекунды; 0 = последние limit записей. limit — 1..500;
// дефолт 100.
func (u *LogsUsecase) ListSince(ctx context.Context, nodeID string, sinceMs int64, limit int) ([]*domain.LogRecord, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if n.ClickHouseTable == "" {
		return nil, fmt.Errorf("node %q has no clickhouse_table configured", n.Path)
	}
	return u.logs.ListSince(ctx, n.ClickHouseTable, sinceMs, limit)
}

// Subscribe — SSE live-tail (§7.4 ТЗ).
//
// Реализация — простой polling раз в pollInterval с курсором по date_request.
// Канал закрывается, когда ctx отменён. errCh передаёт фатальные ошибки
// (например, удалена таблица); после ошибки оба канала закрываются.
//
// Это не самая дешёвая реализация (каждый клиент = свой опрос ClickHouse),
// но для админок этого хватает. Долгосрочный путь — pub/sub через
// Kafka databus.logs (out of scope в v1).
func (u *LogsUsecase) Subscribe(ctx context.Context, nodeID string) (<-chan *domain.LogRecord, <-chan error, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	if n.ClickHouseTable == "" {
		return nil, nil, errors.New("node has no clickhouse_table configured")
	}

	ch := make(chan *domain.LogRecord, 64)
	errCh := make(chan error, 1)
	cursor := time.Now().UnixMilli()

	go func() {
		defer close(ch)
		defer close(errCh)
		tick := time.NewTicker(u.pollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				recs, err := u.logs.ListSince(ctx, n.ClickHouseTable, cursor, u.streamLimit)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					select {
					case errCh <- err:
					default:
					}
					return
				}
				for _, r := range recs {
					if ms := r.DateRequest.UnixMilli(); ms > cursor {
						cursor = ms
					}
					select {
					case ch <- r:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch, errCh, nil
}
