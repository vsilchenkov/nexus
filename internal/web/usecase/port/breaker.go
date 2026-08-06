package port

import (
	"context"
	"time"
)

// BreakerState — состояние circuit breaker'а узла для интерфейса (§81.4).
//
// Threshold нулевой означает «политика неизвестна»: состояние записано версией
// до §81.3 либо breaker ещё не считал отказы. Интерфейс в этом случае обязан
// показать счётчик без знаменателя, а не подставлять своё значение — оно может
// отличаться от применённого (политика переопределяется на узле).
type BreakerState struct {
	Exists     bool
	State      string // closed | open | half_open
	Failures   int
	Threshold  int
	OpenedAt   time.Time
	RetryAfter time.Duration
}

// NodeBreaker — состояние и ручной сброс circuit breaker'а узла (§81.4).
//
// Реализуется adapter/out/redis поверх platform/circuitbreaker: состояние
// пишет ДРУГОЙ процесс (Sender), Web читает и сбрасывает его в том же Redis —
// прецедент кросс-процессного доступа тот же, что у queuecancel и nodestatus.
//
// Порт опционален: без Redis реализации нет, и usecase отвечает
// ErrBreakerUnavailable.
type NodeBreaker interface {
	State(ctx context.Context, nodePath string) (BreakerState, error)
	// Reset возвращает состояние ДО сброса — оно уходит в аудит (§81.4.2).
	Reset(ctx context.Context, nodePath string) (BreakerState, error)
}

// NodeStatusResetter — снятие персистентного исхода последнего вызова узла
// (§52) при ручном сбросе breaker'а (§81.4.2).
//
// Отдельный порт от NodeStatusReader: у чтения бейджа и у его снятия разные
// потребители, а интерфейс должен перечислять только то, что вызывающий
// действительно зовёт (CLAUDE.md §2, ISP).
type NodeStatusResetter interface {
	ClearLastOutcome(ctx context.Context, nodePath string) error
}
