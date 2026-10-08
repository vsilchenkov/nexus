package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// fakeKafkaAdmin — конфигурируемый port.KafkaAdmin для тестов usecase.
// Методы зовутся из горутин чтения брокера (§114.4), поэтому счётчики
// атомарные, а wait — общий для обоих чтений хук «брокер завис».
type fakeKafkaAdmin struct {
	topics    []port.TopicInfo
	topicsErr error
	health    port.BrokerHealth
	healthErr error
	pings     []port.BrokerPing
	pingErr   error

	wait        func(context.Context) error // nil — ответ сразу
	topicsCalls atomic.Int32
	healthCalls atomic.Int32
}

func (f *fakeKafkaAdmin) Topics(ctx context.Context) ([]port.TopicInfo, error) {
	f.topicsCalls.Add(1)
	if f.wait != nil {
		if err := f.wait(ctx); err != nil {
			return nil, err
		}
	}
	return f.topics, f.topicsErr
}
func (f *fakeKafkaAdmin) BrokerHealth(ctx context.Context) (port.BrokerHealth, error) {
	f.healthCalls.Add(1)
	if f.wait != nil {
		if err := f.wait(ctx); err != nil {
			return port.BrokerHealth{}, err
		}
	}
	return f.health, f.healthErr
}
func (f *fakeKafkaAdmin) Ping(context.Context) ([]port.BrokerPing, error) {
	return f.pings, f.pingErr
}

// memKafkaCache — in-memory port.KafkaCache без TTL: для тестов важно лишь то,
// что второй вызов попадает в кеш. Значения гоняются через JSON, как в
// Redis-адаптере, — иначе тест хранил бы указатель на тот же слайс и не заметил
// бы, что в кеш уехали размеры. Нечитаемое значение — промах, как в
// Redis-адаптере. Под mutex: чтения брокера идут из горутин (§114.4).
type memKafkaCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKafkaCache() *memKafkaCache { return &memKafkaCache{data: map[string][]byte{}} }

func (c *memKafkaCache) Get(_ context.Context, key string, dst any) (bool, error) {
	c.mu.Lock()
	raw, ok := c.data[key]
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	if json.Unmarshal(raw, dst) != nil {
		return false, nil //nolint:nilerr // повреждённый кеш = промах, как в Redis-адаптере
	}
	return true, nil
}

func (c *memKafkaCache) Set(_ context.Context, key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = raw
	return nil
}

func (c *memKafkaCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.data[key]
	return ok
}

func kafkaWindow() (time.Time, time.Time) {
	until := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	return until.Add(-time.Hour), until
}

func TestKafkaOverview_PrometheusAndKafka(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{
		kafkaOv: port.KafkaSummary{
			Produced: 1000, Consumed: 990, FailedProduced: 0, FailedConsumed: 1, CurrentLag: 50,
		},
	}
	admin := &fakeKafkaAdmin{health: port.BrokerHealth{BrokersTotal: 3, BrokersOnline: 3}}
	uc := NewKafkaMonitorUsecase(prom, admin, nil, nil, defaultTh(), logging.NewNoop())

	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)

	assert.True(t, r.PrometheusAvailable)
	assert.True(t, r.KafkaAvailable)
	assert.Equal(t, uint64(1000), r.Summary.ProducedTotal)
	assert.Equal(t, uint64(50), r.Summary.CurrentLag)
	assert.Equal(t, 3, r.BrokerHealth.BrokersOnline)
	// error_rate = 1/(1000+990) ≈ 0.0005 < порога warning 0.001 → healthy.
	assert.InDelta(t, 1.0/1990.0, r.ErrorRate, 1e-9)
	assert.Equal(t, SeverityOK, r.Health.Severity)
	// prev period == cur (фейк) → дельта 0, но определена.
	assert.True(t, r.HasDelta)
	assert.InDelta(t, 0.0, r.DeltaProduced, 1e-9)
}

func TestKafkaOverview_DegradesWithoutSources(t *testing.T) {
	t.Parallel()
	uc := NewKafkaMonitorUsecase(nil, nil, nil, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)

	assert.False(t, r.PrometheusAvailable)
	assert.False(t, r.KafkaAvailable)
	assert.Equal(t, uint64(0), r.Summary.ProducedTotal)
	// Без сигналов кластер считается здоровым.
	assert.Equal(t, SeverityOK, r.Health.Severity)
}

