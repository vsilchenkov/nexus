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

// fetchedReader — что экземпляр уже получил из партиций (§114.2).
// Реализуется *readerSlot.
type fetchedReader interface {
	FetchedNext(partition int) (int64, bool)
}

// stallTarget — экземпляр группы глазами сторожа. Реализуется *readerSlot.
type stallTarget interface {
	fetchedReader
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

// readUpTo — экземпляр получил всё, что брокер знал о партициях parts:
// позиция следующего сообщения не меньше high watermark снимка (§114.2).
// Тогда стоит не чтение, а сохранение позиции (брокер отвергает commit), и
// перезапуск дал бы только повторную доставку. Пустой parts — не дочитал:
// сравнивать не с чем.
func readUpTo(r fetchedReader, parts []kafkapf.PartitionPosition) bool {
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		next, ok := r.FetchedNext(p.Partition)
		if !ok || next < p.Last {
			return false
		}
	}
	return true
}

// StallGuard — настройки сторожа зависшего consumer'а (§113.6) для групп
// Sender'а. After ≤ 0 или Inspector == nil — сторож выключен.
type StallGuard struct {
	Inspector groupInspector
	After     time.Duration
	// ClientPrefix — уникальная на процесс часть client.id reader'ов (имя
	// реплики): участники разных реплик в одной группе не должны совпадать.
	ClientPrefix string
	Metrics      restartRecorder // nil — без метрики
	// Commits — счётчик отвергнутых commit'ов (§114.3). Слоты reader'ов
	// считают их всегда, даже при выключенном стороже; nil — без метрики.
	Commits commitRecorder
	Clock   clock.Clock // nil — системные часы
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
	// lastStuckWarn — когда последний раз предупреждали о дочитавшем экземпляре
	// со стоящим commit (§114.2): Warn не чаще раза в stallCooldown.
	lastStuckWarn time.Time
}

