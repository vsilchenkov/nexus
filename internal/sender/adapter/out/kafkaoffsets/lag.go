package kafkaoffsets

import (
	"context"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/platform/logging"
)

// LagTarget — consumer-группа и её топик, чей lag публикуется.
type LagTarget struct {
	Group, Topic string
}

// groupPositioner — позиции группы по партициям топика (реализует
// *kafkapf.Offsets).
type groupPositioner interface {
	GroupPositions(ctx context.Context, group, topic string) ([]kafkapf.PartitionPosition, error)
}

// LagReporter публикует nexus_kafka_lag{topic,partition,group} из брокера
// (§113.4): ListOffsets − OffsetFetch по реальным номерам партиций.
//
// До §113 lag брался из kafka.Reader.Stats: HWM последнего полученного пакета
// минус позиция предвыборки, без номера партиции (partition="-1"). Он давал
// пилу при перезаписи топика и оставался нулём у зависшего reader'а, который
// ничего не выбирает. Брокер видит то же, что kafka-consumer-groups.sh, — и
// одинаково для всех реплик, поэтому Web и алерты берут max по репликам.
type LagReporter struct {
	offsets groupPositioner
	gauge   *prometheus.GaugeVec
	targets []LagTarget
	logger  logging.Logger
}

// NewLagReporter создаёт репортёр для targets; gauge — nexus_kafka_lag.
func NewLagReporter(offsets groupPositioner, gauge *prometheus.GaugeVec, targets []LagTarget, logger logging.Logger) *LagReporter {
	return &LagReporter{offsets: offsets, gauge: gauge, targets: targets, logger: logger}
}

// Report снимает lag всех групп и публикует его. Недоступный брокер или
// партиция — ряд не обновляется (последнее значение остаётся), причина в debug:
// падение Kafka видно по её собственным алертам, а Warn каждые 15 с был бы шумом.
func (r *LagReporter) Report(ctx context.Context) {
	for _, t := range r.targets {
		pos, err := r.offsets.GroupPositions(ctx, t.Group, t.Topic)
		if err != nil {
			r.logger.Debug("kafka lag: offsets unavailable, series not updated",
				r.logger.Str("group", t.Group), r.logger.Str("topic", t.Topic),
				r.logger.Int("partitions_read", len(pos)), r.logger.Err(err))
		}
		for _, p := range pos {
			r.gauge.WithLabelValues(t.Topic, strconv.Itoa(p.Partition), t.Group).Set(float64(p.Lag()))
		}
	}
}
