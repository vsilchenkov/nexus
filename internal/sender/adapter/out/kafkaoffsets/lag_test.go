package kafkaoffsets

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

type stubPositioner map[string]struct {
	pos []kafkapf.PartitionPosition
	err error
}

func (s stubPositioner) GroupPositions(_ context.Context, group, _ string) ([]kafkapf.PartitionPosition, error) {
	r := s[group]
	return r.pos, r.err
}

func newLagGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexus_kafka_lag"}, []string{"topic", "partition", "group"})
}

// TestLagReporter_PublishesPerPartition (§113.4): lag по реальным номерам
// партиций, а не один ряд partition="-1"; некоммиченная партиция считается с
// начала, частичный ответ брокера публикуется, остальное не трогается.
func TestLagReporter_PublishesPerPartition(t *testing.T) {
	t.Parallel()

	gauge := newLagGauge()
	gauge.WithLabelValues("nexus.async.dlq", "0", "g-dlq-reprocess").Set(42) // прошлое значение
	offsets := stubPositioner{
		"g": {pos: []kafkapf.PartitionPosition{
			{Partition: 0, First: 0, Last: 100, Committed: 90},
			{Partition: 3, First: 0, Last: 960, Committed: -1},
		}},
		"g-paused": {
			pos: []kafkapf.PartitionPosition{{Partition: 1, First: 5, Last: 7, Committed: 7}},
			err: errors.New("partition 2: not leader"),
		},
		"g-dlq-reprocess": {err: errors.New("dial")},
	}
	r := NewLagReporter(offsets, gauge, []LagTarget{
		{Group: "g", Topic: "nexus.async"},
		{Group: "g-paused", Topic: "nexus.async.paused"},
		{Group: "g-dlq-reprocess", Topic: "nexus.async.dlq"},
	}, logging.NewNoop())

	r.Report(context.Background())

	assert.InDelta(t, 10, testutil.ToFloat64(gauge.WithLabelValues("nexus.async", "0", "g")), 0)
	assert.InDelta(t, 960, testutil.ToFloat64(gauge.WithLabelValues("nexus.async", "3", "g")), 0)
	assert.InDelta(t, 0, testutil.ToFloat64(gauge.WithLabelValues("nexus.async.paused", "1", "g-paused")), 0)
	assert.InDelta(t, 42, testutil.ToFloat64(gauge.WithLabelValues("nexus.async.dlq", "0", "g-dlq-reprocess")), 0,
		"брокер недоступен — прежнее значение не затирается нулём")
	assert.Equal(t, 4, testutil.CollectAndCount(gauge))
}
