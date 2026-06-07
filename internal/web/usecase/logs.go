package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
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
//
// teamID — multi-tenancy scope (Phase 10.D); пустая строка пропускает
// проверку (legacy CLI/тесты). Чужой узел → ErrNodeNotFound.
func (u *LogsUsecase) ListSince(ctx context.Context, nodeID, teamID string, sinceMs int64, limit int) ([]*domain.LogRecord, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.ListSince(ctx, n.ClickHouseTable, sinceMs, limit)
}

// Search — snapshot с расширенными фильтрами (Phase 6.8).
// Подставляет n.ClickHouseTable в q.Table.
func (u *LogsUsecase) Search(ctx context.Context, nodeID, teamID string, q port.LogQuery) ([]*domain.LogRecord, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	q.Table = n.ClickHouseTable
	return u.logs.Search(ctx, q)
}

// GetByID — одна запись лога целиком (включая тела request/response).
//
// Отдельный путь от ListSince/Search: списки возвращают только метаданные
// (без тел), а полное тело тянется лениво по клику на конкретную строку —
// иначе snapshot из сотен строк с большими JSON-телами вешает фронт (§7.4.1).
// Запись не найдена → domain.ErrNotFound.
func (u *LogsUsecase) GetByID(ctx context.Context, nodeID, teamID, logID string) (*domain.LogRecord, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	return u.logs.GetByID(ctx, n.ClickHouseTable, logID)
}

// resolveNode — общий путь: получить узел, проверить team scope, убедиться
// что у него настроен ClickHouseTable.
func (u *LogsUsecase) resolveNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if teamID != "" && n.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	if n.ClickHouseTable == "" {
		return nil, domain.ErrNodeLogsNotConfigured
	}
	return n, nil
}

// matchLogFilter — клиентский фильтр для live-tail. Совпадает по семантике
// с SQL-фильтром в LogReaderCH.Search, но применяется in-memory ко всем
// событиям перед отправкой клиенту (избегаем динамической перестройки
// polling-запроса при смене фильтра в UI).
func matchLogFilter(r *domain.LogRecord, q port.LogQuery) bool {
	if q.IP != "" && r.IP != q.IP {
		return false
	}
	if q.Host != "" && r.Host != q.Host {
		return false
	}
	switch q.Status {
	case "ok":
		if r.Status < 200 || r.Status > 299 {
			return false
		}
	case "err":
		if r.Status > 0 && r.Status < 400 {
			return false
		}
	}
	switch q.Done {
	case "yes":
		if !r.Done {
			return false
		}
	case "no":
		if r.Done {
			return false
		}
	}
	if q.Q != "" {
		needle := strings.ToLower(q.Q)
		if !strings.Contains(strings.ToLower(r.URL), needle) &&
			!strings.Contains(strings.ToLower(r.Request), needle) &&
			!strings.Contains(strings.ToLower(r.Response), needle) {
			return false
		}
	}
	return true
}

// Subscribe — SSE live-tail (§7.4 ТЗ). filter применяется in-memory
// к каждому событию перед отправкой клиенту (Phase 6.8) —
// SinceMs/UntilMs/Limit/Table из filter игнорируются (курсор сам управляется).
//
// Реализация — простой polling раз в pollInterval с курсором по date_request.
// Канал закрывается, когда ctx отменён. errCh передаёт фатальные ошибки
// (например, удалена таблица); после ошибки оба канала закрываются.
//
// Это не самая дешёвая реализация (каждый клиент = свой опрос ClickHouse),
// но для админок этого хватает. Долгосрочный путь — pub/sub через
// Kafka nexus.logs (out of scope в v1).
func (u *LogsUsecase) Subscribe(ctx context.Context, nodeID, teamID string, filter port.LogQuery) (<-chan *domain.LogRecord, <-chan error, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan *domain.LogRecord, 64)
	errCh := make(chan error, 1)
	cursor := time.Now().UnixMilli()

	go func() {
		defer close(ch)
		defer close(errCh)
		defer safego.Recover(u.logger, "web.logsLiveTail")
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
					if !matchLogFilter(r, filter) {
						continue
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