func TestKafkaOverview_BrokerHealthErrorDegrades(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{healthErr: errors.New("boom")}
	uc := NewKafkaMonitorUsecase(&fakeProm{}, admin, nil, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)
	assert.False(t, r.KafkaAvailable)
}

func TestKafkaTopics(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{{Name: "nexus.async", Partitions: 4}}}
	uc := NewKafkaMonitorUsecase(nil, admin, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Topics(context.Background())
	require.True(t, r.KafkaAvailable)
	require.Len(t, r.Topics, 1)
	assert.Equal(t, "nexus.async", r.Topics[0].Name)
}

func TestKafkaTopics_NoAdmin(t *testing.T) {
	t.Parallel()
	uc := NewKafkaMonitorUsecase(nil, nil, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Topics(context.Background())
	assert.False(t, r.KafkaAvailable)
	assert.Empty(t, r.Topics)
}

// §75: размер топика приходит из Prometheus (JMX-агент брокера) — админ-API
// Kafka его не отдаёт. Топики без метрики остаются с нулём (UI рисует «—»).
func TestKafkaTopics_SizesFromPrometheus(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{
		{Name: "nexus.async", Partitions: 4},
		{Name: "nexus.async.dlq", Partitions: 4},
		{Name: "nexus.logs.retry", Partitions: 4},
	}}
	prom := &fakeProm{topicSizes: map[string]int64{
		"nexus.async":     1503238553,
		"nexus.async.dlq": 12907,
		// nexus.logs.retry в ответе метрик отсутствует вовсе (например, партиций
		// топика нет на опрошенном брокере) — размер остаётся нулевым.
		"__consumer_offsets": 4096, // internal-топика в списке нет — ключ игнорируется
	}}
	uc := NewKafkaMonitorUsecase(prom, admin, nil, nil, defaultTh(), logging.NewNoop())

	r := uc.Topics(context.Background())

	require.True(t, r.KafkaAvailable)
	require.Len(t, r.Topics, 3)
	assert.Equal(t, int64(1503238553), r.Topics[0].SizeBytes)
	assert.Equal(t, int64(12907), r.Topics[1].SizeBytes)
	assert.Zero(t, r.Topics[2].SizeBytes)
	// Источник ответил — нулевой размер третьего топика означает «топик пуст»,
	// и UI обязан показать «0 B», а не подсказку про ненастроенный экспортёр.
	assert.True(t, r.SizesAvailable)
}

// §75 (ревизия): SizesAvailable отличает «источника нет» от «топик пуст».
// Без флага UI выдавал бы пустой топик за сломанный JMX-экспортёр и посылал
// оператора чинить исправный мониторинг.
func TestKafkaTopics_SizesUnavailableWhenNoSeries(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{{Name: "nexus.async", Partitions: 4}}}

	// Prometheus жив, но серий kafka_log_log_size нет (агент не поднят).
	uc := NewKafkaMonitorUsecase(&fakeProm{topicSizes: map[string]int64{}}, admin, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Topics(context.Background())
	require.True(t, r.KafkaAvailable)
	assert.False(t, r.SizesAvailable)

	// Prometheus вовсе не сконфигурирован — тот же исход.
	uc = NewKafkaMonitorUsecase(nil, admin, nil, nil, defaultTh(), logging.NewNoop())
	r = uc.Topics(context.Background())
	require.True(t, r.KafkaAvailable)
	assert.False(t, r.SizesAvailable)
}

// §75 (ревизия): кеш метаданных Kafka не должен консервировать размеры —
// они запрашиваются на каждый вызов, иначе после пропажи метрики UI до конца
// TTL показывал бы размеры, противореча собственному флагу.
func TestKafkaTopics_SizesNotFrozenByCache(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{{Name: "nexus.async", Partitions: 4}}}
	prom := &fakeProm{topicSizes: map[string]int64{"nexus.async": 4096}}
	cache := newMemKafkaCache()
	uc := NewKafkaMonitorUsecase(prom, admin, cache, nil, defaultTh(), logging.NewNoop())

	first := uc.Topics(context.Background())
	require.Len(t, first.Topics, 1)
	require.Equal(t, int64(4096), first.Topics[0].SizeBytes)
	require.True(t, first.SizesAvailable)

	// Метрика пропала (агент упал). Метаданные всё ещё берутся из кеша, но
	// размер обязан обнулиться вместе с флагом.
	prom.topicSizes = map[string]int64{}
	second := uc.Topics(context.Background())
	require.Len(t, second.Topics, 1)
	assert.Zero(t, second.Topics[0].SizeBytes)
	assert.False(t, second.SizesAvailable)
	assert.True(t, second.KafkaAvailable)
}

