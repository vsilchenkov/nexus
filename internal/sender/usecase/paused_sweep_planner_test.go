package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

type fakeNodesSnapshot struct {
	paused int
	fp     string
	err    error
}

func (f *fakeNodesSnapshot) NodesSnapshot(context.Context) (int, string, error) {
	return f.paused, f.fp, f.err
}

type fakeSweepSignal struct {
	v     int64
	err   error
	bumps int
}

func (f *fakeSweepSignal) Signal(context.Context) (int64, error) { return f.v, f.err }

func (f *fakeSweepSignal) Bump(context.Context) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.bumps++
	f.v++
	return f.v, nil
}

type fakePausedOffsets struct {
	pos       []PausedPartition
	posErr    error
	posCalls  int
	retention time.Duration
	retErr    error
}

func (f *fakePausedOffsets) Positions(context.Context) ([]PausedPartition, error) {
	f.posCalls++
	return f.pos, f.posErr
}

func (f *fakePausedOffsets) Retention(context.Context) (time.Duration, error) {
	return f.retention, f.retErr
}

// setCommitted ставит committed партиции part (позиции раунда).
func (f *fakePausedOffsets) setCommitted(part int, c int64) {
	for i := range f.pos {
		if f.pos[i].Partition == part {
			f.pos[i].Committed = c
		}
	}
}

type fakeSweepMetrics struct {
	reasons []string
	paused  int
}

func (m *fakeSweepMetrics) IncPausedSweepPass(r string) { m.reasons = append(m.reasons, r) }
func (m *fakeSweepMetrics) SetPausedNodes(n int)        { m.paused = n }

type manualClock struct{ t time.Time }

func (c *manualClock) Now() time.Time          { return c.t }
func (c *manualClock) advance(d time.Duration) { c.t = c.t.Add(d) }

type plannerFixture struct {
	nodes   *fakeNodesSnapshot
	signal  *fakeSweepSignal
	offsets *fakePausedOffsets
	metrics *fakeSweepMetrics
	clock   *manualClock
	p       *PausedSweepPlanner
	// passProcessed — сколько сообщений «обрабатывает» каждый проход.
	passProcessed int
}

const testTick = 30 * time.Second

// newPlannerFixture — две партиции: в 0 очередь 10 сообщений (группа на 0),
// 1 пуста. Backstop 6 ч, retention 7 дней.
func newPlannerFixture(t *testing.T, cfg PausedSweepPlannerConfig) *plannerFixture {
	t.Helper()
	f := &plannerFixture{
		nodes:  &fakeNodesSnapshot{paused: 1, fp: "fp-1"},
		signal: &fakeSweepSignal{v: 7},
		offsets: &fakePausedOffsets{
			pos: []PausedPartition{
				{Partition: 0, First: 0, Last: 10, Committed: 0},
				{Partition: 1, First: 0, Last: 0, Committed: -1},
			},
			retention: 7 * 24 * time.Hour,
		},
		metrics:       &fakeSweepMetrics{},
		clock:         &manualClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)},
		passProcessed: 5,
	}
	if cfg.Backstop == 0 {
		cfg.Backstop = 6 * time.Hour
	}
	f.p = NewPausedSweepPlanner(f.nodes, f.signal, f.offsets, cfg, f.metrics, logging.NewNoop(),
		WithPlannerClock(f.clock))
	return f
}

// tick — один тик: сдвиг часов, решение и, если проход разрешён, его итог
// (passProcessed сообщений).
func (f *plannerFixture) tick() PausedSweepDecision {
	f.clock.advance(testTick)
	d := f.p.Decide(context.Background())
	if d.Run {
		f.p.PassDone(f.passProcessed)
	}
	return d
}

func run(r PausedSweepReason) PausedSweepDecision { return PausedSweepDecision{Run: true, Reason: r} }

var skip = PausedSweepDecision{Reason: PausedSweepSkip}

// completeStartupRound проводит стартовый раунд до конца: старт, снятие цели,
// дочитывание партиции 0, завершение.
func (f *plannerFixture) completeStartupRound(t *testing.T) {
	t.Helper()
	require.Equal(t, run(PausedSweepStartup), f.tick())
	require.Equal(t, run(PausedSweepRound), f.tick(), "цель снимается на следующем тике")
	f.offsets.setCommitted(0, 10)
	require.Equal(t, skip, f.tick(), "группа дочитала до цели — раунд окончен")
}

