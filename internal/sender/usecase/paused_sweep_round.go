package usecase

import (
	"context"
	"time"
)

// continueRound — тик внутри активного раунда: снять цель, проверить
// завершение или продвижение. Офсеты недоступны — проход на каждом тике, как
// до §113: лишняя перезапись лучше застрявшей доставки.
func (p *PausedSweepPlanner) continueRound(ctx context.Context, now time.Time) PausedSweepDecision {
	r := p.round
	run := PausedSweepDecision{Run: true, Reason: PausedSweepRound}
	pos, err := p.offsets.Positions(ctx)
	if err != nil {
		p.warnOffsets(err, now)
		return run
	}
	if r.target == nil {
		r.target = roundTarget(pos)
		p.logger.Debug("paused sweep: round target captured", p.logger.Int("partitions", len(r.target)))
		return run
	}
	undone := r.undone(pos)
	if len(undone) == 0 {
		p.logger.Debug("paused sweep: round complete",
			p.logger.Str("duration", now.Sub(r.startedAt).String()))
		p.round = nil
		return PausedSweepDecision{Reason: PausedSweepSkip}
	}
	switch {
	case r.progressed(undone):
		r.stalled = 0
	case p.lastPassProcessed > 0:
		// Свой проход переписал сообщения, а незавершённые партиции стоят:
		// крутим свои партиции вхолостую.
		r.stalled++
	default:
		// Свой проход ничего не прочитал (сбой брокера, нагрузка) — он ничего и
		// не переписал. Бросить раунд значило бы отложить снятие паузы или
		// отмену до backstop; повторять дёшево.
	}
	r.reached = undone
	if r.stalled >= roundStallTicks {
		p.abandonRound("round partitions are not progressing", now.Sub(r.startedAt), len(undone))
		return PausedSweepDecision{Reason: PausedSweepSkip}
	}
	return run
}

// abandonRound бросает раунд, в котором незавершённые партиции не
// продвигаются, хотя свои проходы переписывают сообщения: эти партиции не
// разбирает ни одна реплика (реплика без Redis не узнала о раунде, её reader
// завис). Иначе свои партиции переписывались бы каждый тик до backstop.
// Следующий раунд — по событию или backstop.
func (p *PausedSweepPlanner) abandonRound(why string, age time.Duration, undone int) {
	p.logger.Warn("paused sweep: "+why+", round abandoned",
		p.logger.Str("op", "sender.pausedSweep.round"),
		p.logger.Str("age", age.String()),
		p.logger.Int("undone_partitions", undone),
		p.logger.Str("backstop", p.backstop.String()))
	p.round = nil
}

// warnOffsets — офсеты delay-топика недоступны: Warn не чаще offsetsWarnEvery,
// остальное в Debug.
func (p *PausedSweepPlanner) warnOffsets(err error, now time.Time) {
	if p.offsetsWarnAt.IsZero() || now.Sub(p.offsetsWarnAt) >= offsetsWarnEvery {
		p.offsetsWarnAt = now
		p.logger.Warn("paused sweep: topic offsets unavailable, sweeping every tick until they return",
			p.logger.Str("op", "sender.pausedSweep.offsets"), p.logger.Err(err))
		return
	}
	p.logger.Debug("paused sweep: topic offsets unavailable", p.logger.Err(err))
}

// refreshBackstop пересчитывает эффективный backstop из retention топика не
// чаще retentionRefresh. Брокер не ответил — retention из конфига.
func (p *PausedSweepPlanner) refreshBackstop(ctx context.Context, now time.Time) {
	if !p.retentionAt.IsZero() && now.Sub(p.retentionAt) < retentionRefresh {
		return
	}
	p.retentionAt = now
	retention, err := p.offsets.Retention(ctx)
	if err != nil {
		p.logger.Debug("paused sweep: topic retention unavailable, using kafka.topic.retention_ms",
			p.logger.Err(err), p.logger.Str("fallback", p.cfg.FallbackRetention.String()))
		retention = p.cfg.FallbackRetention
	}
	next := effectiveBackstop(p.cfg.Backstop, retention)
	if next != p.backstop {
		p.logger.Info("paused sweep: effective backstop changed",
			p.logger.Str("backstop", next.String()),
			p.logger.Str("configured", p.cfg.Backstop.String()),
			p.logger.Str("retention", retention.String()))
	}
	p.backstop = next
}

// effectiveBackstop — min(backstop, retention/4). Сообщение на паузе
// переписывается не реже раза в backstop; четверть retention — запас, чтобы
// оно не протухло, даже если пара раундов сорвётся. retention ≤ 0 — хранение
// без ограничения по времени.
func effectiveBackstop(backstop, retention time.Duration) time.Duration {
	if retention > 0 {
		return min(backstop, retention/4)
	}
	return backstop
}

// roundTarget — цель раунда: текущий конец каждой партиции.
func roundTarget(pos []PausedPartition) map[int]int64 {
	t := make(map[int]int64, len(pos))
	for _, pp := range pos {
		t[pp.Partition] = pp.Last
	}
	return t
}

// undone — партиции цели, до которой группа ещё не дочитала, с текущей
// позицией. Позиция — committed, но не левее First (retention срезал хвост);
// партиция, пропавшая из ответа, считается недочитанной.
func (r *sweepRound) undone(pos []PausedPartition) map[int]int64 {
	cur := make(map[int]int64, len(pos))
	for _, pp := range pos {
		cur[pp.Partition] = max(pp.Committed, pp.First)
	}
	out := make(map[int]int64)
	for part, target := range r.target {
		c, ok := cur[part]
		if !ok {
			out[part] = -1
			continue
		}
		if c < target {
			out[part] = c
		}
	}
	return out
}

// progressed — продвинулась ли хоть одна недочитанная партиция с прошлого тика.
// Первая проверка после снятия цели считается продвижением.
func (r *sweepRound) progressed(undone map[int]int64) bool {
	if r.reached == nil {
		return true
	}
	for part, c := range undone {
		if prev, ok := r.reached[part]; !ok || c > prev {
			return true
		}
	}
	return false
}
