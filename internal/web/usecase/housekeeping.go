package usecase

import (
	"context"
	"time"

	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
)

// Housekeeping — фоновые задачи Web Service. В Phase 4 — только
// удаление старых записей audit-журнала (§7.13: «housekeeping-job
// удаляет старые записи раз в сутки»).
//
// Расширение: ClickHouse partition drop (§4.3) — нужно отдельное
// подключение к CH из Web; делается в следующей итерации.
type Housekeeping struct {
	audit         *AuditUsecase
	retentionDays int
	interval      time.Duration
	clock         clock.Clock
	logger        logging.Logger
}

// HousekeepingOption — функциональная опция конструктора.
type HousekeepingOption func(*Housekeeping)

// WithHousekeepingClock подменяет источник времени (§4 CLAUDE.md): от него
// зависит граница retention — какие записи аудита считать устаревшими.
func WithHousekeepingClock(c clock.Clock) HousekeepingOption {
	return func(h *Housekeeping) { h.clock = c }
}

func NewHousekeeping(audit *AuditUsecase, retentionDays int, logger logging.Logger, opts ...HousekeepingOption) *Housekeeping {
	h := &Housekeeping{
		audit:         audit,
		retentionDays: retentionDays,
		interval:      24 * time.Hour,
		clock:         clock.System(),
		logger:        logger,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Run блокируется до ctx.Done. Запускается из app.Start в горутине.
func (h *Housekeeping) Run(ctx context.Context) {
	if h.retentionDays <= 0 {
		return
	}
	tick := time.NewTicker(h.interval)
	defer tick.Stop()

	// Первый прогон сразу — чтобы при долгом простое сервиса было
	// видно эффект без ожидания целого дня.
	h.cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.cycle(ctx)
		}
	}
}

func (h *Housekeeping) cycle(ctx context.Context) {
	cutoff := h.clock.Now().AddDate(0, 0, -h.retentionDays)
	n, err := h.audit.repo.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		h.logger.ErrorWithOp("housekeeping audit delete failed", err, "housekeeping.cycle")
		return
	}
	if n > 0 {
		h.logger.Info("housekeeping audit cleanup",
			h.logger.Int("deleted", n),
			h.logger.Str("cutoff", cutoff.Format(time.RFC3339)))
	}
}
