package rejectlog_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/adapter/out/rejectlog"
)

// fakeWriter — писатель-заглушка: копит пачки и умеет отказывать.
type fakeWriter struct {
	mu      sync.Mutex
	batches [][]domain.RejectedAggregate
	err     error
	// block закрывается тестом; пока открыт, Flush висит (проверка того, что
	// Add не ждёт писателя).
	block chan struct{}
}

func (w *fakeWriter) Flush(ctx context.Context, aggs []domain.RejectedAggregate) error {
	if w.block != nil {
		select {
		case <-w.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.batches = append(w.batches, aggs)
	return nil
}

func (w *fakeWriter) all() []domain.RejectedAggregate {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []domain.RejectedAggregate
	for _, b := range w.batches {
		out = append(out, b...)
	}
	return out
}

// fakeDrops — счётчик потерь по причинам.
type fakeDrops struct {
	mu sync.Mutex
	n  map[string]int
}

func newFakeDrops() *fakeDrops { return &fakeDrops{n: map[string]int{}} }

func (d *fakeDrops) IncIngressRejectDropped(cause string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n[cause] += n
}

func (d *fakeDrops) get(cause string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n[cause]
}

// staticHosts — резолвер PTR с фиксированной таблицей.
type staticHosts map[string]string

func (h staticHosts) Lookup(ip string) string { return h[ip] }

func sample(path, ip string) rejectlog.Sample {
	return rejectlog.Sample{
		Key: domain.RejectedGroupKey{
			TeamSlug: "vika", NodePath: path,
			Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST",
		},
		Status:    404,
		ClientIP:  ip,
		UserAgent: "axios/1.6",
		RawPath:   "/api/v1/vika/" + path,
	}
}

// runCollector поднимает коллектор с ручным сбросом: интервал большой, сброс
// вызывается остановкой (Run дописывает накопленное по ctx.Done).
func runCollector(t *testing.T, w rejectlog.Writer, cfg rejectlog.Config, opts ...rejectlog.Option) (*rejectlog.Collector, func()) {
	t.Helper()
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Hour
	}
	c := rejectlog.New(w, cfg, logging.NewNoop(), opts...)
	c.SetEnabled(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	return c, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("collector did not stop")
		}
	}
}

// TestCollectorAggregates: одинаковые отказы схлопываются в одну группу со
// счётчиком, разные пути — в разные группы. Это и есть причина, по которой
// журнал переживает сканер (§94.3).
func TestCollectorAggregates(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	c, stop := runCollector(t, w, rejectlog.Config{})
	for range 5 {
		c.Add(sample("telephony", "10.0.0.1"))
	}
	c.Add(sample("sms", "10.0.0.2"))
	stop()

	aggs := w.all()
	require.Len(t, aggs, 2, "две разные группы")

	byPath := map[string]domain.RejectedAggregate{}
	for _, a := range aggs {
		byPath[a.Key.NodePath] = a
	}
	tel := byPath["telephony"]
	assert.Equal(t, int64(5), tel.Count, "повторы схлопнулись в счётчик")
	require.Len(t, tel.Clients, 1)
	assert.Equal(t, int64(5), tel.Clients[0].Count)
	assert.Equal(t, "10.0.0.1", tel.Clients[0].IP)
	assert.Equal(t, "axios/1.6", tel.Clients[0].UserAgent)
	assert.Equal(t, int64(1), byPath["sms"].Count)
}

// TestCollectorNormalizesKey: пустой слог разворачивается в default ещё до
// агрегации — иначе один узел давал бы две группы в зависимости от формы
// адреса (§78.1).
func TestCollectorNormalizesKey(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	c, stop := runCollector(t, w, rejectlog.Config{})
	s := sample("telephony", "10.0.0.1")
	s.Key.TeamSlug = ""
	c.Add(s)
	s2 := sample("telephony", "10.0.0.1")
	s2.Key.TeamSlug = domain.DefaultTeamSlug
	c.Add(s2)
	stop()

	aggs := w.all()
	require.Len(t, aggs, 1, "обе формы адреса — одна группа")
	assert.Equal(t, domain.DefaultTeamSlug, aggs[0].Key.TeamSlug)
	assert.Equal(t, int64(2), aggs[0].Count)
}

// TestCollectorResolvesHost: PTR-имя подставляется из резолвера (§67).
func TestCollectorResolvesHost(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	hosts := staticHosts{"10.0.0.1": "crm-app01.example.ru"}
	c, stop := runCollector(t, w, rejectlog.Config{}, rejectlog.WithHostResolver(hosts))
	c.Add(sample("telephony", "10.0.0.1"))
	stop()

	aggs := w.all()
	require.Len(t, aggs, 1)
	require.Len(t, aggs[0].Clients, 1)
	assert.Equal(t, "crm-app01.example.ru", aggs[0].Clients[0].Host)
}