// TestPlanner_StartupRoundThenIdle: стартовый раунд проходит полный оборот и
// заканчивается; дальше, пока ничего не меняется, тики пропускаются — очередь
// паузы не переписывается (инцидент 06.10.2026).
func TestPlanner_StartupRoundThenIdle(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})

	f.completeStartupRound(t)
	posCalls := f.offsets.posCalls
	for range 20 {
		assert.Equal(t, skip, f.tick())
	}
	assert.Equal(t, posCalls, f.offsets.posCalls, "в простое брокер не опрашивается")
	assert.Equal(t, 1, f.signal.bumps, "старт поднимает сигнал для других реплик")
	assert.Equal(t, 1, f.metrics.paused)
	assert.Equal(t, "startup", f.metrics.reasons[0])
	assert.Equal(t, "skip", f.metrics.reasons[len(f.metrics.reasons)-1])
}

// TestPlanner_Triggers: каждое событие запускает раунд со своей причиной;
// собственный Bump не считается чужим сигналом.
func TestPlanner_Triggers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *plannerFixture)
		want   PausedSweepDecision
	}{
		{"nothing changed", func(*plannerFixture) {}, skip},
		{"node edited", func(f *plannerFixture) { f.nodes.fp = "fp-2" }, run(PausedSweepNodes)},
		{"cancel signal", func(f *plannerFixture) { f.signal.v++ }, run(PausedSweepSignal)},
		{"paused count only", func(f *plannerFixture) { f.nodes.paused = 0 }, skip},
		{"backstop elapsed", func(f *plannerFixture) { f.clock.advance(6 * time.Hour) }, run(PausedSweepBackstop)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPlannerFixture(t, PausedSweepPlannerConfig{})
			f.completeStartupRound(t)

			tt.change(f)
			assert.Equal(t, tt.want, f.tick())
		})
	}
}

// TestPlanner_BackstopBumpsSignal: страховочный раунд поднимает сигнал — его
// подхватят другие реплики; сам планировщик свой инкремент не принимает за
// событие.
func TestPlanner_BackstopBumpsSignal(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.completeStartupRound(t)

	f.clock.advance(6 * time.Hour)
	require.Equal(t, run(PausedSweepBackstop), f.tick())
	assert.Equal(t, 2, f.signal.bumps)
	assert.Equal(t, run(PausedSweepRound), f.tick(), "не signal: свой Bump не сбрасывает раунд")
}

// TestPlanner_ReadErrorsDoNotLoseChange: изменение, пришедшееся на сбой PG или
// Redis, ловится после восстановления — прежнее значение запоминается только
// после успешного чтения.
func TestPlanner_ReadErrorsDoNotLoseChange(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.completeStartupRound(t)

	f.nodes.fp, f.nodes.err = "fp-2", errors.New("pg down")
	f.signal.err = errors.New("redis down")
	assert.Equal(t, skip, f.tick())

	f.nodes.err = nil
	assert.Equal(t, run(PausedSweepNodes), f.tick())

	f.offsets.setCommitted(0, 10)
	f.signal.v++
	f.signal.err = nil
	assert.Equal(t, run(PausedSweepSignal), f.tick(), "сигнал во время сбоя Redis не потерян")
}

// TestPlanner_NoRedis: без Redis сигнала нет, но старт, узлы и backstop работают.
func TestPlanner_NoRedis(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.p.signal = nil

	f.completeStartupRound(t)
	f.nodes.fp = "fp-2"
	assert.Equal(t, run(PausedSweepNodes), f.tick())
	assert.Zero(t, f.signal.bumps)
}

// TestPlanner_RoundCompletion: раунд закончен, когда committed группы (не левее
// First) дошёл до цели в каждой партиции.
func TestPlanner_RoundCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pos  []PausedPartition
		done bool
	}{
		{"committed reached target", []PausedPartition{{Partition: 0, First: 0, Last: 15, Committed: 10}}, true},
		{"committed behind target", []PausedPartition{{Partition: 0, First: 0, Last: 15, Committed: 9}}, false},
		{"retention cut past committed", []PausedPartition{{Partition: 0, First: 12, Last: 15, Committed: 3}}, true},
		{"never committed, empty", []PausedPartition{{Partition: 0, First: 10, Last: 10, Committed: -1}}, true},
		{"partition missing from answer", []PausedPartition{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPlannerFixture(t, PausedSweepPlannerConfig{})
			f.offsets.pos = []PausedPartition{{Partition: 0, First: 0, Last: 10, Committed: 0}}
			require.Equal(t, run(PausedSweepStartup), f.tick())
			require.Equal(t, run(PausedSweepRound), f.tick()) // цель: партиция 0 → 10

			f.offsets.pos = tt.pos
			got := f.tick()
			if tt.done {
				assert.Equal(t, skip, got)
				return
			}
			assert.Equal(t, run(PausedSweepRound), got)
		})
	}
}

