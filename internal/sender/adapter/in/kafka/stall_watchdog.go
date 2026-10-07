package kafka

import (
	"context"
	"slices"
	"time"

	"nexus/internal/platform/clock"
	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// groupInspector — позиции и назначения группы из брокера. Реализуется
// *kafkapf.Offsets.
type groupInspector interface {
	GroupPositions(ctx context.Context, group, topic string) ([]kafkapf.PartitionPosition, error)
	GroupAssignment(ctx context.Context, group, topic string) (map[string][]int, error)
}

// stallTarget — экземпляр группы глазами сторожа. Реализуется *readerSlot.
type stallTarget interface {
	ClientID() string
	WaitingSince() time.Time
	RequestRestart() bool
}

// restartRecorder считает перезапуски reader'ов (nexus_kafka_consumer_restarts_total).
type restartRecorder interface {
	IncKafkaConsumerRestart(group, reason string)
}

// Стратегия сторожа (§113.6).
const (
	stallCheckEvery = 30 * time.Second
	stallCooldown   = 5 * time.Minute
	// stallReasonFetch — экземпляр ждёт в FetchMessage, а его партиции стоят.
	stallReasonFetch = "stalled"
)

// StallGuard — настройки сторожа зависшего consumer'а (§113.6) для групп
// Sender'а. After ≤ 0 или Inspector == nil — сторож выключен.
type StallGuard struct {
	Inspector groupInspector
	After     time.Duration
	// ClientPrefix — уникальная на процесс часть client.id reader'ов (имя
	// реплики): участники разных реплик в одной группе не должны совпадать.
	ClientPrefix string
	Metrics      restartRecorder // nil — без метрики
	Clock        clock.Clock     // nil — системные часы
	// CheckEvery — период проверки непрерывной группы; ≤ 0 → 30 с. Меньше
	// задают только integration-тесты.
	CheckEvery time.Duration
}

func (g StallGuard) enabled() bool { return g.After > 0 && g.Inspector != nil }

func (g StallGuard) clk() clock.Clock {
	if g.Clock == nil {
		return clock.System()
	}
	return g.Clock
}

// partitionProgress — с какого момента партиция стоит: lag > 0, committed не
// меняется.
type partitionProgress struct {
	committed int64
	since     time.Time
}

// stallWatchdog — сторож непрерывной группы (основной async, clog-retry).
//
// После рестарта брокера kafka-go иногда остаётся в группе (heartbeat идёт,
// партиции назначены), а FetchMessage больше не возвращается; до §113 это
// лечилось только рестартом Sender'а. Сторож перезапускает reader экземпляра,
// у которого ОДНОВРЕМЕННО: на назначенных ему партициях lag > 0 и committed
// не двигается ≥ After, а сам он ≥ After ждёт в FetchMessage. Занятый
// обработкой экземпляр (долгая отправка, retry-in-place) не ждёт в fetch и не
// трогается; чужие партиции (другая реплика) — тоже: назначение берётся из
// DescribeGroups по client.id.
type stallWatchdog struct {
	group, topic string
	guard        StallGuard
	slots        []stallTarget
	clock        clock.Clock
	logger       logging.Logger

	progress    map[int]partitionProgress
	lastRestart time.Time
}

func newStallWatchdog(group, topic string, guard StallGuard, slots []stallTarget, logger logging.Logger) *stallWatchdog {
	return &stallWatchdog{
		group: group, topic: topic, guard: guard, slots: slots,
		clock: guard.clk(), logger: logger, progress: map[int]partitionProgress{},
	}
}

// Run проверяет группу раз в CheckEvery (по умолчанию stallCheckEvery) до
// отмены ctx.
func (w *stallWatchdog) Run(ctx context.Context) {
	every := w.guard.CheckEvery
	if every <= 0 {
		every = stallCheckEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.Check(ctx)
		}
	}
}

