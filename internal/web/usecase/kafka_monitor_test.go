package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// fakeKafkaAdmin — конфигурируемый port.KafkaAdmin для тестов usecase.
type fakeKafkaAdmin struct {
	topics    []port.TopicInfo
	topicsErr error
	health    port.BrokerHealth
	healthErr error
	pings     []port.BrokerPing
	pingErr   error
}

func (f *fakeKafkaAdmin) Topics(context.Context) ([]port.TopicInfo, error) {
	return f.topics, f.topicsErr
}
func (f *fakeKafkaAdmin) BrokerHealth(context.Context) (port.BrokerHealth, error) {
	return f.health, f.healthErr
}
func (f *fakeKafkaAdmin) Ping(context.Context) ([]port.BrokerPing, error) {
	return f.pings, f.pingErr
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
	uc := NewKafkaMonitorUsecase(prom, admin, nil, defaultTh(), logging.NewNoop())

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
	uc := NewKafkaMonitorUsecase(nil, nil, nil, defaultTh(), logging.NewNoop())
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
	uc := NewKafkaMonitorUsecase(&fakeProm{}, admin, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)
	assert.False(t, r.KafkaAvailable)
}

func TestKafkaTopics(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{{Name: "nexus.async", Partitions: 4}}}
	uc := NewKafkaMonitorUsecase(nil, admin, nil, defaultTh(), logging.NewNoop())
	r := uc.Topics(context.Background())
	require.True(t, r.KafkaAvailable)
	require.Len(t, r.Topics, 1)
	assert.Equal(t, "nexus.async", r.Topics[0].Name)
}

func TestKafkaTopics_NoAdmin(t *testing.T) {
	t.Parallel()
	uc := NewKafkaMonitorUsecase(nil, nil, nil, defaultTh(), logging.NewNoop())
	r := uc.Topics(context.Background())
	assert.False(t, r.KafkaAvailable)
	assert.Empty(t, r.Topics)
}

func TestKafkaByNode_TopProducersAndFailures(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{throughput: map[string]port.NodeThroughput{
		"billing":    {Out: 800, Errors: 0},
		"geo/notify": {Out: 200, Errors: 0},
		"old/legacy": {Out: 100, Errors: 50},
	}}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()
	r := uc.ByNode(context.Background(), since, until)

	require.True(t, r.PrometheusAvailable)
	require.NotEmpty(t, r.TopProducers)
	assert.Equal(t, "billing", r.TopProducers[0].NodePath)
	assert.InDelta(t, 800.0/1100.0, r.TopProducers[0].Share, 1e-9)
	require.Len(t, r.TopFailures, 1)
	assert.Equal(t, "old/legacy", r.TopFailures[0].NodePath)
	assert.InDelta(t, 50.0/150.0, r.TopFailures[0].Rate, 1e-9)
}

func TestKafkaTest_AllBrokersOK(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{pings: []port.BrokerPing{
		{Addr: "k1:9092", OK: true}, {Addr: "k2:9092", OK: true},
	}}
	uc := NewKafkaMonitorUsecase(nil, admin, nil, defaultTh(), logging.NewNoop())
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
	uc := NewKafkaMonitorUsecase(nil, admin, nil, defaultTh(), logging.NewNoop())
	r := uc.Test(context.Background())
	assert.False(t, r.OK)
	assert.True(t, r.KafkaAvailable)
}
