package kafka

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
)

// TestInFlightGauge проверяет, что in-flight gauge растёт/падает на inc/dec и
// что nil-метрики (без WithMetrics) безопасны.
func TestInFlightGauge(t *testing.T) {
	t.Parallel()

	t.Run("inc/dec moves the gauge", func(t *testing.T) {
		t.Parallel()
		m := metrics.New("sender")
		g := NewConsumerGroup(&config.Config{}, "nexus.async", nil, logging.NewNoop(), WithMetrics(m))

		g.incInFlight()
		g.incInFlight()
		assert.Equal(t, 2.0, testutil.ToFloat64(m.KafkaInFlight.WithLabelValues("sender")))
		g.decInFlight()
		assert.Equal(t, 1.0, testutil.ToFloat64(m.KafkaInFlight.WithLabelValues("sender")))
	})

	t.Run("nil metrics is safe", func(t *testing.T) {
		t.Parallel()
		g := NewConsumerGroup(&config.Config{}, "nexus.async", nil, logging.NewNoop())
		assert.NotPanics(t, func() {
			g.incInFlight()
			g.decInFlight()
		})
	})
}
