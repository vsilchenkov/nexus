package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

var testKafkaGroups = KafkaGroupNames{
	Main: "nexus-sender", Paused: "nexus-sender-paused", DLQ: "nexus-sender-dlq-reprocess",
	Retry: "nexus-sender-clog-retry", MainTopic: "nexus.async", PausedTopic: "nexus.async.paused",
}

// fakeGroupLag — lag групп из «брокера» и счётчик обращений к нему. Группы
// читаются параллельно (§114.4) — счётчик под mutex; wait — хук «брокер
// завис», общий для всех групп.
type fakeGroupLag struct {
	lags  map[string]int64
	errs  map[string]error
	wait  func(context.Context) error
	mu    sync.Mutex
	calls int
}

func (f *fakeGroupLag) GroupLag(ctx context.Context, group, _ string) (int64, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.wait != nil {
		if err := f.wait(ctx); err != nil {
			return 0, err
		}
	}
	return f.lags[group], f.errs[group]
}

type fakePausedNodes struct {
	refs  []port.NodeKeyRef
	err   error
	calls int
}

func (f *fakePausedNodes) PausedNodes(context.Context) ([]port.NodeKeyRef, error) {
	f.calls++
	return f.refs, f.err
}

func pausedRef(i int) port.NodeKeyRef {
	return port.NodeKeyRef{ID: fmt.Sprintf("id-%d", i), Key: port.NodeKey{Team: "edo", Path: fmt.Sprintf("node-%d", i)}}
}

// TestKafkaOverview_BrokerLag (§113.7): текущий lag — основная группа из
// брокера, Prometheus — запасной путь; очередь паузы — отдельным полем вместе
// с узлами на паузе и в lag/health не входит.
func TestKafkaOverview_BrokerLag(t *testing.T) {
	t.Parallel()
	many := make([]port.NodeKeyRef, 25)
	for i := range many {
		many[i] = pausedRef(i)
	}
	tests := []struct {
		name          string
		lag           *fakeGroupLag // nil — источник не подключён
		paused        *fakePausedNodes
		wantLag       uint64
		wantBroker    bool
		wantBacklog   uint64
		wantNodes     int
		wantTotal     int
		wantAvailable bool
		wantListed    int // обращений к списку узлов
	}{
		{name: "lag из брокера вместо Prometheus",
			lag:     &fakeGroupLag{lags: map[string]int64{"nexus-sender": 3}},
			paused:  &fakePausedNodes{},
			wantLag: 3, wantBroker: true},
		{name: "брокер не ответил — lag из Prometheus",
			lag:     &fakeGroupLag{errs: map[string]error{"nexus-sender": errors.New("dial")}},
			paused:  &fakePausedNodes{},
			wantLag: 900},
		{name: "источник не подключён — lag из Prometheus", paused: &fakePausedNodes{}, wantLag: 900},
		{name: "очередь паузы с узлом",
			lag:        &fakeGroupLag{lags: map[string]int64{"nexus-sender-paused": 960}},
			paused:     &fakePausedNodes{refs: []port.NodeKeyRef{pausedRef(1)}},
			wantBroker: true, wantBacklog: 960, wantNodes: 1, wantTotal: 1, wantAvailable: true, wantListed: 1},
		{name: "очередь паузы без узлов на паузе",
			lag:        &fakeGroupLag{lags: map[string]int64{"nexus-sender-paused": 960}},
			paused:     &fakePausedNodes{refs: []port.NodeKeyRef{}},
			wantBroker: true, wantBacklog: 960, wantAvailable: true, wantListed: 1},
		{name: "список узлов не прочитан — не утверждаем, что их нет",
			lag:        &fakeGroupLag{lags: map[string]int64{"nexus-sender-paused": 960}},
			paused:     &fakePausedNodes{err: errors.New("pg down")},
			wantBroker: true, wantBacklog: 960, wantListed: 1},
		{name: "длинный список обрезается, число — полное",
			lag:        &fakeGroupLag{lags: map[string]int64{"nexus-sender-paused": 5}},
			paused:     &fakePausedNodes{refs: many},
			wantBroker: true, wantBacklog: 5, wantNodes: maxPausedNodesListed, wantTotal: 25,
			wantAvailable: true, wantListed: 1},
		{name: "очередь паузы пуста — PostgreSQL не спрашиваем",
			lag:        &fakeGroupLag{lags: map[string]int64{}},
			paused:     &fakePausedNodes{refs: []port.NodeKeyRef{pausedRef(1)}},
			wantBroker: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := []KafkaMonitorOption{WithKafkaGroups(testKafkaGroups), WithPausedNodes(tt.paused)}
			if tt.lag != nil {
				opts = append(opts, WithKafkaGroupLag(tt.lag))
			}
			prom := &fakeProm{kafkaOv: port.KafkaSummary{CurrentLag: 900}}
			uc := NewKafkaMonitorUsecase(prom, nil, nil, nil, defaultTh(), logging.NewNoop(), opts...)

			since, until := kafkaWindow()
			r := uc.Overview(context.Background(), since, until)

			assert.Equal(t, tt.wantLag, r.Summary.CurrentLag)
			assert.Equal(t, tt.wantBroker, r.LagFromBroker)
			assert.Equal(t, tt.wantBacklog, r.Summary.PausedBacklog)
			assert.Len(t, r.PausedNodes, tt.wantNodes)
			assert.Equal(t, tt.wantTotal, r.PausedNodesTotal)
			assert.Equal(t, tt.wantAvailable, r.PausedNodesAvailable)
			assert.Equal(t, tt.wantListed, tt.paused.calls)
		})
	}
}

