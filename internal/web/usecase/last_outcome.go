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
	logger logging.Logger
}

func NewLastOutcomeResolver(
	status port.NodeStatusReader,
	prom port.PromMetrics,
	logger logging.Logger,
) *LastOutcomeResolver {
	return &LastOutcomeResolver{status: status, prom: prom, logger: logger}
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

// Resolve — исходы для набора путей узлов.
//
// Приоритет: Redis (персистентный, §46) → Prometheus (instant-gauge §41,
// fallback для узлов, которых ещё нет в Redis) → неизвестно. Ошибка любого
// источника деградирует мягко: он просто не участвует.
func (r *LastOutcomeResolver) Resolve(ctx context.Context, at time.Time, paths []string) map[string]Outcome {
	out := make(map[string]Outcome, len(paths))
	if len(paths) == 0 {
		return out
	}

	var redisLO map[string]domain.NodeOutcome
	if r.status != nil {
		if m, err := r.status.GetLastOutcomes(ctx, paths); err != nil {
			r.logger.Warn("redis node last outcomes failed", r.logger.Err(err))
		} else {
			redisLO = m
		}
	}

	var promLE map[string]float64
	if r.prom != nil {
		if m, err := r.prom.NodeLastErrors(ctx, at); err != nil {
			r.logger.Warn("prometheus node last errors failed", r.logger.Err(err))
		} else {
			promLE = m
		}
	}

	for _, p := range paths {
		if v, ok := redisLO[p]; ok {
			out[p] = Outcome{Outcome: v, Source: OutcomeSourceRedis, Known: true}
			continue
		}
		if v, ok := promLE[p]; ok {
			out[p] = Outcome{Outcome: domain.OutcomeFromGaugeValue(v), Source: OutcomeSourcePrometheus, Known: true}
			continue
		}
		out[p] = Outcome{Outcome: domain.NodeOutcomeOK, Source: OutcomeSourceNone}
	}
	return out
}
