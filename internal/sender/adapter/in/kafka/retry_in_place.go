package kafka

import (
	"context"
	"time"
)

// Паузы повтора одного сообщения в проходных reader'ах (sweeper паузы, DLQ-
// репроцессор, §113.2): сбой PostgreSQL или Kafka обычно длится секунды, но
// может затянуться — удвоение до потолка не долбит лежащую зависимость.
const (
	passRetryMin = time.Second
	passRetryMax = 30 * time.Second
)

// inPlaceRetry — повтор обработки ОДНОГО прочитанного сообщения, пока исход не
// станет финальным (§113.2).
//
// Почему не «пропустить и перечитать позже»: kafka-go FetchMessage сдвигает
// курсор reader'а сразу, незакоммиченное сообщение тому же reader'у не
// передоставляется (только после rebalance/рестарта), а commit следующего
// сообщения той же партиции переносит committed offset за пропущенное — оно
// теряется. Единственный безопасный ответ на транзиентный сбой — повторить то же
// сообщение на месте (тот же приём, что у основного consumer'а).
type inPlaceRetry struct {
	min, max time.Duration
}

// run вызывает handle, пока тот не вернёт true (финальный исход). Между
// попытками — пауза от min с удвоением до max; onRetry (может быть nil) получает
// номер неудачной попытки и предстоящую паузу. Возвращает false, если ctx
// отменён раньше финального исхода: сообщение остаётся незакоммиченным, и после
// рестарта его перечитают с committed offset.
func (r inPlaceRetry) run(ctx context.Context, handle func() bool, onRetry func(attempt int, wait time.Duration)) bool {
	wait := max(r.min, time.Millisecond)
	for attempt := 1; ; attempt++ {
		if handle() {
			return true
		}
		if onRetry != nil {
			onRetry(attempt, wait)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
		wait = min(wait*2, max(r.max, wait))
	}
}
