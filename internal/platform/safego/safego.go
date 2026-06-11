// Package safego — defer-friendly хелперы для перехвата паник в горутинах.
//
// Сервис не должен падать от паники в фоновой задаче (§30 ТЗ). Каждая горутина
// в продакшн-коде первой строкой ставит `defer safego.Recover(logger, "op")`:
// паника гасится, логируется как error и капчурится в Sentry, а процесс
// продолжает работу.
package safego

import (
	"context"
	"runtime/debug"

	"nexus/internal/platform/logging"
)

// Recover перехватывает панику текущей горутины, логирует её как error и
// возвращает управление (re-panic не делает). Предназначен для defer в начале
// тела горутины:
//
//	go func() {
//		defer safego.Recover(logger, "web.listenAndServe")
//		// ...
//	}()
//
// logger.Error в v1.7.9 сам отправляет событие в Sentry (sentryslog-хендлер,
// EventLevel включает Error). У фоновой горутины нет request-scoped hub'а, так
// что событие уходит в глобальный CurrentHub — это корректно. Stacktrace точки
// паники кладётся атрибутом stack.
func Recover(logger logging.Logger, op string) {
	r := recover()
	if r == nil {
		return
	}
	logger.Error("panic recovered in goroutine",
		logger.Any("panic", r),
		logger.Str("op", op),
		logger.Str("stack", string(debug.Stack())),
	)
}

// Go запускает fn в горутине с panic-recovery и возвращает канал, который
// закрывается по её завершении. Используется для фоновых задач, которые
// graceful shutdown обязан дождаться (Phase AUD.3):
//
//	done := safego.Go(logger, "web.housekeeping", func() { hk.Run(ctx) })
//	// в Stop():
//	safego.Await(stopCtx, done, logger, "web.housekeeping")
func Go(logger logging.Logger, op string, fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover(logger, op)
		fn()
	}()
	return done
}

// Await ждёт закрытия done либо истечения ctx; во втором случае пишет warn —
// фоновая задача не уложилась в shutdown-бюджет (диагностика зависших горутин).
func Await(ctx context.Context, done <-chan struct{}, logger logging.Logger, op string) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
		logger.Warn("background task did not stop within shutdown deadline",
			logger.Str("op", op))
	}
}

// RecoverCtx — вариант Recover для горутин, у которых есть контекст с
// привязанным Sentry-hub (например, отпочкованный от запроса через
// context.WithoutCancel). Через logger.WithContext(ctx) событие попадёт в
// hub из контекста со всеми его тегами; если hub'а нет — в глобальный.
func RecoverCtx(ctx context.Context, logger logging.Logger, op string) {
	r := recover()
	if r == nil {
		return
	}
	logger.WithContext(ctx).Error("panic recovered in goroutine",
		logger.Any("panic", r),
		logger.Str("op", op),
		logger.Str("stack", string(debug.Stack())),
	)
}
