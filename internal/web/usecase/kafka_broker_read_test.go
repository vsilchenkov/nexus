package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// hangUntilDone — «брокер завис»: чтение ждёт, пока его не оборвёт бюджет.
func hangUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// barrier — чтение ждёт, пока не придут все n чтений. Последовательные чтения
// друг друга не дождутся и упрутся в бюджет — так тест отличает параллельные
// чтения от последовательных без замеров времени.
func barrier(n int) func(context.Context) error {
	var (
		mu      sync.Mutex
		arrived int
	)
	all := make(chan struct{})
	return func(ctx context.Context) error {
		mu.Lock()
		arrived++
		if arrived == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// brokerUC — экран Kafka со всеми источниками брокера; nil-источник не
// подключается (typed-nil в интерфейсе дал бы панику вместо деградации).
func brokerUC(admin *fakeKafkaAdmin, lag *fakeGroupLag, cache port.KafkaCache, opts ...KafkaMonitorOption) *KafkaMonitorUsecase {
	var a port.KafkaAdmin
	if admin != nil {
		a = admin
	}
	all := []KafkaMonitorOption{WithKafkaGroups(testKafkaGroups), WithPausedNodes(&fakePausedNodes{})}
	if lag != nil {
		all = append(all, WithKafkaGroupLag(lag))
	}
	prom := &fakeProm{kafkaOv: port.KafkaSummary{CurrentLag: 5}}
	return NewKafkaMonitorUsecase(prom, a, cache, nil, defaultTh(), logging.NewNoop(), append(all, opts...)...)
}

// TestKafkaOverview_BrokerReadsParallel (§114.4): три чтения брокера идут
// одновременно — иначе барьер не откроется, и каждое упрётся в бюджет.
func TestKafkaOverview_BrokerReadsParallel(t *testing.T) {
	t.Parallel()
	wait := barrier(3)
	admin := &fakeKafkaAdmin{health: port.BrokerHealth{BrokersTotal: 1, BrokersOnline: 1}, wait: wait}
	lag := &fakeGroupLag{lags: map[string]int64{"nexus-sender": 4}, wait: wait}
	uc := brokerUC(admin, lag, nil, WithKafkaBrokerBudget(KafkaBrokerBudget{Overview: 5 * time.Second}))

	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)

	assert.True(t, r.KafkaAvailable)
	assert.True(t, r.LagFromBroker)
	assert.EqualValues(t, 4, r.Summary.CurrentLag)
	assert.True(t, r.PausedBacklogAvailable)
	assert.Equal(t, ReasonHealthy, r.Health.Reason)
}

// TestKafkaOverview_BrokerHangsWithinBudget (§114.4, §114.5): зависший брокер
// не держит ответ дольше бюджета (без бюджета тест висел бы до таймаута), а
// экран честно говорит, что брокер не ответил.
func TestKafkaOverview_BrokerHangsWithinBudget(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{health: port.BrokerHealth{BrokersTotal: 1, BrokersOnline: 1}, wait: hangUntilDone}
	lag := &fakeGroupLag{lags: map[string]int64{"nexus-sender": 4}, wait: hangUntilDone}
	uc := brokerUC(admin, lag, nil, WithKafkaBrokerBudget(KafkaBrokerBudget{Overview: 50 * time.Millisecond}))

	since, until := kafkaWindow()
	start := time.Now()
	r := uc.Overview(context.Background(), since, until)

	assert.Less(t, time.Since(start), 2*time.Second)
	assert.False(t, r.KafkaAvailable)
	assert.False(t, r.PausedBacklogAvailable)
	assert.False(t, r.LagFromBroker)
	assert.EqualValues(t, 5, r.Summary.CurrentLag, "lag — из Prometheus")
	assert.Zero(t, r.BrokerHealth)
	assert.Equal(t, HealthBanner{SeverityWarn, ReasonBrokerUnreachable}, r.Health)
}

// TestKafkaOverview_AvailabilityFlags (§114.5): kafka_available — все
// настроенные чтения брокера удались; paused_backlog_available — очередь паузы
// прочитана или не настроена.
func TestKafkaOverview_AvailabilityFlags(t *testing.T) {
	t.Parallel()
	dial := errors.New("dial")
	okHealth := func() *fakeKafkaAdmin {
		return &fakeKafkaAdmin{health: port.BrokerHealth{BrokersTotal: 1, BrokersOnline: 1}}
	}
	tests := []struct {
		name       string
		admin      *fakeKafkaAdmin
		lag        *fakeGroupLag
		groups     *KafkaGroupNames // nil — testKafkaGroups
		wantKafka  bool
		wantPaused bool
		wantBroker bool
		wantReason string
	}{
		{name: "всё прочитано", admin: okHealth(), lag: &fakeGroupLag{},
			wantKafka: true, wantPaused: true, wantBroker: true, wantReason: ReasonHealthy},
		{name: "очередь паузы не прочитана", admin: okHealth(),
			lag:        &fakeGroupLag{errs: map[string]error{"nexus-sender-paused": dial}},
			wantBroker: true, wantReason: ReasonBrokerUnreachable},
		{name: "lag основной группы не прочитан", admin: okHealth(),
			lag:        &fakeGroupLag{errs: map[string]error{"nexus-sender": dial}},
			wantPaused: true, wantReason: ReasonBrokerUnreachable},
		{name: "состояние брокеров не прочитано", admin: &fakeKafkaAdmin{healthErr: dial}, lag: &fakeGroupLag{},
			wantPaused: true, wantBroker: true, wantReason: ReasonBrokerUnreachable},
		{name: "Kafka не настроена", wantPaused: true, wantReason: ReasonHealthy},
		{name: "очередь паузы не настроена", admin: okHealth(),
			lag:       &fakeGroupLag{errs: map[string]error{"nexus-sender-paused": dial}},
			groups:    &KafkaGroupNames{Main: "nexus-sender", MainTopic: "nexus.async"},
			wantKafka: true, wantPaused: true, wantBroker: true, wantReason: ReasonHealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var opts []KafkaMonitorOption
			if tt.groups != nil {
				opts = append(opts, WithKafkaGroups(*tt.groups))
			}
			uc := brokerUC(tt.admin, tt.lag, nil, opts...)

			since, until := kafkaWindow()
			r := uc.Overview(context.Background(), since, until)

			assert.Equal(t, tt.wantKafka, r.KafkaAvailable, "kafka_available")
			assert.Equal(t, tt.wantPaused, r.PausedBacklogAvailable, "paused_backlog_available")
			assert.Equal(t, tt.wantBroker, r.LagFromBroker, "lag_from_broker")
			assert.Equal(t, tt.wantReason, r.Health.Reason)
		})
	}
}

