package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestStallAfter (§113.6): ключа нет → 120 с; явный 0 — сторож выключен и
// дефолтом не перетирается; отрицательное значение отвергается.
func TestStallAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		want    time.Duration
		wantErr bool
	}{
		{name: "ключа нет", yaml: "kafka:\n  consumer:\n    instances: 2\n", want: 120 * time.Second},
		{name: "выключен", yaml: "kafka:\n  consumer:\n    stall_after_sec: 0\n", want: 0},
		{name: "свой порог", yaml: "kafka:\n  consumer:\n    stall_after_sec: 300\n", want: 300 * time.Second},
		{name: "отрицательный", yaml: "kafka:\n  consumer:\n    stall_after_sec: -1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg Config
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &cfg))
			applyDefaults(&cfg)
			err := validateStallAfter(cfg.Kafka.Consumer)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Kafka.Consumer.StallAfter())
		})
	}
}
