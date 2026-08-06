package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ErrBreakerUnavailable — состояние circuit breaker'а недоступно: Redis не
// сконфигурирован в этой инсталляции либо не отвечает.
var ErrBreakerUnavailable = errors.New("usecase: circuit breaker state unavailable")

// BreakerStateResult — ответ на запрос состояния (§81.4.1).
//
// Available=false означает «breaker'а в этой инсталляции нет вообще»: без Redis
// защита не работает и все запросы проходят. Это честный ответ, а не ошибка —
// поэтому чтение в таком случае успешно, а вот сбрасывать нечего (см. Reset).
type BreakerStateResult struct {
	Available  bool
	Exists     bool
	State      string
	Failures   int
	Threshold  int
	OpenedAt   time.Time
	RetryAfter time.Duration
}

// BreakerResetResult — итог ручного сброса (§81.4.2).
type BreakerResetResult struct {
	WasOpen           bool
	PreviousState     string
	PreviousFailures  int
	NodeStatusCleared bool
}

// NodeBreakerUsecase — просмотр и ручной сброс защиты узла (§81.4).
//
// Отдельный usecase, а не метод AsyncQueueUsecase: breaker актуален и для
// sync-узла, у которого очереди нет вовсе, а у того конструктор уже несёт
// двенадцать позиционных зависимостей.
type NodeBreakerUsecase struct {
	breaker port.NodeBreaker        // nil без Redis
	status  port.NodeStatusResetter // nil без Redis
	nodes   port.NodeRepo
	audit   *AuditUsecase
	logger  logging.Logger
}

func NewNodeBreakerUsecase(
	breaker port.NodeBreaker,
	status port.NodeStatusResetter,
	nodes port.NodeRepo,
	audit *AuditUsecase,
	logger logging.Logger,
) *NodeBreakerUsecase {
	return &NodeBreakerUsecase{breaker: breaker, status: status, nodes: nodes, audit: audit, logger: logger}
}

// resolveNode — тот же team-scope контракт, что у остальных операций над узлом:
// чужой узел неотличим от несуществующего (не подтверждаем его существование).
func (u *NodeBreakerUsecase) resolveNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("breaker get node: %w", err)
	}
	if teamID != "" && node.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	return node, nil
}

// State — текущее состояние защиты узла. Доступно всем ролям: наблюдателю тоже
// нужно понимать, почему узел молчит.
func (u *NodeBreakerUsecase) State(ctx context.Context, nodeID, teamID string) (BreakerStateResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return BreakerStateResult{}, err
	}
	if u.breaker == nil {
		// §51.9: тихая деградация — оставляем след, иначе «почему карточки нет»
		// придётся выяснять по конфигурации.
		u.logger.Debug("breaker: state requested but redis is not configured",
			u.logger.Str("node", node.Path))
		return BreakerStateResult{Available: false, State: string(domain.BreakerStateClosed)}, nil
	}

	st, err := u.breaker.State(ctx, node.Path)
	if err != nil {
		// НЕ fail-open: соврать оператору «закрыт», когда состояние неизвестно,
		// хуже, чем честно сказать «не знаю». Fail-open нужен там, где на кону
		// доставка (Allow/IsOpen в Sender'е), а здесь — решение человека.
		u.logger.ErrorWithOp("breaker state read failed", err, "breaker.state",
			u.logger.Str("node", node.Path))
		return BreakerStateResult{}, ErrBreakerUnavailable
	}
	return BreakerStateResult{
		Available:  true,
		Exists:     st.Exists,
		State:      st.State,
		Failures:   st.Failures,
		Threshold:  st.Threshold,
		OpenedAt:   st.OpenedAt,
		RetryAfter: st.RetryAfter,
	}, nil
}

// Reset снимает блокировку узла вручную (§81.4.2): удаляет состояние breaker'а
// и персистентный исход последнего вызова (§52), пишет действие в аудит.
//
// Идемпотентен: сброс закрытого breaker'а — не ошибка, но в аудит попадает
// (оператор действительно нажал кнопку, и при разборе это ценно).
func (u *NodeBreakerUsecase) Reset(ctx context.Context, actor Actor, nodeID, teamID string) (BreakerResetResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return BreakerResetResult{}, err
	}
	if u.breaker == nil {
		return BreakerResetResult{}, ErrBreakerUnavailable
	}

	before, err := u.breaker.Reset(ctx, node.Path)
	if err != nil {
		u.logger.ErrorWithOp("breaker reset failed", err, "breaker.reset",
			u.logger.Str("node", node.Path))
		return BreakerResetResult{}, ErrBreakerUnavailable
	}

	res := BreakerResetResult{
		WasOpen:          before.State == string(domain.BreakerStateOpen),
		PreviousState:    before.State,
		PreviousFailures: before.Failures,
	}

	// Снятие бейджа — best-effort: главное сделано (защита снята), и ронять
	// операцию из-за второстепенной записи нельзя. Оператор видит исход в ответе.
	if u.status != nil {
		if err := u.status.ClearLastOutcome(ctx, node.Path); err != nil {
			u.logger.Warn("breaker reset: node status not cleared",
				u.logger.Str("node", node.Path), u.logger.Err(err))
		} else {
			res.NodeStatusCleared = true
		}
	}

	u.audit.Log(ctx, actor, domain.ActionNodeBreakerReset, "node", node.ID, map[string]any{
		"node_path":           node.Path,
		"was_open":            res.WasOpen,
		"previous_state":      res.PreviousState,
		"previous_failures":   res.PreviousFailures,
		"node_status_cleared": res.NodeStatusCleared,
	})
	return res, nil
}
