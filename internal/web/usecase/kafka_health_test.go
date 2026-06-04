package usecase

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// defaultTh — пороги из дефолтов config (§6 spec) для табличных тестов.
func defaultTh() KafkaThresholds {
	return KafkaThresholds{
		LagWarning:              100,
		LagCritical:             1000,
		LagGrowthCriticalPerSec: 50,
		ErrorRateWarning:        0.001,
		ErrorRateCritical:       0.01,
		ProduceP95WarningMs:     100,
		ProduceP95CriticalMs:    500,
	}
}

func TestEvaluateHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       healthInput
		severity string
		reason   string
	}{
		{
			name:     "healthy",
			in:       healthInput{currentLag: 10, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityOK, reason: ReasonHealthy,
		},
		{
			name:     "offline partitions always critical",
			in:       healthInput{offlinePartition: 1, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityErr, reason: ReasonOfflinePartitions,
		},
		{
			name:     "majority brokers down",
			in:       healthInput{brokersTotal: 3, brokersOnline: 1},
			severity: SeverityErr, reason: ReasonBrokersDown,
		},
		{
			name:     "error rate above critical",
			in:       healthInput{errorRate: 0.02, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityErr, reason: ReasonErrorRateHigh,
		},
		{
			name:     "lag above critical",
			in:       healthInput{currentLag: 1500, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityErr, reason: ReasonLagHigh,
		},
		{
			name:     "lag growing fast",
			in:       healthInput{currentLag: 200, lagGrowthPerSec: 80, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityErr, reason: ReasonLagGrowing,
		},
		{
			name:     "under-replicated is warn",
			in:       healthInput{underReplicated: 1, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityWarn, reason: ReasonUnderReplicated,
		},
		{
			name:     "single broker down is warn",
			in:       healthInput{brokersTotal: 3, brokersOnline: 2},
			severity: SeverityWarn, reason: ReasonBrokersDown,
		},
		{
			name:     "lag above warning",
			in:       healthInput{currentLag: 300, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityWarn, reason: ReasonLagHigh,
		},
		{
			name:     "error rate above warning",
			in:       healthInput{errorRate: 0.005, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityWarn, reason: ReasonErrorRateHigh,
		},
		{
			name:     "offline beats lag (most critical first)",
			in:       healthInput{offlinePartition: 1, currentLag: 5000, brokersTotal: 3, brokersOnline: 3},
			severity: SeverityErr, reason: ReasonOfflinePartitions,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := evaluateHealth(tt.in, defaultTh())
			assert.Equal(t, tt.severity, got.Severity)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

func TestAutoStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		window time.Duration
		want   time.Duration
	}{
		{time.Hour, 30 * time.Second},
		{3 * time.Hour, time.Minute},
		{24 * time.Hour, 5 * time.Minute},
		{7 * 24 * time.Hour, 30 * time.Minute},
		{30 * 24 * time.Hour, time.Hour},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, autoStep(tt.window))
	}
}

func TestPctDelta(t *testing.T) {
	t.Parallel()
	r, ok := pctDelta(108, 100)
	assert.True(t, ok)
	assert.InDelta(t, 0.08, r, 1e-9)

	_, ok = pctDelta(50, 0)
	assert.False(t, ok)
}