// TestPlanner_TargetCapturedOnNextTick: цель снимается на тике ПОСЛЕ события —
// в раунд попадают копии, которые основной consumer дописал в момент события.
func TestPlanner_TargetCapturedOnNextTick(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})

	require.Equal(t, run(PausedSweepStartup), f.tick())
	f.offsets.pos[0].Last = 12 // дописано между тиками
	require.Equal(t, run(PausedSweepRound), f.tick())

	f.offsets.setCommitted(0, 10)
	assert.Equal(t, run(PausedSweepRound), f.tick(), "до 12 ещё не дочитали")
	f.offsets.setCommitted(0, 12)
	assert.Equal(t, skip, f.tick())
}

// TestPlanner_TriggerMidRoundResetsTarget: новое событие посреди раунда
// начинает раунд заново — цель снимается снова.
func TestPlanner_TriggerMidRoundResetsTarget(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})

	require.Equal(t, run(PausedSweepStartup), f.tick())
	require.Equal(t, run(PausedSweepRound), f.tick()) // цель 10
	f.offsets.setCommitted(0, 5)
	f.signal.v++
	require.Equal(t, run(PausedSweepSignal), f.tick())

	f.offsets.pos[0].Last = 20
	require.Equal(t, run(PausedSweepRound), f.tick()) // новая цель 20
	f.offsets.setCommitted(0, 10)
	assert.Equal(t, run(PausedSweepRound), f.tick(), "старая цель уже не действует")
}

// TestPlanner_OffsetsUnavailableSweepsEveryTick: без офсетов раунд нельзя ни
// снять, ни завершить — проход на каждом тике, как до §113.
func TestPlanner_OffsetsUnavailableSweepsEveryTick(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.offsets.posErr = errors.New("broker down")

	require.Equal(t, run(PausedSweepStartup), f.tick())
	for range 5 {
		assert.Equal(t, run(PausedSweepRound), f.tick())
	}
	f.offsets.posErr = nil
	assert.Equal(t, run(PausedSweepRound), f.tick(), "офсеты вернулись — снимается цель")
	f.offsets.setCommitted(0, 10)
	assert.Equal(t, skip, f.tick())
}

// TestPlanner_StalledRoundAbandoned: партиции, которые никто не дочитывает
// (зависший reader, реплика без Redis не узнала о раунде), не держат раунд —
// иначе свои партиции переписывались бы каждый тик до backstop.
func TestPlanner_StalledRoundAbandoned(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})

	require.Equal(t, run(PausedSweepStartup), f.tick())
	require.Equal(t, run(PausedSweepRound), f.tick())
	f.offsets.setCommitted(0, 4)
	require.Equal(t, run(PausedSweepRound), f.tick(), "первая проверка — продвижение")
	for i := 1; i < roundStallTicks; i++ {
		require.Equal(t, run(PausedSweepRound), f.tick(), "тик без продвижения %d", i)
	}
	assert.Equal(t, skip, f.tick(), "раунд брошен")
	assert.Equal(t, skip, f.tick())
}

// TestPlanner_IdlePassesDoNotAbandonRound: проходы ничего не прочитали (сбой
// брокера, нагрузка) — они ничего и не переписали. Раунд не бросается: иначе
// снятие паузы или отмена ждали бы backstop (6 ч).
func TestPlanner_IdlePassesDoNotAbandonRound(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})

	require.Equal(t, run(PausedSweepStartup), f.tick())
	require.Equal(t, run(PausedSweepRound), f.tick())
	f.passProcessed = 0
	for i := range 3 * roundStallTicks {
		require.Equal(t, run(PausedSweepRound), f.tick(), "пустой проход %d", i)
	}
	f.passProcessed = 3
	f.offsets.setCommitted(0, 10)
	assert.Equal(t, skip, f.tick(), "reader ожил — раунд дошёл до конца")
}

