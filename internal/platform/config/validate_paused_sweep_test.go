package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidatePausedSweep (§113.3): interval ≤ backstop ≤ retention/4.
// TestDefaultPausedBackstop (ревизия §113): ключа backstop_sec нет, а retention
// короче суток — по умолчанию берётся четверть retention, и конфиг проходит
// Validate. До правки дефолт был 6 ч всегда, и такая инсталляция после
// обновления не поднимала ни один из трёх сервисов.
func TestDefaultPausedBackstop(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		retentionMs int64
		interval    int
		want        int
	}{
		{"week retention keeps 6h", 7 * 24 * 3600 * 1000, 30, 21600},
		{"12h retention takes a quarter", 12 * 3600 * 1000, 30, 10800},
		{"retention not set", 0, 30, 21600},
		{"tiny retention not below interval", 60 * 1000, 30, 30},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := defaultPausedBackstopSec(tt.retentionMs, tt.interval)
			assert.Equal(t, tt.want, got)
			if tt.retentionMs >= 4*int64(tt.interval)*1000 || tt.retentionMs <= 0 {
				assert.NoError(t, validatePausedSweep(&SenderPausedSweepConfig{IntervalSec: tt.interval, BackstopSec: got}, tt.retentionMs))
			}
		})
	}
}

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
		// Ревизия §113: выключенный sweeper не мешает стартовать (Validate общий).
		{"disabled is not validated", SenderPausedSweepConfig{Disabled: true, IntervalSec: 30, BackstopSec: 10}, week, false},
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
