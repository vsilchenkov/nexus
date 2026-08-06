package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/circuitbreaker"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeBreakerRedis реализует port.NodeBreaker (§81.4): читает и сбрасывает
// состояние circuit breaker'а узла в том же Redis, куда его пишет Sender.
//
// Формат ключа не дублируется — берётся из circuitbreaker.Key, как у
// nodestatus.Key для §52. Разойдись он — интерфейс показывал бы «закрыт» над
// открытым breaker'ом, и заметить это было бы нечем.
type NodeBreakerRedis struct {
	admin  *circuitbreaker.Admin
	logger logging.Logger
}

var _ port.NodeBreaker = (*NodeBreakerRedis)(nil)

func NewNodeBreakerRedis(client *goredis.Client, logger logging.Logger) *NodeBreakerRedis {
	return &NodeBreakerRedis{admin: circuitbreaker.NewAdmin(client), logger: logger}
}

// State — снимок без побочных эффектов: половинчато-открытая проба не
// расходуется (иначе открытая вкладка UI съедала бы пробы боевого трафика).
func (r *NodeBreakerRedis) State(ctx context.Context, nodePath string) (port.BreakerState, error) {
	snap, err := r.admin.Snapshot(ctx, nodePath)
	if err != nil {
		return port.BreakerState{}, err
	}
	return toPortState(snap), nil
}

// Reset снимает блокировку и возвращает состояние ДО сброса — для аудита.
func (r *NodeBreakerRedis) Reset(ctx context.Context, nodePath string) (port.BreakerState, error) {
	snap, err := r.admin.Reset(ctx, nodePath)
	if err != nil {
		return port.BreakerState{}, err
	}
	r.logger.Debug("breaker: state reset",
		r.logger.Str("node", nodePath),
		r.logger.Str("previous_state", string(snap.State)),
		r.logger.Int("previous_failures", snap.Failures))
	return toPortState(snap), nil
}

func toPortState(s circuitbreaker.Snapshot) port.BreakerState {
	return port.BreakerState{
		Exists:     s.Exists,
		State:      string(s.State),
		Failures:   s.Failures,
		Threshold:  s.Threshold,
		OpenedAt:   s.OpenedAt,
		RetryAfter: s.RetryAfter,
	}
}
