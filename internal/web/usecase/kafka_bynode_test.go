package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/web/usecase/port"
)

// fakeNodePaths — port.NodePathResolver с записью запрошенных путей: тест
// проверяет не только результат, но и то, ЧТО именно спросили у базы.
type fakeNodePaths struct {
	ids   map[string]string
	err   error
	asked []string
}

func (f *fakeNodePaths) IDsByPaths(_ context.Context, paths []string) (map[string]string, error) {
	f.asked = append(f.asked, paths...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, p := range paths {
		if id, ok := f.ids[p]; ok {
			out[p] = id
		}
	}
	return out, nil
}

// §98.2/§101.9: строки top-узлов получают id по точной паре (команда, путь).
func TestKafkaByNode_ResolvesNodeIDs(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{
		asyncThroughput: map[port.NodeKey]port.NodeThroughput{
			{Team: "fin", Path: "billing"}:    {Out: 800},
			{Team: "geo", Path: "geo/notify"}: {Out: 200},
			{Path: metrics.NodeUnresolved}:    {Out: 50},
			{Team: "fin", Path: "gone"}:       {Out: 30},
		},
		failures: map[port.NodeKey]port.NodeThroughput{
			{Team: "geo", Path: "geo/notify"}: {Out: 200, Errors: 7},
			{Path: metrics.NodeUnresolved}:    {Out: 50, Errors: 5},
			{Team: "fin", Path: "gone"}:       {Out: 30, Errors: 3},
		},
	}
	// "gone" — узла уже нет: резолвер его не отдаёт.
	nodes := &fakeNodeKeys{refs: []port.NodeKeyRef{
		{ID: "n-1", Key: port.NodeKey{Team: "fin", Path: "billing"}},
		{ID: "n-2", Key: port.NodeKey{Team: "geo", Path: "geo/notify"}},
	}}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()

	r := uc.ByNode(context.Background(), since, until)

	byPath := map[string]NodeProducer{}
	for _, p := range r.TopProducers {
		byPath[p.NodePath] = p
	}
	assert.Equal(t, "n-1", byPath["billing"].NodeID)
	assert.Equal(t, "fin", byPath["billing"].Team, "§101.9: слог команды приходит с сервера")
	assert.Equal(t, "n-2", byPath["geo/notify"].NodeID)
	assert.Empty(t, byPath["gone"].NodeID, "узла нет — ссылка вела бы в никуда")
	// §94.8: метка-заглушка узлом не является — про неё базу даже не спрашивают,
	// иначе экран предлагал бы открыть «узел <unresolved>».
	assert.Empty(t, byPath[metrics.NodeUnresolved].NodeID)
	assert.NotContains(t, nodes.askedPaths(), metrics.NodeUnresolved)

	// Один путь спрашивается один раз, даже когда он есть в ОБОИХ блоках
	// (geo/notify и gone с ошибками попадают и в producers, и в failures).
	require.NotEmpty(t, r.TopFailures)
	assert.ElementsMatch(t, []string{"billing", "geo/notify", "gone"}, nodes.askedPaths())
	for _, f := range r.TopFailures {
		if f.NodePath == "geo/notify" {
			assert.Equal(t, "n-2", f.NodeID)
		}
	}
}

// §101.9 — боевой случай: путь telegram в командах messengers и webhook. До
// §101 это была одна строка 1 755 без ссылки (путь неоднозначен); теперь две
// строки, у каждой своя команда, число и ссылка.
func TestKafkaByNode_SameNamePathsInTwoTeams(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{
		asyncThroughput: map[port.NodeKey]port.NodeThroughput{
			{Team: "messengers", Path: "telegram"}: {Out: 7200},
			{Team: "webhook", Path: "telegram"}:    {Out: 220},
		},
		failures: map[port.NodeKey]port.NodeThroughput{
			{Team: "messengers", Path: "telegram"}: {Out: 7200, Errors: 1747},
			{Team: "webhook", Path: "telegram"}:    {Out: 220, Errors: 8},
		},
	}
	nodes := &fakeNodeKeys{refs: []port.NodeKeyRef{
		{ID: "id-messengers", Key: port.NodeKey{Team: "messengers", Path: "telegram"}},
		{ID: "id-webhook", Key: port.NodeKey{Team: "webhook", Path: "telegram"}},
	}}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()

	r := uc.ByNode(context.Background(), since, until)

	require.Len(t, r.TopFailures, 2)
	first, second := r.TopFailures[0], r.TopFailures[1]
	assert.Equal(t, "telegram", first.NodePath)
	assert.Equal(t, "messengers", first.Team)
	assert.Equal(t, "id-messengers", first.NodeID)
	assert.EqualValues(t, 1747, first.Failed)
	assert.InDelta(t, 1747.0/7200.0, first.Rate, 1e-9) // §113.8: errors/out
	assert.Equal(t, "webhook", second.Team)
	assert.Equal(t, "id-webhook", second.NodeID)
	assert.EqualValues(t, 8, second.Failed)

	require.Len(t, r.TopProducers, 2)
	assert.ElementsMatch(t, []string{"id-messengers", "id-webhook"},
		[]string{r.TopProducers[0].NodeID, r.TopProducers[1].NodeID})
}

// §101.9: в окне выката часть трафика — ряды до обновления, без команды.
// Однозначный путь сворачивается в строку узла (числа и порядок — после
// свёртки), неоднозначный остаётся строкой без команды и без ссылки. Свёртка
// идёт в обоих срезах, а пути у базы спрашиваются одним запросом.
func TestKafkaByNode_LegacyRowsFold(t *testing.T) {
	t.Parallel()
	slice := func() map[port.NodeKey]port.NodeThroughput {
		return map[port.NodeKey]port.NodeThroughput{
			{Team: "fin", Path: "billing"}:  {Out: 100, Errors: 1},
			{Path: "billing"}:               {Out: 900, Errors: 2},
			{Team: "msg", Path: "telegram"}: {Out: 50},
			{Path: "telegram"}:              {Out: 400, Errors: 412},
			{Path: metrics.NodeUnresolved}:  {Out: 10},
		}
	}
	prom := &fakeProm{asyncThroughput: slice(), failures: slice()}
	nodes := &fakeNodeKeys{refs: []port.NodeKeyRef{
		{ID: "n-bill", Key: port.NodeKey{Team: "fin", Path: "billing"}},
		{ID: "n-tg-msg", Key: port.NodeKey{Team: "msg", Path: "telegram"}},
		{ID: "n-tg-wh", Key: port.NodeKey{Team: "wh", Path: "telegram"}},
	}}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()

	r := uc.ByNode(context.Background(), since, until)

	require.Len(t, r.TopProducers, 4)
	assert.Equal(t, NodeProducer{NodePath: "billing", Team: "fin", NodeID: "n-bill", Produced: 1000,
		Share: r.TopProducers[0].Share}, r.TopProducers[0], "900 до §101 + 100 после — одна строка узла")
	assert.Equal(t, "telegram", r.TopProducers[1].NodePath)
	assert.Empty(t, r.TopProducers[1].Team, "путь в двух командах — строка без команды")
	assert.Empty(t, r.TopProducers[1].NodeID)
	for _, p := range r.TopProducers {
		assert.False(t, p.NodePath == "billing" && p.Team == "", "ряд до §101 свёрнут, отдельной строки нет")
	}

	require.Len(t, r.TopFailures, 2)
	assert.Equal(t, NodeFailure{NodePath: "telegram", Failed: 412, Rate: r.TopFailures[0].Rate}, r.TopFailures[0])
	assert.Equal(t, "fin", r.TopFailures[1].Team)
	assert.EqualValues(t, 3, r.TopFailures[1].Failed)

	// Каждый путь спрошен дважды: один раз свёрткой (хотя ряды до §101 есть в
	// обоих срезах) и один раз резолвом id строк fin/billing и msg/telegram.
	// Свёртка по каждому срезу отдельно дала бы по три.
	asked := nodes.askedPaths()
	assert.Equal(t, 2, countOf(asked, "telegram"), asked)
	assert.Equal(t, 2, countOf(asked, "billing"), asked)
}

func countOf(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

// Резолвер — украшение: без него и при его ошибке экран обязан работать, а ряды
// до §101 — остаться отдельной строкой, а не приписаться кому попало.
func TestKafkaByNode_ResolverDegrades(t *testing.T) {
	t.Parallel()
	since, until := kafkaWindow()

	t.Run("резолвера нет вовсе", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{asyncThroughput: map[port.NodeKey]port.NodeThroughput{{Team: "fin", Path: "billing"}: {Out: 10}}}
		uc := NewKafkaMonitorUsecase(prom, nil, nil, nil, defaultTh(), logging.NewNoop())
		r := uc.ByNode(context.Background(), since, until)
		require.Len(t, r.TopProducers, 1)
		assert.Empty(t, r.TopProducers[0].NodeID)
		assert.Equal(t, "billing", r.TopProducers[0].NodePath)
	})

	t.Run("резолвер упал", func(t *testing.T) {
		t.Parallel()
		prom := &fakeProm{asyncThroughput: map[port.NodeKey]port.NodeThroughput{
			{Team: "fin", Path: "billing"}: {Out: 10},
			{Path: "billing"}:              {Out: 5},
		}}
		nodes := &fakeNodeKeys{err: errors.New("pg down")}
		uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
		r := uc.ByNode(context.Background(), since, until)
		require.Len(t, r.TopProducers, 2, "без проверки однозначности ряд до §101 не сворачивается")
		for _, p := range r.TopProducers {
			assert.Empty(t, p.NodeID)
		}
		assert.True(t, r.PrometheusAvailable)
	})
}
