package usecase

import (
	"context"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// rejectedHousekeepingInterval — период чистки журнала отказов.
//
// Час, а не сутки (как у аудита): уменьшение срока хранения обязано
// применяться сразу (§94.5). Обычный путь — событие reload сразу после
// сохранения настройки; тикер страхует реплику, которая событие пропустила
// (была недоступна, только что поднялась).
const rejectedHousekeepingInterval = time.Hour

// RejectedHousekeeping удаляет устаревшие записи журнала отказов (§94.5).
//
// Отдельная задача, а не ещё один шаг в Housekeeping: у той суточный тикер, а
// здесь нужен и другой период, и внеочередной запуск по смене настройки.
//
// Периодический цикл берётся под лок реплики (§93.7) — как чистка аудита: с
// двумя репликами Web тикер срабатывает дважды, и обе делают один и тот же
// DELETE по растущей таблице.
//
// А внеочередной прогон (CycleNow по смене настройки) лока НЕ берёт вовсе, и
// это не упущение: общий лок с TTL от часового тикера отложил бы применение
// нового срока хранения до следующего часа — ровно то обещание §94.5
// («уменьшение применяется сразу»), ради которого прогон и существует. Двойной
// DELETE при этом безвреден: условие идемпотентно, вторая реплика делает
// пустой проход.
type RejectedHousekeeping struct {
	repo      port.RejectedRepo
	retention *RejectRetentionProvider
	interval  time.Duration
	lock      CycleLock
	clock     clock.Clock
	logger    logging.Logger
}

// RejectedHousekeepingOption — функциональная опция конструктора.
type RejectedHousekeepingOption func(*RejectedHousekeeping)

// WithRejectedHousekeepingClock подменяет источник времени (§4 CLAUDE.md): от
// него зависит граница retention.
func WithRejectedHousekeepingClock(c clock.Clock) RejectedHousekeepingOption {
	return func(h *RejectedHousekeeping) {
		if c != nil {
			h.clock = c
		}
	}
}

// WithRejectedHousekeepingInterval меняет период цикла (для тестов).
func WithRejectedHousekeepingInterval(d time.Duration) RejectedHousekeepingOption {
	return func(h *RejectedHousekeeping) {
		if d > 0 {
			h.interval = d
		}
	}
}

// WithRejectedHousekeepingLock подключает лок ПЕРИОДИЧЕСКОГО цикла (§93.7).
// Ключ обязан отличаться от ключа чистки аудита: это разные задачи с разными
// периодами, под общим ключом одна отменяла бы другую. На внеочередной прогон
// (CycleNow) лок не распространяется — см. комментарий к типу.
func WithRejectedHousekeepingLock(l CycleLock) RejectedHousekeepingOption {
	return func(h *RejectedHousekeeping) {
		if l != nil {
			h.lock = l
		}
	}
}

func NewRejectedHousekeeping(repo port.RejectedRepo, retention *RejectRetentionProvider,
	logger logging.Logger, opts ...RejectedHousekeepingOption,
) *RejectedHousekeeping {
	h := &RejectedHousekeeping{
		repo:      repo,
		retention: retention,
		interval:  rejectedHousekeepingInterval,
		clock:     clock.System(),
		logger:    logger,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Run блокируется до ctx.Done. Запускается из app.Start в горутине.
func (h *RejectedHousekeeping) Run(ctx context.Context) {
	tick := time.NewTicker(h.interval)
	defer tick.Stop()

	// Первый проход сразу: после простоя сервиса накопившееся должно уйти без
	// ожидания целого периода.
	h.tickCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.tickCycle(ctx)
		}
	}
}

// tickCycle — плановый проход: выполняется одной репликой из пары (§93.7).
func (h *RejectedHousekeeping) tickCycle(ctx context.Context) {
	if !h.acquireCycle(ctx) {
		return
	}
	h.Cycle(ctx)
}

// acquireCycle — можно ли этой реплике выполнять плановый цикл.
//
// Ошибка Redis трактуется как запрет — тот же выбор, что у чистки аудита: с
// больным Redis дешевле пропустить час, чем пройти вторым DELETE по таблице,
// которая и так растёт. Пропуск не теряет данные: следующий тик через час, а
// смена срока хранения всё равно приходит внеочередным прогоном без лока.
func (h *RejectedHousekeeping) acquireCycle(ctx context.Context) bool {
	if h.lock == nil {
		return true
	}
	ttl := max(h.interval/2, time.Minute)
	ok, err := h.lock.TryLock(ctx, ttl)
	if err != nil {
		h.logger.Warn("rejected housekeeping: cycle lock check failed, skipping cycle",
			h.logger.Err(err))
		return false
	}
	if !ok {
		h.logger.Debug("rejected housekeeping: cycle already taken by another replica")
	}
	return ok
}

// CycleNow выполняет внеочередную чистку — вызывается подписчиком reload сразу
// после смены настройки: уменьшение срока обязано применяться немедленно, а не
// в следующем часу (§94.5).
func (h *RejectedHousekeeping) CycleNow(ctx context.Context) { h.Cycle(ctx) }

// Cycle выполняет одну чистку.
func (h *RejectedHousekeeping) Cycle(ctx context.Context) {
	if h.repo == nil || h.retention == nil {
		return
	}
	days := h.retention.Days()
	if days <= 0 {
		// Срок 0 — журнал выключен: накопленное удаляется целиком, иначе
		// выключение оставляло бы данные лежать без срока годности.
		n, err := h.repo.PurgeRejected(ctx)
		if err != nil {
			h.logger.ErrorWithOp("rejected housekeeping purge failed", err, "housekeeping.rejected")
			return
		}
		if n > 0 {
			h.logger.Info("rejected housekeeping purge", h.logger.Int("deleted", n))
		}
		return
	}

	cutoff := h.clock.Now().AddDate(0, 0, -days)
	n, err := h.repo.DeleteRejectedOlderThan(ctx, cutoff)
	if err != nil {
		h.logger.ErrorWithOp("rejected housekeeping delete failed", err, "housekeeping.rejected")
		return
	}
	if n > 0 {
		h.logger.Info("rejected housekeeping cleanup",
			h.logger.Int("deleted", n),
			h.logger.Int("retention_days", days),
			h.logger.Str("cutoff", cutoff.Format(time.RFC3339)))
	} else {
		h.logger.Debug("rejected housekeeping: nothing to delete",
			h.logger.Int("retention_days", days))
	}

	// Предохранитель от сканера: срока хранения мало, потому что каждая новая
	// попытка по случайному пути — это НОВАЯ группа. Держим самые свежие.
	trimmed, err := h.repo.TrimRejectedGroups(ctx, domain.RejectedMaxStoredGroups)
	if err != nil {
		h.logger.ErrorWithOp("rejected housekeeping trim failed", err, "housekeeping.rejected")
		return
	}
	if trimmed > 0 {
		h.logger.Warn("rejected housekeeping: group limit reached, oldest groups dropped",
			h.logger.Int("deleted", trimmed),
			h.logger.Int("limit", domain.RejectedMaxStoredGroups))
	}
}