// §75: недоступный Prometheus не должен ломать список топиков — размеры просто
// остаются нулевыми (деградация до поведения, которое было до §75).
func TestKafkaTopics_SizesDegradeOnPromError(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{{Name: "nexus.async", Partitions: 4, MessagesEstimate: 42}}}
	prom := &fakeProm{topicSizeErr: errors.New("prometheus down")}
	uc := NewKafkaMonitorUsecase(prom, admin, nil, nil, defaultTh(), logging.NewNoop())

	r := uc.Topics(context.Background())

	require.True(t, r.KafkaAvailable)
	require.Len(t, r.Topics, 1)
	assert.Zero(t, r.Topics[0].SizeBytes)
	assert.Equal(t, int64(42), r.Topics[0].MessagesEstimate)
}

// «Топ продюсеров» — только async (§113.5), «Top failures» — весь трафик.
// Регрессия 1.41.0: оба блока строились из async-среза, и ошибки sync-узлов
// пропали со страницы — на бою блок показывал «Никаких ошибок» при живых
// ошибках sync-узлов.
func TestKafkaByNode_TopProducersAndFailures(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{
		asyncThroughput: map[port.NodeKey]port.NodeThroughput{
			{Team: "fin", Path: "billing"}:    {Out: 800},
			{Team: "geo", Path: "geo/notify"}: {Out: 200},
			{Team: "old", Path: "old/legacy"}: {Out: 100, Errors: 50},
		},
		failures: map[port.NodeKey]port.NodeThroughput{
			{Team: "fin", Path: "billing"}:    {Out: 800},
			{Team: "old", Path: "old/legacy"}: {Out: 100, Errors: 50},
			// sync-узел: в Kafka его нет, ошибки есть.
			{Team: "mcp", Path: "mcp-1c-vika"}: {Out: 40, Errors: 10},
		},
	}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.ByNode(context.Background(), since, until)

	require.True(t, r.PrometheusAvailable)
	require.Len(t, r.TopProducers, 3)
	assert.Equal(t, "billing", r.TopProducers[0].NodePath)
	assert.InDelta(t, 800.0/1100.0, r.TopProducers[0].Share, 1e-9)
	for _, p := range r.TopProducers {
		assert.NotEqual(t, "mcp-1c-vika", p.NodePath, "sync-узел в продюсерах Kafka")
	}

	require.Len(t, r.TopFailures, 2)
	assert.Equal(t, "old/legacy", r.TopFailures[0].NodePath)
	// §113.8: ошибки — подмножество исходящих, знаменатель — Out.
	assert.InDelta(t, 50.0/100.0, r.TopFailures[0].Rate, 1e-9)
	assert.Equal(t, NodeFailure{NodePath: "mcp-1c-vika", Team: "mcp", Failed: 10, Rate: 0.25}, r.TopFailures[1])
}

// Отказ запроса ошибок — деградация всего ответа, а не пустой «Top failures»
// рядом с живыми продюсерами: пустой блок экран показывает как «ошибок нет».
func TestKafkaByNode_FailuresQueryDown(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{
		asyncThroughput: map[port.NodeKey]port.NodeThroughput{{Team: "fin", Path: "billing"}: {Out: 800}},
		failuresErr:     errors.New("prometheus: timeout"),
	}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.ByNode(context.Background(), since, until)

	assert.False(t, r.PrometheusAvailable)
	assert.Empty(t, r.TopProducers)
	assert.Empty(t, r.TopFailures)
}

// TestTopFailures_Rate (§113.8): доля ошибок — errors/out, не выше 100%.
func TestTopFailures_Rate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		out, err float64
		want     float64
	}{
		{name: "половина исходящих провалилась", out: 100, err: 50, want: 0.5},
		{name: "все исходящие провалились", out: 40, err: 40, want: 1},
		{name: "экстраполяция increase(): ошибок больше исходящих", out: 9.6, err: 10.2, want: 1},
		{name: "исходящих нет", out: 0, err: 3, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows := topFailures(map[port.NodeKey]port.NodeThroughput{{Team: "t", Path: "p"}: {Out: tt.out, Errors: tt.err}})
			require.Len(t, rows, 1)
			assert.InDelta(t, tt.want, rows[0].Rate, 1e-9)
		})
	}
}

