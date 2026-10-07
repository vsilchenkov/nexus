package usecase

import (
	"context"
	"time"

	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
)

// PausedSweepReason — причина решения планировщика на тике; метка
// nexus_paused_sweep_passes_total.
type PausedSweepReason string

const (
	PausedSweepSkip       PausedSweepReason = "skip"       // прохода нет
	PausedSweepStartup    PausedSweepReason = "startup"    // первый тик процесса
	PausedSweepNodes      PausedSweepReason = "nodes"      // изменилась таблица узлов
	PausedSweepSignal     PausedSweepReason = "signal"     // сигнал Redis (отмена, старт или страховка другой реплики)
	PausedSweepBackstop   PausedSweepReason = "backstop"   // страховочный раунд без события
	PausedSweepRound      PausedSweepReason = "round"      // продолжение начатого раунда
	PausedSweepContinuous PausedSweepReason = "continuous" // аварийный режим: проход на каждом тике
)

// PausedSweepDecision — решение планировщика на один тик.
type PausedSweepDecision struct {
	Run    bool
	Reason PausedSweepReason
}

// NodesSnapshotReader — число узлов на паузе и отпечаток таблицы узлов
// (реализует nodepg.Reader). Отпечаток меняется от любой правки узла.
type NodesSnapshotReader interface {
	NodesSnapshot(ctx context.Context) (paused int, fingerprint string, err error)
}

// PausedSweepSignaler — общий для реплик счётчик-сигнал (реализует
// queuecancel.Redis). Bump возвращает новое значение.
type PausedSweepSignaler interface {
	Signal(ctx context.Context) (int64, error)
	Bump(ctx context.Context) (int64, error)
}

// PausedPartition — позиция группы прохода в одной партиции delay-топика.
// Committed = -1 — группа партицию не коммитила.
type PausedPartition struct {
	Partition              int
	First, Last, Committed int64
}

// PausedTopicOffsets — офсеты delay-топика и его retention из брокера.
// Positions возвращает ошибку, если хоть одна партиция не прочитана.
type PausedTopicOffsets interface {
	Positions(ctx context.Context) ([]PausedPartition, error)
	Retention(ctx context.Context) (time.Duration, error)
}

// PausedSweepMetrics — метрики планировщика (реализует *metrics.Metrics).
type PausedSweepMetrics interface {
	IncPausedSweepPass(reason string)
	SetPausedNodes(n int)
}

// PausedSweepPlannerConfig — параметры планировщика из sender.paused_sweep.
type PausedSweepPlannerConfig struct {
	// Backstop — страховочный раунд (backstop_sec); эффективно не больше
	// четверти retention топика.
	Backstop time.Duration
	// FallbackRetention — kafka.topic.retention_ms на случай, если брокер не
	// ответил на DescribeConfigs; 0 — неизвестен.
	FallbackRetention time.Duration
	// Continuous — проход на каждом тике (поведение до §113).
	Continuous bool
}

const (
	// roundStallTicks — тиков подряд без продвижения незавершённых партиций,
	// после которых раунд бросается: их не разбирает ни одна реплика.
	roundStallTicks = 3
	// retentionRefresh — как часто перечитывать retention топика: оператор
	// может понизить его на ходу (освободить диск), и backstop обязан
	// подстроиться.
	retentionRefresh = 10 * time.Minute
	// offsetsWarnEvery — не чаще одного Warn о недоступных офсетах.
	offsetsWarnEvery = 10 * time.Minute
)

// PausedSweepPlanner решает на каждом тике sweeper'а очереди паузы, нужен ли
// проход (§113.3). Проход переписывает сообщения узлов, которые всё ещё на
// паузе, поэтому он нужен, только когда судьба сообщений могла измениться:
// сменился узел (статус, тип, удаление), сообщение отменили, процесс только
// стартовал. Без этого очередь sync-узла на паузе переписывалась каждые 30 с —
// 17 ГБ за 4 дня (бой 06.10.2026).
//
// Событие запускает РАУНД — полный оборот очереди. Первый проход сразу; цель
// (конец каждой партиции) снимается на следующем тике, чтобы в раунд попали
// копии, которые основной consumer дописывал в момент события. Раунд окончен,
// когда committed-офсет группы (всех реплик) дошёл до цели в каждой партиции.
//
// Не потокобезопасен: Decide вызывает одна горутина sweeper'а.
type PausedSweepPlanner struct {
	nodes   NodesSnapshotReader
	signal  PausedSweepSignaler // nil — нет Redis: триггер signal выключен
	offsets PausedTopicOffsets
	cfg     PausedSweepPlannerConfig
	metrics PausedSweepMetrics
	clock   clock.Clock
	logger  logging.Logger

	started       bool
	fingerprint   string
	fpKnown       bool
	signalValue   int64
	signalKnown   bool
	lastRoundAt   time.Time
	backstop      time.Duration
	retentionAt   time.Time
	offsetsWarnAt time.Time
	round         *sweepRound
	// lastPassProcessed — сколько сообщений обработал последний проход
	// (PassDone). Пустой проход ничего не переписывает, и застоем раунда не
	// считается.
	lastPassProcessed int
}

// sweepRound — активный раунд. target == nil — цель ещё не снята.
type sweepRound struct {
	startedAt time.Time
	target    map[int]int64
	reached   map[int]int64 // последняя известная позиция незавершённых партиций
	stalled   int
}

// PlannerOption — функциональная опция NewPausedSweepPlanner.
type PlannerOption func(*PausedSweepPlanner)

