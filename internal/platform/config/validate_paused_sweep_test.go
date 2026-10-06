package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidatePausedSweep (§113.3): interval ≤ backstop ≤ retention/4.
func TestValidatePausedSweep(t *testing.T) {
	t.Parallel()

	const week = int64(7 * 24 * 3600 * 1000)
	tests := []struct {
		name        string
		cfg         SenderPausedSweepConfig
		retentionMs int64
		wantErr     bool
	}{
		{"defaults on week retention", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: 21600}, week, false},
		{"backstop equals retention quarter", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: int(week / 4 / 1000)}, week, false},
		{"backstop above retention quarter", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: int(week/4/1000) + 1}, week, true},
		{"backstop below interval", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: 10}, week, true},
		{"zero interval", SenderPausedSweepConfig{IntervalSec: 0, BackstopSec: 21600}, week, true},
		{"unlimited retention", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: 10 * 21600}, -1, false},
		{"retention not set", SenderPausedSweepConfig{IntervalSec: 30, BackstopSec: 10 * 21600}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePausedSweep(&tt.cfg, tt.retentionMs)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
