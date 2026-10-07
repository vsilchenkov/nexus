package kafka

import (
	"context"
	"time"

	"nexus/internal/platform/clock"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// passRecreator — проходной reader глазами сторожа. Реализуется *readerSlot.
type passRecreator interface {
	fetchedReader
	ClientID() string
	Recreate(reason string)
}

const (
	// passIdleLimit — сколько проходов подряд без единого сообщения терпим,
	// прежде чем спросить брокер: после ребаланса reader несколько секунд
	// получает назначение, и проход с fetchTimeout 2 с может закончиться пустым.
	passIdleLimit = 3
	// stallReasonPassIdle — проходы пусты, а на партициях reader'а есть lag.
	stallReasonPassIdle = "pass_idle"
)

// passStallGuard — сторож проходного reader'а (§113.6): sweeper паузы и
// DLQ-репроцессор не висят в FetchMessage (ожидание ограничено таймаутом
// прохода), поэтому зависший reader у них выглядит иначе — проходы
// заканчиваются по таймауту пустыми, а очередь стоит.
//
// После passIdleLimit пустых проходов подряд сторож смотрит в брокер: если
// lag есть на партициях этого reader'а или на партициях, не назначенных никому
// (группа застряла в ребалансе), reader пересоздаётся. Партиции другой реплики
// поводом не служат — у неё свой сторож.
//
// §114.2: reader, получивший всё до конца лога, не пересоздаётся — lag у него
// означает отвергнутый commit, а не зависание. Между пересозданиями — пауза
// stallCooldown, как у непрерывных групп: до §114 пересоздание шло каждые
// passIdleLimit пустых проходов.
type passStallGuard struct {
	group, topic string
	inspector    groupInspector
	slot         passRecreator
	metrics      restartRecorder // nil — без метрики
	clock        clock.Clock
	logger       logging.Logger
	idle         int

	lastRecreate  time.Time
	lastStuckWarn time.Time
}

// newPassStallGuard возвращает nil, если сторож выключен (guard.After ≤ 0).
func newPassStallGuard(group, topic string, guard StallGuard, slot passRecreator, logger logging.Logger) *passStallGuard {
	if !guard.enabled() {
		return nil
	}
	return &passStallGuard{group: group, topic: topic, inspector: guard.Inspector,
		slot: slot, metrics: guard.Metrics, clock: guard.clk(), logger: logger}
}

// PassDone учитывает итог прохода; nil-получатель — сторож выключен.
func (g *passStallGuard) PassDone(ctx context.Context, processed int) {
	if g == nil {
		return
	}
	if processed > 0 {
		g.idle = 0
		return
	}
	g.idle++
	if g.idle < passIdleLimit {
		return
	}
	g.idle = 0
	lagging, orphan, err := g.laggingPartitions(ctx)
	if err != nil {
		g.logger.Debug("kafka pass stall guard: broker state unavailable",
			g.logger.Str("group", g.group), g.logger.Err(err))
		return
	}
	if len(lagging) == 0 {
		return
	}
	if !orphan && readUpTo(g.slot, lagging) {
		g.reportCommitStuck()
		return
	}
	g.recreate(lagging)
}

// recreate пересоздаёт reader, если с прошлого пересоздания прошло не меньше
// stallCooldown.
func (g *passStallGuard) recreate(lagging []kafkapf.PartitionPosition) {
	now := g.clock.Now()
	if !g.lastRecreate.IsZero() && now.Sub(g.lastRecreate) < stallCooldown {
		g.logger.Debug("kafka pass stall guard: recreate suppressed by cooldown",
			g.logger.Str("group", g.group), g.logger.Str("last", g.lastRecreate.String()))
		return
	}
	g.lastRecreate = now
	g.logger.Warn("kafka pass reader reads nothing while its partitions lag: recreating reader",
		g.logger.Str("group", g.group),
		g.logger.Str("topic", g.topic),
		g.logger.Str("client_id", g.slot.ClientID()),
		g.logger.Any("lag", sumLag(lagging)),
		g.logger.Int("empty_passes", passIdleLimit))
	g.slot.Recreate(stallReasonPassIdle)
	if g.metrics != nil {
		g.metrics.IncKafkaConsumerRestart(g.group, stallReasonPassIdle)
	}
}

// reportCommitStuck — Warn о дочитавшем reader'е со стоящим commit не чаще
// раза в stallCooldown, Debug — в остальные разы.
func (g *passStallGuard) reportCommitStuck() {
	now := g.clock.Now()
	group, id := g.logger.Str("group", g.group), g.logger.Str("client_id", g.slot.ClientID())
	if !g.lastStuckWarn.IsZero() && now.Sub(g.lastStuckWarn) < stallCooldown {
		g.logger.Debug("kafka pass stall guard: commit stuck, reader caught up; not recreating", group, id)
		return
	}
	g.lastStuckWarn = now
	g.logger.Warn("kafka commit stuck: pass reader caught up but offsets do not advance; not recreating (see nexus_kafka_commit_errors_total)",
		group, g.logger.Str("topic", g.topic), id)
}

// laggingPartitions — партиции с lag, которые должен читать этот reader:
// назначенные ему и не назначенные никому (orphan=true, если такие есть).
func (g *passStallGuard) laggingPartitions(ctx context.Context) ([]kafkapf.PartitionPosition, bool, error) {
	pos, err := g.inspector.GroupPositions(ctx, g.group, g.topic)
	if err != nil {
		return nil, false, err
	}
	assigned, err := g.inspector.GroupAssignment(ctx, g.group, g.topic)
	if err != nil {
		return nil, false, err
	}
	lagging, orphan := ownOrOrphanLagging(pos, assigned, g.slot.ClientID())
	return lagging, orphan, nil
}
