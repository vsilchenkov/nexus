package kafka

import (
	"context"

	"nexus/internal/platform/logging"
)

// passRecreator — проходной reader глазами сторожа. Реализуется *readerSlot.
type passRecreator interface {
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
type passStallGuard struct {
	group, topic string
	inspector    groupInspector
	slot         passRecreator
	metrics      restartRecorder // nil — без метрики
	logger       logging.Logger
	idle         int
}

// newPassStallGuard возвращает nil, если сторож выключен (guard.After ≤ 0).
func newPassStallGuard(group, topic string, guard StallGuard, slot passRecreator, logger logging.Logger) *passStallGuard {
	if !guard.enabled() {
		return nil
	}
	return &passStallGuard{group: group, topic: topic, inspector: guard.Inspector,
		slot: slot, metrics: guard.Metrics, logger: logger}
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
	lag, err := g.stuckLag(ctx)
	if err != nil {
		g.logger.Debug("kafka pass stall guard: broker state unavailable",
			g.logger.Str("group", g.group), g.logger.Err(err))
		return
	}
	if lag == 0 {
		return
	}
	g.logger.Warn("kafka pass reader reads nothing while its partitions lag: recreating reader",
		g.logger.Str("group", g.group),
		g.logger.Str("topic", g.topic),
		g.logger.Str("client_id", g.slot.ClientID()),
		g.logger.Any("lag", lag),
		g.logger.Int("empty_passes", passIdleLimit))
	g.slot.Recreate(stallReasonPassIdle)
	if g.metrics != nil {
		g.metrics.IncKafkaConsumerRestart(g.group, stallReasonPassIdle)
	}
}

// stuckLag — lag партиций, которые должен читать этот reader: назначенных ему
// и не назначенных никому.
func (g *passStallGuard) stuckLag(ctx context.Context) (int64, error) {
	pos, err := g.inspector.GroupPositions(ctx, g.group, g.topic)
	if err != nil {
		return 0, err
	}
	assigned, err := g.inspector.GroupAssignment(ctx, g.group, g.topic)
	if err != nil {
		return 0, err
	}
	return ownOrOrphanLag(pos, assigned, g.slot.ClientID()), nil
}
