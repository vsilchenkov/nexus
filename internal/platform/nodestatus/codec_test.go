package nodestatus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// TestEncodeDecodeOutcome (§52): Redis-кодировка "0"=ok, "1"=down, "2"=degraded
// (намеренно НЕ совпадает с гауджем — legacy/rolling-совместимость, см. doc пакета).
func TestEncodeDecodeOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		outcome domain.NodeOutcome
		encoded string
	}{
		{domain.NodeOutcomeOK, "0"},
		{domain.NodeOutcomeDown, "1"},
		{domain.NodeOutcomeDegraded, "2"},
	}
	for _, tt := range tests {
		t.Run(string(tt.outcome), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.encoded, EncodeOutcome(tt.outcome))
			got, ok := DecodeOutcome(tt.encoded)
			require.True(t, ok)
			assert.Equal(t, tt.outcome, got, "round-trip")
		})
	}
}

// TestDecodeOutcome_Legacy: булево "1" старого Sender («любой не-2xx»)
// читается как down — worst case, самоисцелится следующим вызовом узла.
func TestDecodeOutcome_Legacy(t *testing.T) {
	t.Parallel()

	got, ok := DecodeOutcome("1")
	require.True(t, ok)
	assert.Equal(t, domain.NodeOutcomeDown, got)
}

// TestDecodeOutcome_Unknown: нераспознанное значение → ok=false, вызывающая
// сторона пропускает узел (fallback на Prometheus).
func TestDecodeOutcome_Unknown(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", "x", "3", "ok", "down"} {
		_, ok := DecodeOutcome(s)
		assert.False(t, ok, "value %q must not decode", s)
	}
}