// TestKafkaOverview_PausedBacklogNotInHealth: большая очередь паузы при пустой
// основной группе — не тревога (до §113 именно она давала баннер «Высокий
// consumer lag»).
func TestKafkaOverview_PausedBacklogNotInHealth(t *testing.T) {
	t.Parallel()
	lag := &fakeGroupLag{lags: map[string]int64{"nexus-sender-paused": 50_000}}
	uc := NewKafkaMonitorUsecase(&fakeProm{}, nil, nil, nil, defaultTh(), logging.NewNoop(),
		WithKafkaGroups(testKafkaGroups), WithKafkaGroupLag(lag), WithPausedNodes(&fakePausedNodes{}))

	since, until := kafkaWindow()
	r := uc.Overview(context.Background(), since, until)

	assert.Zero(t, r.Summary.CurrentLag)
	assert.EqualValues(t, 50_000, r.Summary.PausedBacklog)
	assert.Equal(t, SeverityOK, r.Health.Severity)
}

// TestKafkaOverview_BrokerLagCached: lag кешируется на TTL адаптера кеша —
// автообновление экрана не ходит в брокер каждые 10 секунд.
func TestKafkaOverview_BrokerLagCached(t *testing.T) {
	t.Parallel()
	lag := &fakeGroupLag{lags: map[string]int64{"nexus-sender": 7, "nexus-sender-paused": 2}}
	uc := NewKafkaMonitorUsecase(nil, nil, newMemKafkaCache(), nil, defaultTh(), logging.NewNoop(),
		WithKafkaGroups(testKafkaGroups), WithKafkaGroupLag(lag), WithPausedNodes(&fakePausedNodes{}))

	since, until := kafkaWindow()
	first := uc.Overview(context.Background(), since, until)
	second := uc.Overview(context.Background(), since, until)

	assert.Equal(t, 2, lag.calls, "второй вызов — из кеша")
	assert.Equal(t, first.Summary, second.Summary)
	assert.EqualValues(t, 7, second.Summary.CurrentLag)
	assert.EqualValues(t, 2, second.Summary.PausedBacklog)
}