// Check — одна проверка: обновить прогресс партиций и перезапустить
// зависшие экземпляры.
func (w *stallWatchdog) Check(ctx context.Context) {
	pos, err := w.guard.Inspector.GroupPositions(ctx, w.group, w.topic)
	if err != nil {
		// Неполная картина — не решаем: сторож не должен перезапускать по догадке.
		w.logger.Debug("kafka stall watchdog: positions unavailable",
			w.logger.Str("group", w.group), w.logger.Err(err))
		return
	}
	now := w.clock.Now()
	stalled := w.stalledPartitions(pos, now)
	if len(stalled) == 0 {
		return
	}
	victims := w.stalledSlots(ctx, stalled, now)
	if len(victims) == 0 {
		return
	}
	if !w.lastRestart.IsZero() && now.Sub(w.lastRestart) < stallCooldown {
		w.logger.Debug("kafka stall watchdog: restart suppressed by cooldown",
			w.logger.Str("group", w.group), w.logger.Str("last", w.lastRestart.String()))
		return
	}
	w.restart(victims, stalled, now)
}

// stalledPartitions обновляет прогресс и возвращает партиции, стоящие ≥ After.
func (w *stallWatchdog) stalledPartitions(pos []kafkapf.PartitionPosition, now time.Time) map[int]int64 {
	stalled := map[int]int64{}
	for _, p := range pos {
		prev, ok := w.progress[p.Partition]
		if !ok || prev.committed != p.Committed || p.Lag() == 0 {
			w.progress[p.Partition] = partitionProgress{committed: p.Committed, since: now}
			continue
		}
		if now.Sub(prev.since) >= w.guard.After {
			stalled[p.Partition] = p.Lag()
		}
	}
	return stalled
}

// stalledSlots — свои экземпляры, которые сами ждут в fetch ≥ After и которым
// назначена стоящая партиция. Стоящая партиция, не назначенная никому (группа
// застряла в ребалансе), — повод для всех таких экземпляров.
func (w *stallWatchdog) stalledSlots(ctx context.Context, stalled map[int]int64, now time.Time) []stallTarget {
	assigned, err := w.guard.Inspector.GroupAssignment(ctx, w.group, w.topic)
	if err != nil {
		w.logger.Debug("kafka stall watchdog: assignment unavailable",
			w.logger.Str("group", w.group), w.logger.Err(err))
		return nil
	}
	owners := partitionOwners(assigned)
	orphan := false
	for p := range stalled {
		if _, ok := owners[p]; !ok {
			orphan = true
		}
	}
	var out []stallTarget
	for _, s := range w.slots {
		owns := orphan || slices.ContainsFunc(assigned[s.ClientID()], func(p int) bool { _, ok := stalled[p]; return ok })
		ws := s.WaitingSince()
		if owns && !ws.IsZero() && now.Sub(ws) >= w.guard.After {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		// §51.9: стоящие партиции чужие (другая реплика) или свой экземпляр занят.
		w.logger.Debug("kafka stall watchdog: stalled partitions not owned by an idle local reader",
			w.logger.Str("group", w.group), w.logger.Any("stalled", stalled))
	}
	return out
}

// restart перезапускает зависшие экземпляры и сбрасывает отсчёт их партиций.
func (w *stallWatchdog) restart(victims []stallTarget, stalled map[int]int64, now time.Time) {
	restarted := false
	for _, s := range victims {
		id := s.ClientID()
		if !s.RequestRestart() {
			continue // успел получить сообщение — уже не завис
		}
		w.logger.Warn("kafka consumer stalled: recreating reader",
			w.logger.Str("group", w.group),
			w.logger.Str("topic", w.topic),
			w.logger.Str("client_id", id),
			w.logger.Any("stalled_partitions", stalled),
			w.logger.Str("stall_after", w.guard.After.String()))
		if w.guard.Metrics != nil {
			w.guard.Metrics.IncKafkaConsumerRestart(w.group, stallReasonFetch)
		}
		restarted = true
	}
	if !restarted {
		return
	}
	w.lastRestart = now
	for p := range stalled {
		w.progress[p] = partitionProgress{committed: w.progress[p].committed, since: now}
	}
}

// partitionOwners — партиция → client.id участника, которому она назначена.
func partitionOwners(assigned map[string][]int) map[int]string {
	out := map[int]string{}
	for client, parts := range assigned {
		for _, p := range parts {
			out[p] = client
		}
	}
	return out
}

// ownOrOrphanLag — lag партиций, назначенных clientID или никому.
func ownOrOrphanLag(pos []kafkapf.PartitionPosition, assigned map[string][]int, clientID string) int64 {
	owners := partitionOwners(assigned)
	var lag int64
	for _, p := range pos {
		if owner, ok := owners[p.Partition]; !ok || owner == clientID {
			lag += p.Lag()
		}
	}
	return lag
}