// TestKafkaOverview_FailureCached (§114.4): неудача кешируется — следующее
// автообновление не платит бюджет снова.
func TestKafkaOverview_FailureCached(t *testing.T) {
	t.Parallel()
	dial := errors.New("dial")
	admin := &fakeKafkaAdmin{healthErr: dial}
	lag := &fakeGroupLag{errs: map[string]error{"nexus-sender": dial, "nexus-sender-paused": dial}}
	uc := brokerUC(admin, lag, newMemKafkaCache())

	since, until := kafkaWindow()
	uc.Overview(context.Background(), since, until)
	second := uc.Overview(context.Background(), since, until)

	assert.EqualValues(t, 1, admin.healthCalls.Load(), "состояние брокеров — из кеша")
	assert.Equal(t, 2, lag.calls, "lag обеих групп — из кеша")
	assert.False(t, second.KafkaAvailable)
	assert.False(t, second.PausedBacklogAvailable)
	assert.Equal(t, ReasonBrokerUnreachable, second.Health.Reason)
}

// TestKafkaTopics_FailureCached (§114.4): неудачный обход топиков — не «топиков
// нет», а kafka_available=false, и в пределах TTL брокер не спрашивается.
func TestKafkaTopics_FailureCached(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topicsErr: errors.New("list groups: dial")}
	uc := brokerUC(admin, nil, newMemKafkaCache())

	first := uc.Topics(context.Background())
	second := uc.Topics(context.Background())

	assert.False(t, first.KafkaAvailable)
	assert.False(t, second.KafkaAvailable)
	assert.NotNil(t, second.Topics, "в JSON — [], а не null")
	assert.EqualValues(t, 1, admin.topicsCalls.Load())
}

// TestKafkaTopics_ReadOutlivesCanceledRequest (§114.4): закрытая вкладка
// первого пришедшего не обрывает чтение — оно доходит до кеша удачным, и
// следующий запрос получает данные без нового обхода брокера.
func TestKafkaTopics_ReadOutlivesCanceledRequest(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	admin := &fakeKafkaAdmin{
		topics: []port.TopicInfo{{Name: "nexus.async"}},
		wait: func(ctx context.Context) error {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	cache := newMemKafkaCache()
	uc := brokerUC(admin, nil, cache, WithKafkaBrokerBudget(KafkaBrokerBudget{Topics: 5 * time.Second}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan KafkaTopicsResult, 1)
	go func() { done <- uc.Topics(ctx) }()
	<-started
	cancel()
	assert.False(t, (<-done).KafkaAvailable, "ушедший запрос ответа не ждёт")

	close(release)
	require.Eventually(t, func() bool { return cache.has(brokerKeyTopics) }, 2*time.Second, 5*time.Millisecond)
	r := uc.Topics(context.Background())
	assert.True(t, r.KafkaAvailable)
	assert.Len(t, r.Topics, 1)
	assert.EqualValues(t, 1, admin.topicsCalls.Load())
}

// TestCloneTopics: правка копии (роли, размеры) не задевает общий срез
// singleflight, а nil даёт пустой срез.
func TestCloneTopics(t *testing.T) {
	t.Parallel()
	src := []port.TopicInfo{{Name: "a", ConsumerGroups: []port.TopicConsumerGroup{{Group: "g"}}}}
	cp := cloneTopics(src)
	cp[0].SizeBytes = 42
	cp[0].ConsumerGroups[0].Role = port.KafkaGroupRoleMain

	assert.Zero(t, src[0].SizeBytes)
	assert.Empty(t, src[0].ConsumerGroups[0].Role)
	assert.NotNil(t, cloneTopics(nil))
}