// TestKafkaTopics_GroupRoles (§113.7): группам Nexus проставляются роли по
// именам из конфига, в том числе у метаданных из кеша; посторонняя группа —
// без роли.
func TestKafkaTopics_GroupRoles(t *testing.T) {
	t.Parallel()
	admin := &fakeKafkaAdmin{topics: []port.TopicInfo{
		{Name: "nexus.async", ConsumerGroups: []port.TopicConsumerGroup{{Group: "nexus-sender"}, {Group: "debug-tool"}}},
		{Name: "nexus.async.paused", ConsumerGroups: []port.TopicConsumerGroup{{Group: "nexus-sender-paused", LagTotal: 960}}},
		{Name: "nexus.async.dlq", ConsumerGroups: []port.TopicConsumerGroup{{Group: "nexus-sender-dlq-reprocess"}}},
		{Name: "nexus.clog.retry", ConsumerGroups: []port.TopicConsumerGroup{{Group: "nexus-sender-clog-retry"}}},
	}}
	uc := NewKafkaMonitorUsecase(nil, admin, newMemKafkaCache(), nil, defaultTh(), logging.NewNoop(),
		WithKafkaGroups(testKafkaGroups))

	for _, pass := range []string{"свежие метаданные", "из кеша"} {
		r := uc.Topics(context.Background())
		require.Len(t, r.Topics, 4, pass)
		roles := map[string]string{}
		for _, tp := range r.Topics {
			for _, g := range tp.ConsumerGroups {
				roles[g.Group] = g.Role
			}
		}
		assert.Equal(t, map[string]string{
			"nexus-sender":               port.KafkaGroupRoleMain,
			"debug-tool":                 "",
			"nexus-sender-paused":        port.KafkaGroupRolePaused,
			"nexus-sender-dlq-reprocess": port.KafkaGroupRoleDLQ,
			"nexus-sender-clog-retry":    port.KafkaGroupRoleRetry,
		}, roles, pass)
	}
}

// uncommittedTopics — ответ адаптера (§114.6): у основной группы и у
// посторонней часть lag приходится на партиции без commit.
func uncommittedTopics() []port.TopicInfo {
	return []port.TopicInfo{{Name: "nexus.async", ConsumerGroups: []port.TopicConsumerGroup{
		{Group: "nexus-sender", LagTotal: 10, LagUncommitted: 4, UncommittedPartitions: 1},
		{Group: "debug-tool", LagTotal: 15, LagUncommitted: 3, UncommittedPartitions: 3},
	}}}
}

// TestKafkaTopics_ThirdPartyUncommitted (§114.6): у посторонней группы lag
// партиций без commit вычитается и показывается числом партиций; у группы
// Nexus он настоящий (reader начнёт с First) и остаётся в lag. Без кеша фейк
// отдаёт один и тот же срез, поэтому второй проход проверяет и то, что правится
// копия: иначе lag посторонней группы вычитался бы при каждом запросе.
func TestKafkaTopics_ThirdPartyUncommitted(t *testing.T) {
	t.Parallel()
	for _, cache := range []port.KafkaCache{nil, newMemKafkaCache()} {
		admin := &fakeKafkaAdmin{topics: uncommittedTopics()}
		uc := NewKafkaMonitorUsecase(nil, admin, cache, nil, defaultTh(), logging.NewNoop(),
			WithKafkaGroups(testKafkaGroups))
		for pass := range 2 {
			r := uc.Topics(context.Background())
			require.Len(t, r.Topics, 1)
			groups := r.Topics[0].ConsumerGroups
			require.Len(t, groups, 2)
			assert.EqualValues(t, 10, groups[0].LagTotal, "группа Nexus, проход %d", pass)
			assert.Zero(t, groups[0].UncommittedPartitions, "группа Nexus, проход %d", pass)
			assert.EqualValues(t, 12, groups[1].LagTotal, "посторонняя группа, проход %d", pass)
			assert.Equal(t, 3, groups[1].UncommittedPartitions, "посторонняя группа, проход %d", pass)
		}
		assert.Equal(t, uncommittedTopics(), admin.topics, "ответ адаптера не тронут")
	}
}

// TestKafkaGroupNames_RoleEmptyName: пустое имя группы в конфиге не делает
// группу без имени «основной».
func TestKafkaGroupNames_RoleEmptyName(t *testing.T) {
	t.Parallel()
	assert.Empty(t, KafkaGroupNames{}.role(""))
	assert.Empty(t, KafkaGroupNames{}.role("x"))
}