// WithPlannerClock подменяет источник времени (тесты).
func WithPlannerClock(c clock.Clock) PlannerOption {
	return func(p *PausedSweepPlanner) { p.clock = c }
}

// NewPausedSweepPlanner создаёт планировщик. signal и m могут быть nil.
func NewPausedSweepPlanner(
	nodes NodesSnapshotReader,
	signal PausedSweepSignaler,
	offsets PausedTopicOffsets,
	cfg PausedSweepPlannerConfig,
	m PausedSweepMetrics,
	logger logging.Logger,
	opts ...PlannerOption,
) *PausedSweepPlanner {
	p := &PausedSweepPlanner{
		nodes: nodes, signal: signal, offsets: offsets, cfg: cfg,
		metrics: m, clock: clock.System(), logger: logger,
		backstop: cfg.Backstop,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Decide — решение на текущем тике.
func (p *PausedSweepPlanner) Decide(ctx context.Context) PausedSweepDecision {
	now := p.clock.Now()
	var d PausedSweepDecision
	if p.cfg.Continuous {
		p.pollNodes(ctx) // только ради gauge nexus_paused_nodes
		d = PausedSweepDecision{Run: true, Reason: PausedSweepContinuous}
	} else {
		d = p.decide(ctx, now)
	}
	if p.metrics != nil {
		p.metrics.IncPausedSweepPass(string(d.Reason))
	}
	p.logger.Debug("paused sweep: tick decision",
		p.logger.Str("reason", string(d.Reason)),
		p.logger.Any("run", d.Run),
		p.logger.Any("round_active", p.round != nil),
		p.logger.Str("backstop", p.backstop.String()))
	return d
}

// PassDone сообщает итог прохода, разрешённого Decide: сколько сообщений он
// обработал.
func (p *PausedSweepPlanner) PassDone(processed int) {
	p.lastPassProcessed = processed
}

func (p *PausedSweepPlanner) decide(ctx context.Context, now time.Time) PausedSweepDecision {
	if reason := p.trigger(ctx, now); reason != "" {
		if reason == PausedSweepBackstop && p.round != nil {
			// Раунд жив с прошлого события (lastRoundAt = его старт) и не
			// закончился за backstop: очередь больше, чем успевают проходы, или
			// офсеты недоступны. Начинаем заново с новой целью.
			p.logger.Warn("paused sweep: round did not finish within backstop, restarting",
				p.logger.Str("op", "sender.pausedSweep.round"),
				p.logger.Str("age", now.Sub(p.round.startedAt).String()),
				p.logger.Str("backstop", p.backstop.String()))
		}
		p.round = &sweepRound{startedAt: now}
		p.lastRoundAt = now
		return PausedSweepDecision{Run: true, Reason: reason}
	}
	if p.round == nil {
		return PausedSweepDecision{Reason: PausedSweepSkip}
	}
	return p.continueRound(ctx, now)
}

// trigger опрашивает все источники событий и возвращает причину нового раунда
// ("" — события нет). Источники опрашиваются каждый тик, даже когда причина уже
// найдена, — иначе изменение запомнилось бы с опозданием и дало лишний раунд.
func (p *PausedSweepPlanner) trigger(ctx context.Context, now time.Time) PausedSweepReason {
	nodesChanged := p.pollNodes(ctx)
	signalChanged := p.pollSignal(ctx)
	p.refreshBackstop(ctx, now)
	switch {
	case !p.started:
		p.started = true
		p.bump(ctx)
		return PausedSweepStartup
	case nodesChanged:
		return PausedSweepNodes
	case signalChanged:
		return PausedSweepSignal
	case now.Sub(p.lastRoundAt) >= p.backstop:
		p.bump(ctx)
		return PausedSweepBackstop
	}
	return ""
}

// pollNodes читает отпечаток узлов; true — он изменился с прошлого успешного
// чтения. Ошибка значение не трогает: изменение, пришедшееся на сбой PG,
// поймается после восстановления.
func (p *PausedSweepPlanner) pollNodes(ctx context.Context) bool {
	paused, fp, err := p.nodes.NodesSnapshot(ctx)
	if err != nil {
		p.logger.Debug("paused sweep: nodes snapshot unavailable, trigger skipped this tick",
			p.logger.Err(err))
		return false
	}
	if p.metrics != nil {
		p.metrics.SetPausedNodes(paused)
	}
	changed := p.fpKnown && fp != p.fingerprint
	p.fingerprint, p.fpKnown = fp, true
	return changed
}

// pollSignal читает счётчик-сигнал; true — он изменился с прошлого успешного
// чтения. Без Redis триггер выключен.
func (p *PausedSweepPlanner) pollSignal(ctx context.Context) bool {
	if p.signal == nil {
		return false
	}
	v, err := p.signal.Signal(ctx)
	if err != nil {
		p.logger.Debug("paused sweep: signal unavailable, trigger skipped this tick", p.logger.Err(err))
		return false
	}
	changed := p.signalKnown && v != p.signalValue
	p.signalValue, p.signalKnown = v, true
	return changed
}

// bump поднимает сигнал, чтобы раунд начали и другие реплики: раунд общий для
// группы (каждая реплика читает свои партиции). Своё значение запоминается —
// собственный инкремент не должен на следующем тике сбросить раунд.
func (p *PausedSweepPlanner) bump(ctx context.Context) {
	if p.signal == nil {
		return
	}
	v, err := p.signal.Bump(ctx)
	if err != nil {
		p.logger.Debug("paused sweep: signal bump failed, other replicas keep their schedule", p.logger.Err(err))
		return
	}
	p.signalValue, p.signalKnown = v, true
}
