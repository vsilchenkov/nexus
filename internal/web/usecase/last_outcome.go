package usecase

import (
	"context"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Источники исхода последнего вызова (§84.7) — что именно ответило.
const (
	OutcomeSourceRedis      = "redis"      // персистентный, переживает рестарт Sender/Web
	OutcomeSourcePrometheus = "prometheus" // instant-gauge, сбрасывается при рестарте Sender
	OutcomeSourceNone       = "none"       // ни один источник не ответил
)

// LastOutcomeResolver — исход последнего исходящего вызова узла (§46/§52).
//
// Вынесен из MetricsUsecase.applyLastOutcomes отдельным типом, потому что у
// правила приоритета появился ВТОРОЙ потребитель — бейдж на странице узла
// (§84.7). Дублировать приоритет во втором месте нельзя: две копии одного
// правила расходятся, и рабочий стол начинает показывать одно, а страница узла
// другое. Тащить Redis и Prometheus в NodeUsecase ради бейджа тоже нельзя —
// карточка узла стала бы зависеть от двух необязательных подсистем.
//
// Оба порта опциональны (nil при отсутствующей подсистеме) — это норма
// инсталляции, а не ошибка.
type LastOutcomeResolver struct {
	status port.NodeStatusReader // nil без Redis
	prom   port.PromMetrics      // nil без Prometheus
	// teams — слог команды для ключа гауджа Prometheus (§101.9). nil — гаудж
	// не сопоставляется ни с одним узлом.
	teams  port.TeamRepo
	logger logging.Logger
}

func NewLastOutcomeResolver(
	status port.NodeStatusReader,
	prom port.PromMetrics,
	teams port.TeamRepo,
	logger logging.Logger,
) *LastOutcomeResolver {
	return &LastOutcomeResolver{status: status, prom: prom, teams: teams, logger: logger}
}

// Outcome — исход одного узла и источник, откуда он взят.
//
// Known=false означает «неизвестно»: ни Redis, ни Prometheus не дали значения.
// Отдельный флаг, а не «ok по умолчанию»: соврать оператору «всё в порядке»
// хуже, чем не сказать ничего (та же линия, что у NodeBreakerUsecase.State).
// Рабочий стол при этом продолжает трактовать неизвестность как ok — там бейдж
// есть у каждой строки таблицы и пустого состояния у него нет.
type Outcome struct {
	Outcome domain.NodeOutcome
	Source  string
	Known   bool
}

// NodeRef — узел для резолва исхода. ID — ключ Redis (§101.6); TeamID и Path —
// ключ Prometheus-гауджа: слог команды по TeamID и путь (§101.9).
type NodeRef struct {
	ID     string
	Path   string
	TeamID string
}

// Resolve — исходы для набора узлов, карта по id узла.
//
// Приоритет: Redis (персистентный, §46) → Prometheus (instant-gauge §41,
// fallback для узлов, которых ещё нет в Redis) → неизвестно. Ошибка любого
// источника деградирует мягко: он просто не участвует.
//
// Узел без id в карту не попадает: ключа в Redis у него нет, а по пути
// одноимённые узлы разных команд неразличимы.
func (r *LastOutcomeResolver) Resolve(ctx context.Context, at time.Time, nodes []NodeRef) map[string]Outcome {
	out := make(map[string]Outcome, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == "" {
			r.logger.Debug("last outcome: node without id skipped", r.logger.Str("node", n.Path))
			continue
		}
		ids = append(ids, n.ID)
	}
	if len(ids) == 0 {
		return out
	}

	redisLO := r.redisOutcomes(ctx, ids)
	promLE := r.promOutcomes(ctx, at)
	var slugs map[string]string
	if len(promLE) > 0 {
		slugs = teamSlugsByID(ctx, r.teams, r.logger)
	}

	for _, n := range nodes {
		if n.ID == "" {
			continue
		}
		if v, ok := redisLO[n.ID]; ok {
			out[n.ID] = Outcome{Outcome: v, Source: OutcomeSourceRedis, Known: true}
			continue
		}
		// По паре (слог, путь) и без рядов до §101: гаудж старого процесса
		// Sender исчезает с его рестартом, а исходы двух узлов не складываются.
		if slug := slugs[n.TeamID]; slug != "" {
			if v, ok := promLE[port.NodeKey{Team: slug, Path: n.Path}]; ok {
				out[n.ID] = Outcome{Outcome: domain.OutcomeFromGaugeValue(v), Source: OutcomeSourcePrometheus, Known: true}
				continue
			}
		}
		out[n.ID] = Outcome{Outcome: domain.NodeOutcomeOK, Source: OutcomeSourceNone}
	}
	return out
}

// redisOutcomes — исходы из Redis по id; nil без Redis или при его ошибке.
func (r *LastOutcomeResolver) redisOutcomes(ctx context.Context, ids []string) map[string]domain.NodeOutcome {
	if r.status == nil {
		return nil
	}
	m, err := r.status.GetLastOutcomes(ctx, ids)
	if err != nil {
		r.logger.Warn("redis node last outcomes failed", r.logger.Err(err))
		return nil
	}
	return m
}

// promOutcomes — гауджи Prometheus по паре (team, node); nil без Prometheus или
// при его ошибке.
func (r *LastOutcomeResolver) promOutcomes(ctx context.Context, at time.Time) map[port.NodeKey]float64 {
	if r.prom == nil {
		return nil
	}
	m, err := r.prom.NodeLastErrors(ctx, at)
	if err != nil {
		r.logger.Warn("prometheus node last errors failed", r.logger.Err(err))
		return nil
	}
	return m
}
