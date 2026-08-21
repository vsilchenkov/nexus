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
// CycleLock — распределённый лок цикла периодической задачи (§93.7). Интерфейс
// объявлен здесь, на стороне консьюмера (CLAUDE.md §3); реализуется
// platform/redislock. nil = поведение до §93.
type CycleLock interface {
	TryLock(ctx context.Context, ttl time.Duration) (bool, error)
}

type Housekeeping struct {
	audit         *AuditUsecase
	retentionDays int
	interval      time.Duration
	lock          CycleLock
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

// WithHousekeepingLock подключает лок цикла (§93.7): с двумя репликами Web
// суточный тикер срабатывает дважды, и обе реплики удаляют одни и те же записи
// аудита. Второй DELETE безвреден по данным, но это лишний проход по таблице,
// которая к этому моменту уже выросла (§91).
func WithHousekeepingLock(l CycleLock) HousekeepingOption {
	return func(h *Housekeeping) { h.lock = l }
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
	if !h.acquireCycle(ctx) {
		return
	}
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

// acquireCycle — можно ли этой реплике выполнять цикл (§93.7).
//
// Ошибку Redis трактуем как запрет: пропущенная чистка аудита откладывает
// удаление на сутки, а выполненная дважды — это второй полный проход DELETE по
// растущей таблице ровно в тот момент, когда Redis и без того нездоров.
func (h *Housekeeping) acquireCycle(ctx context.Context) bool {
	if h.lock == nil {
		return true
	}
	ttl := max(h.interval/2, time.Minute)
	ok, err := h.lock.TryLock(ctx, ttl)
	if err != nil {
		h.logger.Warn("housekeeping: cycle lock check failed, skipping cycle", h.logger.Err(err))
		return false
	}
	if !ok {
		h.logger.Debug("housekeeping: cycle already taken by another replica")
	}
	return ok
}