// TestCollectorCapsSamples: в памяти держится не больше сэмплов, чем переживёт
// запись, — иначе долгая группа копила бы их без предела.
func TestCollectorCapsSamples(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	c, stop := runCollector(t, w, rejectlog.Config{})
	for i := range domain.RejectedMaxSamplesPerGroup + 15 {
		s := sample("telephony", "10.0.0.1")
		s.RawPath = fmt.Sprintf("/api/v1/vika/telephony?n=%d", i)
		c.Add(s)
	}
	stop()

	aggs := w.all()
	require.Len(t, aggs, 1)
	assert.Len(t, aggs[0].Samples, domain.RejectedMaxSamplesPerGroup)
	assert.Equal(t, int64(domain.RejectedMaxSamplesPerGroup+15), aggs[0].Count,
		"счётчик считает ВСЕ отказы, а не только сохранённые сэмплы")
	// Остаются последние: по ним видно, что происходит сейчас.
	last := aggs[0].Samples[len(aggs[0].Samples)-1]
	assert.Contains(t, last.RawPath, fmt.Sprintf("n=%d", domain.RejectedMaxSamplesPerGroup+14))
}

// TestCollectorCapsClients: число адресов в группе ограничено (§94.3).
func TestCollectorCapsClients(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	c, stop := runCollector(t, w, rejectlog.Config{})
	for i := range domain.RejectedMaxClientsPerGroup + 20 {
		c.Add(sample("telephony", fmt.Sprintf("10.0.1.%d", i)))
	}
	stop()

	aggs := w.all()
	require.Len(t, aggs, 1)
	assert.Len(t, aggs[0].Clients, domain.RejectedMaxClientsPerGroup)
}

// TestCollectorDropsExcessGroups: сканер по случайным путям не съедает память —
// новые группы сверх предела отбрасываются со счётчиком.
func TestCollectorDropsExcessGroups(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	drops := newFakeDrops()
	c, stop := runCollector(t, w, rejectlog.Config{MaxGroups: 3}, rejectlog.WithDropSink(drops))
	for i := range 10 {
		c.Add(sample(fmt.Sprintf("path-%d", i), "10.0.0.1"))
	}
	stop()

	assert.Len(t, w.all(), 3)
	assert.Equal(t, 7, drops.get("groups_full"))
}

// TestCollectorDisabled: выключенный сбор (§94.5, срок хранения 0) не пишет
// ничего и не считает это потерей — запись просто не ведётся.
func TestCollectorDisabled(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	drops := newFakeDrops()
	c := rejectlog.New(w, rejectlog.Config{}, logging.NewNoop(), rejectlog.WithDropSink(drops))
	require.False(t, c.Enabled(), "сбор выключен до явного включения")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()

	c.Add(sample("telephony", "10.0.0.1"))
	cancel()
	<-done

	assert.Empty(t, w.all())
	assert.Zero(t, drops.get("queue_full"))
}

// TestCollectorAddNeverBlocks: Add не ждёт ни агрегатор, ни писателя. Проверка
// прямая — писатель висит, очередь мала, а тысяча вызовов Add укладывается в
// секунду и увеличивает счётчик потерь (§94.4).
func TestCollectorAddNeverBlocks(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{block: make(chan struct{})}
	drops := newFakeDrops()
	c := rejectlog.New(w, rejectlog.Config{QueueSize: 4, FlushInterval: 10 * time.Millisecond},
		logging.NewNoop(), rejectlog.WithDropSink(drops))
	c.SetEnabled(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()

	start := time.Now()
	for range 1000 {
		c.Add(sample("telephony", "10.0.0.1"))
	}
	elapsed := time.Since(start)

	close(w.block)
	cancel()
	<-done

	assert.Less(t, elapsed, time.Second, "Add обязан возвращаться немедленно")
	assert.Positive(t, drops.get("queue_full"), "переполнение очереди считается потерей")
}

// TestCollectorFlushFailureIsCounted: ошибка записи не копит пачку в памяти —
// накопленное теряется, но это видно по счётчику (§94.4).
func TestCollectorFlushFailureIsCounted(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{err: errors.New("pg is down")}
	drops := newFakeDrops()
	c, stop := runCollector(t, w, rejectlog.Config{}, rejectlog.WithDropSink(drops))
	for range 3 {
		c.Add(sample("telephony", "10.0.0.1"))
	}
	stop()

	assert.Empty(t, w.all())
	assert.Equal(t, 3, drops.get("write_failed"), "потеряны все записи пачки, а не одна группа")
}

// TestCollectorPeriodicFlush: сброс идёт по тикеру, а не только при остановке.
func TestCollectorPeriodicFlush(t *testing.T) {
	t.Parallel()

	w := &fakeWriter{}
	c, stop := runCollector(t, w, rejectlog.Config{FlushInterval: 20 * time.Millisecond})
	defer stop()

	c.Add(sample("telephony", "10.0.0.1"))
	require.Eventually(t, func() bool { return len(w.all()) == 1 }, 2*time.Second, 10*time.Millisecond)
}