func TestKafkaTest_AllBrokersOK(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{pings: []port.BrokerPing{
		{Addr: "k1:9092", OK: true}, {Addr: "k2:9092", OK: true},
	}}
	uc := NewKafkaMonitorUsecase(nil, admin, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Test(context.Background())
	assert.True(t, r.OK)
	assert.True(t, r.KafkaAvailable)
	assert.Len(t, r.Brokers, 2)
}

func TestKafkaTest_OneBrokerDown(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{pings: []port.BrokerPing{
		{Addr: "k1:9092", OK: true}, {Addr: "k2:9092", OK: false, Warn: "unreachable"},
	}}
	uc := NewKafkaMonitorUsecase(nil, admin, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Test(context.Background())
	assert.False(t, r.OK)
	assert.True(t, r.KafkaAvailable)
}

// lagSeries — ряд lag с шагом 30 с от момента 0.
func lagSeries(vals ...float64) []port.KafkaPoint {
	pts := make([]port.KafkaPoint, len(vals))
	for i, v := range vals {
		pts[i] = port.KafkaPoint{TsMs: int64(i) * 30_000, V: v}
	}
	return pts
}

// TestSteadyGrowthPerSec (§113.8): «lag растёт» — только рост без единого
// снижения на всём окне; пила разбираемой очереди тревогой не считается.
func TestSteadyGrowthPerSec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pts  []port.KafkaPoint
		want float64
	}{
		{name: "рост все 5 минут", pts: lagSeries(0, 300, 600, 900, 1200, 1500, 1800, 2100, 2400, 2700, 3000), want: 10},
		{name: "плато внутри роста — всё ещё рост", pts: lagSeries(0, 300, 300, 900, 1200, 1500, 1800, 2100, 2400, 2700, 3000), want: 10},
		{name: "пила: концы выше начала, но был спад",
			pts: lagSeries(0, 3000, 100, 3000, 100, 3000, 100, 3000, 100, 3000, 3000)},
		{name: "дыра в данных — меньше 4 минут", pts: lagSeries(0, 3000, 6000)},
		// Ревизия §113: крайние точки покрывают окно, но внутри пропуск 3 минуты
		// (рестарт Sender'а) — спад мог прийтись на него, это не «рост».
		{name: "разрыв внутри окна", pts: func() []port.KafkaPoint {
			pts := lagSeries(0, 300, 600, 900, 1200, 1500, 1800, 2100, 2400, 2700, 3000)
			return append(pts[:3], pts[9:]...)
		}()},
		{name: "одна точка", pts: lagSeries(5)},
		{name: "пусто"},
		{name: "убывает", pts: lagSeries(3000, 2700, 2400, 2100, 1800, 1500, 1200, 900, 600, 300, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, steadyGrowthPerSec(tt.pts, lagGrowMinSpan, 2*lagGrowStep), 1e-9)
		})
	}
}

// TestKafkaOverview_HealthSignals (§113.8): p95 публикации и устойчивый рост
// lag доходят до health-баннера; пила lag — нет.
func TestKafkaOverview_HealthSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		prom       *fakeProm
		wantSev    string
		wantReason string
	}{
		{name: "p95 публикации выше критического порога",
			prom:    &fakeProm{kafkaOv: port.KafkaSummary{ProduceP95ms: 800}},
			wantSev: SeverityErr, wantReason: ReasonProduceLatency},
		{name: "p95 выше порога предупреждения",
			prom:    &fakeProm{kafkaOv: port.KafkaSummary{ProduceP95ms: 150}},
			wantSev: SeverityWarn, wantReason: ReasonProduceLatency},
		{name: "lag растёт все 5 минут",
			prom: &fakeProm{kafkaSeries: map[string][]port.KafkaPoint{
				"lag": lagSeries(0, 3000, 6000, 9000, 12000, 15000, 18000, 21000, 24000, 27000, 30000)}},
			wantSev: SeverityErr, wantReason: ReasonLagGrowing},
		{name: "пила lag — не рост",
			prom: &fakeProm{kafkaSeries: map[string][]port.KafkaPoint{
				"lag": lagSeries(0, 30000, 0, 30000, 0, 30000, 0, 30000, 0, 30000, 30000)}},
			wantSev: SeverityOK, wantReason: ReasonHealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			uc := NewKafkaMonitorUsecase(tt.prom, nil, nil, nil, defaultTh(), logging.NewNoop())
			since, until := kafkaWindow()
			r := uc.Overview(context.Background(), since, until)
			assert.Equal(t, tt.wantSev, r.Health.Severity)
			assert.Equal(t, tt.wantReason, r.Health.Reason)
		})
	}
}
