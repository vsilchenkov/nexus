package kafka

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// slotName — постоянная часть client.id экземпляра: "<группа>[-<реплика>]-<номер>".
func slotName(group string, guard StallGuard, idx int) string {
	parts := []string{group}
	if guard.ClientPrefix != "" {
		parts = append(parts, guard.ClientPrefix)
	}
	parts = append(parts, strconv.Itoa(idx))
	return strings.Join(parts, "-")
}

// newSlots создаёт n экземпляров группы; factory строит reader по client.id.
func newSlots(n int, group string, guard StallGuard, factory readerFactory, logger logging.Logger) []*readerSlot {
	n = max(n, 1)
	out := make([]*readerSlot, 0, n)
	for i := range n {
		out = append(out, newReaderSlot(slotName(group, guard, i), factory, guard.clk(), logger))
	}
	return out
}

// startStallWatchdog запускает сторожа непрерывной группы (§113.6), если он
// включён. Горутина учитывается в wg владельца и завершается с ctx.
func startStallWatchdog(ctx context.Context, wg *sync.WaitGroup, group, topic string, guard StallGuard, slots []*readerSlot, logger logging.Logger) {
	if !guard.enabled() {
		logger.Debug("kafka stall watchdog disabled", logger.Str("group", group))
		return
	}
	targets := make([]stallTarget, len(slots))
	for i, s := range slots {
		targets[i] = s
	}
	w := newStallWatchdog(group, topic, guard, targets, logger)
	wg.Go(func() {
		defer safego.Recover(logger, "sender.kafkaStallWatchdog")
		w.Run(ctx)
	})
}

// closeSlots закрывает reader'ы всех экземпляров.
func closeSlots(slots []*readerSlot) {
	for _, s := range slots {
		_ = s.Close() // shutdown: ошибка закрытия reader'а ничего не меняет
	}
}