// TestPlanner_ProgressKeepsRoundAlive: пока партиции двигаются, раунд живёт.
func TestPlanner_ProgressKeepsRoundAlive(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.offsets.pos[0].Last = 100

	require.Equal(t, run(PausedSweepStartup), f.tick())
	require.Equal(t, run(PausedSweepRound), f.tick())
	for c := int64(1); c < 10; c++ {
		f.offsets.setCommitted(0, c*10)
		require.Equal(t, run(PausedSweepRound), f.tick())
	}
	f.offsets.setCommitted(0, 100)
	assert.Equal(t, skip, f.tick())
}

// TestPlanner_RoundOlderThanBackstopRestarts: раунд, не закончившийся за
// backstop, начинается заново с новой целью (а не тянется бесконечно).
func TestPlanner_RoundOlderThanBackstopRestarts(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{Backstop: 2 * time.Minute})
	f.offsets.pos[0].Last = 1000

	require.Equal(t, run(PausedSweepStartup), f.tick()) // 0:30
	require.Equal(t, run(PausedSweepRound), f.tick())   // 1:00, цель 1000
	f.offsets.setCommitted(0, 10)
	require.Equal(t, run(PausedSweepRound), f.tick()) // 1:30
	f.offsets.setCommitted(0, 20)
	require.Equal(t, run(PausedSweepRound), f.tick()) // 2:00
	f.offsets.pos[0].Last = 1200
	assert.Equal(t, run(PausedSweepBackstop), f.tick(), "2:30 — раунду 2 минуты")
	assert.Equal(t, run(PausedSweepRound), f.tick(), "новая цель")
	f.offsets.setCommitted(0, 1000)
	assert.Equal(t, run(PausedSweepRound), f.tick(), "старая цель 1000 больше не действует")
}

// TestPlanner_BackstopLimitedByRetention: backstop не больше четверти
// retention топика; брокер не ответил — retention из конфига; без ограничения
// по времени — backstop из конфига.
func TestPlanner_BackstopLimitedByRetention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retention time.Duration
		retErr    error
		fallback  time.Duration
		want      time.Duration
	}{
		{"week retention keeps 6h", 7 * 24 * time.Hour, nil, 0, 6 * time.Hour},
		{"8h retention cuts to 2h", 8 * time.Hour, nil, 0, 2 * time.Hour},
		{"broker error uses config retention", 0, errors.New("dial"), 4 * time.Hour, time.Hour},
		{"broker error, no config", 0, errors.New("dial"), 0, 6 * time.Hour},
		{"unlimited retention", 0, nil, 0, 6 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newPlannerFixture(t, PausedSweepPlannerConfig{FallbackRetention: tt.fallback})
			f.offsets.retention, f.offsets.retErr = tt.retention, tt.retErr
			f.p.Decide(context.Background())
			assert.Equal(t, tt.want, f.p.backstop)
		})
	}
}

// TestPlanner_RetentionLoweredAtRuntime: оператор понизил retention на ходу —
// backstop подстраивается не позже retentionRefresh.
func TestPlanner_RetentionLoweredAtRuntime(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{})
	f.completeStartupRound(t)

	f.offsets.retention = 4 * time.Hour
	f.clock.advance(retentionRefresh)
	assert.Equal(t, skip, f.tick(), "последний раунд ~11 минут назад")
	assert.Equal(t, time.Hour, f.p.backstop, "retention перечитан: backstop = 4ч/4")
	f.clock.advance(time.Hour)
	assert.Equal(t, run(PausedSweepBackstop), f.tick(), "через час — страховочный раунд, а не через 6")
}

// TestPlanner_Continuous: аварийный режим — проход на каждом тике без Bump,
// gauge узлов на паузе обновляется.
func TestPlanner_Continuous(t *testing.T) {
	t.Parallel()
	f := newPlannerFixture(t, PausedSweepPlannerConfig{Continuous: true})
	f.nodes.paused = 3

	for range 5 {
		assert.Equal(t, run(PausedSweepContinuous), f.tick())
	}
	assert.Zero(t, f.signal.bumps)
	assert.Zero(t, f.offsets.posCalls)
	assert.Equal(t, 3, f.metrics.paused)
}
