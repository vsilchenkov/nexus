package usecase

import (
	"context"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeRuntimeResult — runtime-состояние узла для шапки страницы (§84.7).
//
// Available=false означает «источников исхода в этой инсталляции нет или они
// не ответили». Это НЕ то же самое, что ok, и подменять одно другим нельзя:
// зелёная плашка при неизвестном состоянии — ложь в сторону успокоения, а
// именно она дороже всего стоит в аварии.
type NodeRuntimeResult struct {
	Available   bool
	LastOutcome domain.NodeOutcome
	Source      string
}

// NodeRuntimeUsecase — исход последнего исходящего вызова одного узла (§84.7).
//
// Отдельный узкий usecase — калька NodeBreakerUsecase, и по той же причине:
// добавлять две необязательные подсистемы (Redis + Prometheus) в NodeUsecase
// ради бейджа нельзя, иначе от них начинает зависеть выдача самой карточки
// узла. Правило приоритета не дублируется — оно живёт в LastOutcomeResolver,
// общем с рабочим столом.
type NodeRuntimeUsecase struct {
	nodes    port.NodeRepo
	resolver *LastOutcomeResolver
	clock    clock.Clock
	logger   logging.Logger
}

func NewNodeRuntimeUsecase(
	nodes port.NodeRepo,
	resolver *LastOutcomeResolver,
	clk clock.Clock,
	logger logging.Logger,
) *NodeRuntimeUsecase {
	return &NodeRuntimeUsecase{nodes: nodes, resolver: resolver, clock: clk, logger: logger}
}

// State — исход последнего вызова узла. Доступно всем ролям: наблюдателю тоже
// нужно понимать, почему узел молчит (та же линия, что у состояния защиты §81.4).
func (u *NodeRuntimeUsecase) State(ctx context.Context, nodeID, teamID string) (NodeRuntimeResult, error) {
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return NodeRuntimeResult{}, fmt.Errorf("node runtime get node: %w", err)
	}
	// Тот же team-scope контракт, что у остальных операций над узлом: чужой
	// узел неотличим от несуществующего.
	if teamID != "" && node.TeamID != teamID {
		return NodeRuntimeResult{}, domain.ErrNodeNotFound
	}

	got := u.resolver.Resolve(ctx, u.clock.Now(), []string{node.Path})
	o := got[node.Path]
	if !o.Known {
		// §51.9: тихая деградация обязана оставлять след — иначе «почему бейджа
		// нет» придётся выяснять по конфигурации инсталляции.
		u.logger.Debug("node runtime: outcome unknown",
			u.logger.Str("node", node.Path))
		return NodeRuntimeResult{Available: false, Source: OutcomeSourceNone}, nil
	}
	return NodeRuntimeResult{Available: true, LastOutcome: o.Outcome, Source: o.Source}, nil
}
