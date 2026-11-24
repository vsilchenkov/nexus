package kafka

import (
	"context"
	"sync"
	"time"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// drainAbortGrace — сколько ждать выхода горутин ПОСЛЕ обрыва доставок по
// потолку (§118.8). Отменённый контекст рвёт HTTP и commit сразу; запас нужен
// на возврат из обработчика и запись исхода, а не на саму доставку.
const drainAbortGrace = 5 * time.Second

// deliveryScope — контекст ДОСТАВКИ, отвязанный от остановки ЧТЕНИЯ (§118.8).
//
// До §118 обработка сообщения шла на том же контексте, что и FetchMessage, а
// runner отменяет его первым при остановке: HTTP-запрос к получателю рвался
// посреди отправки, offset не коммитился, и после старта сообщение уходило
// снова — получатель (1С) оборванный вызов обычно дорабатывал, и выходил
// повтор на каждом деплое. Теперь чтение останавливается отменой контекста
// приложения, а доставка в полёте доигрывается до потолка shutdown.timeout_sec
// и обрывается только Abort.
//
// Значения контекста (логгер, трейс) наследуются от родителя через
// context.WithoutCancel — отвязывается только отмена (CLAUDE.md §6).
type deliveryScope struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	aborted bool
}

// begin возвращает контекст доставки и функцию его освобождения. Повторный
// begin (новый Run того же экземпляра) заменяет прежний; после Abort контекст
// выдаётся уже отменённым — остановленный компонент не доставляет.
func (d *deliveryScope) begin(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.aborted {
		cancel()
	}
	d.cancel = cancel
	return ctx, cancel
}

// Abort обрывает доставки в полёте (прежнее поведение остановки). Идемпотентен.
func (d *deliveryScope) Abort() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.aborted = true
	if d.cancel != nil {
		d.cancel()
	}
}

// waitGroupCtx ждёт wg не дольше ctx; true — дождались.
//
// Горутина-ожидатель завершается, когда wg обнулится; вызывающий обязан в
// итоге дождаться wg (после Abort и закрытия reader'ов), иначе она утечёт.
func waitGroupCtx(ctx context.Context, wg *sync.WaitGroup, logger logging.Logger) bool {
	done := make(chan struct{})
	go func() {
		defer safego.Recover(logger, "sender.kafkaDrainWait")
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// waitDone ждёт закрытия done не дольше ctx; true — дождались. nil-канал —
// цикл не запускался, ждать нечего.
func waitDone(ctx context.Context, done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