// stallVictim — свой экземпляр, ждущий в fetch, и его стоящие партиции.
type stallVictim struct {
	target stallTarget
	parts  []kafkapf.PartitionPosition // стоящие партиции, назначенные ему
	orphan bool                        // стоит партиция, не назначенная никому
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
	victims := w.dropCaughtUp(w.stalledSlots(ctx, stalled, now), now)
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
func (w *stallWatchdog) stalledPartitions(pos []kafkapf.PartitionPosition, now time.Time) map[int]kafkapf.PartitionPosition {
	stalled := map[int]kafkapf.PartitionPosition{}
	for _, p := range pos {
		prev, ok := w.progress[p.Partition]
		if !ok || prev.committed != p.Committed || p.Lag() == 0 {
			w.progress[p.Partition] = partitionProgress{committed: p.Committed, since: now}
			continue
		}
		if now.Sub(prev.since) >= w.guard.After {
			stalled[p.Partition] = p
		}
	}
	return stalled
}

// stalledSlots — свои экземпляры, которые сами ждут в fetch ≥ After и которым
// назначена стоящая партиция. Стоящая партиция, не назначенная никому (группа
// застряла в ребалансе), — повод для всех таких экземпляров.
func (w *stallWatchdog) stalledSlots(ctx context.Context, stalled map[int]kafkapf.PartitionPosition, now time.Time) []stallVictim {
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
	var out []stallVictim
	for _, s := range w.slots {
		parts := ownStalled(assigned[s.ClientID()], stalled)
		ws := s.WaitingSince()
		if (orphan || len(parts) > 0) && !ws.IsZero() && now.Sub(ws) >= w.guard.After {
			out = append(out, stallVictim{target: s, parts: parts, orphan: orphan})
		}
	}
	if len(out) == 0 {
		// §51.9: стоящие партиции чужие (другая реплика) или свой экземпляр занят.
		w.logger.Debug("kafka stall watchdog: stalled partitions not owned by an idle local reader",
			w.logger.Str("group", w.group), w.logger.Any("stalled", lagByPartition(stalled)))
	}
	return out
}

// ownStalled — стоящие партиции из назначенных экземпляру, по возрастанию.
func ownStalled(assigned []int, stalled map[int]kafkapf.PartitionPosition) []kafkapf.PartitionPosition {
	var out []kafkapf.PartitionPosition
	for _, p := range assigned {
		if pos, ok := stalled[p]; ok {
			out = append(out, pos)
		}
	}
	slices.SortFunc(out, func(a, b kafkapf.PartitionPosition) int { return a.Partition - b.Partition })
	return out
}

// dropCaughtUp убирает из жертв экземпляры, которые всё прочитали (§114.2): у
// них стоит не чтение, а commit — брокер отвергает сохранение позиции.
// Перезапуск перечитал бы всё после последнего удачного commit (дубли у
// приёмника), а при недоступном __consumer_offsets новый участник не вошёл бы
// в группу — ребаланс пишет туда же. Ничья партиция дочитанной не считается:
// её лечит как раз перезапуск.
func (w *stallWatchdog) dropCaughtUp(victims []stallVictim, now time.Time) []stallVictim {
	var out []stallVictim
	var stuck []string
	for _, v := range victims {
		if !v.orphan && readUpTo(v.target, v.parts) {
			stuck = append(stuck, v.target.ClientID())
			continue
		}
		out = append(out, v)
	}
	if len(stuck) > 0 {
		w.reportCommitStuck(stuck, now)
	}
	return out
}

// reportCommitStuck — Warn о дочитавших экземплярах со стоящим commit не чаще
// раза в stallCooldown на группу, Debug — на каждой проверке.
func (w *stallWatchdog) reportCommitStuck(clientIDs []string, now time.Time) {
	group, topic := w.logger.Str("group", w.group), w.logger.Str("topic", w.topic)
	ids := w.logger.Any("client_ids", clientIDs)
	if !w.lastStuckWarn.IsZero() && now.Sub(w.lastStuckWarn) < stallCooldown {
		w.logger.Debug("kafka stall watchdog: commit stuck, reader caught up; not restarting", group, topic, ids)
		return
	}
	w.lastStuckWarn = now
	w.logger.Warn("kafka commit stuck: reader caught up but offsets do not advance; not restarting (see nexus_kafka_commit_errors_total)",
		group, topic, ids, w.logger.Str("stall_after", w.guard.After.String()))
}

// restart перезапускает зависшие экземпляры и сбрасывает отсчёт их партиций.
func (w *stallWatchdog) restart(victims []stallVictim, stalled map[int]kafkapf.PartitionPosition, now time.Time) {
	restarted := false
	for _, v := range victims {
		id := v.target.ClientID()
		if !v.target.RequestRestart() {
			continue // успел получить сообщение — уже не завис
		}
		w.logger.Warn("kafka consumer stalled: recreating reader",
			w.logger.Str("group", w.group),
			w.logger.Str("topic", w.topic),
			w.logger.Str("client_id", id),
			w.logger.Any("stalled_partitions", lagByPartition(stalled)),
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

// lagByPartition — партиция → lag, для логов.
func lagByPartition(pos map[int]kafkapf.PartitionPosition) map[int]int64 {
	out := make(map[int]int64, len(pos))
	for p, v := range pos {
		out[p] = v.Lag()
	}
	return out
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

// ownOrOrphanLagging — партиции с lag, назначенные clientID или никому;
// orphan=true, если среди них есть ничья.
func ownOrOrphanLagging(pos []kafkapf.PartitionPosition, assigned map[string][]int, clientID string) ([]kafkapf.PartitionPosition, bool) {
	owners := partitionOwners(assigned)
	var out []kafkapf.PartitionPosition
	orphan := false
	for _, p := range pos {
		if p.Lag() == 0 {
			continue
		}
		owner, ok := owners[p.Partition]
		switch {
		case !ok:
			orphan = true
			out = append(out, p)
		case owner == clientID:
			out = append(out, p)
		}
	}
	return out, orphan
}

// sumLag — суммарный lag партиций, для логов.
func sumLag(pos []kafkapf.PartitionPosition) int64 {
	var lag int64
	for _, p := range pos {
		lag += p.Lag()
	}
	return lag
}
