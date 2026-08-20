package usecase

import (
	"context"
	"time"

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
// Распределённого лока цикла у задачи нет: в этой ветке его ещё не существует
// (§93 с redislock в dev не влит). Для чистки это безопасно — DELETE по
// условию идемпотентен, и вторая реплика лишь сделает пустой проход. При
// слиянии с §93 цикл берётся под лок так же, как чистка аудита, причём у
// периодического и внеочередного проходов ключи ДОЛЖНЫ быть разными: общий лок
// с длинным TTL от тикера отложил бы применение нового срока хранения, то есть
// сломал бы обещание «уменьшение применяется сразу».
type RejectedHousekeeping struct {
	repo      port.RejectedRepo
	retention *RejectRetentionProvider
	interval  time.Duration
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
	h.Cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.Cycle(ctx)
		}
	}
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
		return
	}
	h.logger.Debug("rejected housekeeping: nothing to delete",
		h.logger.Int("retention_days", days))
}
