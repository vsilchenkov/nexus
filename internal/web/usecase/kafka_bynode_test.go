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

// §98.2: строки top-узлов получают id, чтобы вести на журнал узла.
func TestKafkaByNode_ResolvesNodeIDs(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{throughput: map[string]port.NodeThroughput{
		"billing":              {Out: 800, Errors: 0},
		"geo/notify":           {Out: 200, Errors: 7},
		metrics.NodeUnresolved: {Out: 50, Errors: 5},
		"twin":                 {Out: 30, Errors: 3},
	}}
	// "twin" одноимённый в двух командах — резолвер его не отдаёт вовсе.
	nodes := &fakeNodePaths{ids: map[string]string{"billing": "n-1", "geo/notify": "n-2"}}
	uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
	since, until := kafkaWindow()

	r := uc.ByNode(context.Background(), since, until)

	byPath := map[string]string{}
	for _, p := range r.TopProducers {
		byPath[p.NodePath] = p.NodeID
	}
	assert.Equal(t, "n-1", byPath["billing"])
	assert.Equal(t, "n-2", byPath["geo/notify"])
	// Неоднозначный путь остаётся без id: ссылка вела бы неизвестно куда.
	assert.Empty(t, byPath["twin"])
	// §94.8: метка-заглушка узлом не является — про неё базу даже не спрашивают,
	// иначе экран предлагал бы открыть «узел <unresolved>».
	assert.Empty(t, byPath[metrics.NodeUnresolved])
	assert.NotContains(t, nodes.asked, metrics.NodeUnresolved)

	// Один путь спрашивается один раз, даже когда он есть в ОБОИХ блоках
	// (geo/notify и twin с ошибками попадают и в producers, и в failures).
	require.NotEmpty(t, r.TopFailures)
	assert.ElementsMatch(t, []string{"billing", "geo/notify", "twin"}, nodes.asked)
	for _, f := range r.TopFailures {
		if f.NodePath == "geo/notify" {
			assert.Equal(t, "n-2", f.NodeID)
		}
	}
}

// Резолвер — украшение: без него и при его ошибке экран обязан работать.
func TestKafkaByNode_ResolverDegrades(t *testing.T) {
	t.Parallel()
	prom := &fakeProm{throughput: map[string]port.NodeThroughput{"billing": {Out: 10}}}
	since, until := kafkaWindow()

	t.Run("резолвера нет вовсе", func(t *testing.T) {
		t.Parallel()
		uc := NewKafkaMonitorUsecase(prom, nil, nil, nil, defaultTh(), logging.NewNoop())
		r := uc.ByNode(context.Background(), since, until)
		require.Len(t, r.TopProducers, 1)
		assert.Empty(t, r.TopProducers[0].NodeID)
		assert.Equal(t, "billing", r.TopProducers[0].NodePath)
	})

	t.Run("резолвер упал", func(t *testing.T) {
		t.Parallel()
		nodes := &fakeNodePaths{err: errors.New("pg down")}
		uc := NewKafkaMonitorUsecase(prom, nil, nil, nodes, defaultTh(), logging.NewNoop())
		r := uc.ByNode(context.Background(), since, until)
		require.Len(t, r.TopProducers, 1)
		assert.Empty(t, r.TopProducers[0].NodeID)
		assert.True(t, r.PrometheusAvailable)
	})
}
