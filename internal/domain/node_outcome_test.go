package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOutcomeFromStatusCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code int32
		want NodeOutcome
	}{
		{name: "transport error status 0", code: 0, want: NodeOutcomeDown},
		{name: "negative status", code: -1, want: NodeOutcomeDown},
		{name: "1xx informational", code: 101, want: NodeOutcomeDegraded},
		{name: "200 ok", code: 200, want: NodeOutcomeOK},
		{name: "204 no content", code: 204, want: NodeOutcomeOK},
		{name: "299 upper bound of 2xx", code: 299, want: NodeOutcomeOK},
		{name: "301 redirect", code: 301, want: NodeOutcomeDegraded},
		{name: "404 not found", code: 404, want: NodeOutcomeDegraded},
		{name: "422 stale FCM token (§50.4)", code: 422, want: NodeOutcomeDegraded},
		{name: "499 upper bound of degraded", code: 499, want: NodeOutcomeDegraded},
		{name: "500 server error", code: 500, want: NodeOutcomeDown},
		{name: "502 oversize (§43-rev)", code: 502, want: NodeOutcomeDown},
		{name: "503 breaker open", code: 503, want: NodeOutcomeDown},
		{name: "599 upper 5xx", code: 599, want: NodeOutcomeDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, OutcomeFromStatusCode(tt.code))
		})
	}
}

func TestNodeOutcome_GaugeValue_RoundTrip(t *testing.T) {
	t.Parallel()

	for _, o := range []NodeOutcome{NodeOutcomeOK, NodeOutcomeDegraded, NodeOutcomeDown} {
		assert.Equal(t, o, OutcomeFromGaugeValue(o.GaugeValue()), "round-trip for %s", o)
	}
}

func TestOutcomeFromGaugeValue_Bounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    float64
		want NodeOutcome
	}{
		{name: "zero is ok", v: 0, want: NodeOutcomeOK},
		{name: "below degraded threshold", v: 0.5, want: NodeOutcomeOK},
		{name: "exactly degraded", v: 1, want: NodeOutcomeDegraded},
		{name: "between degraded and down", v: 1.5, want: NodeOutcomeDegraded},
		{name: "exactly down", v: 2, want: NodeOutcomeDown},
		{name: "above down", v: 3, want: NodeOutcomeDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, OutcomeFromGaugeValue(tt.v))
		})
	}
}

func TestNodeOutcome_Valid(t *testing.T) {
	t.Parallel()

	assert.True(t, NodeOutcomeOK.Valid())
	assert.True(t, NodeOutcomeDegraded.Valid())
	assert.True(t, NodeOutcomeDown.Valid())
	assert.False(t, NodeOutcome("").Valid())
	assert.False(t, NodeOutcome("unknown").Valid())
}

func TestNodeOutcome_IsError(t *testing.T) {
	t.Parallel()

	assert.False(t, NodeOutcomeOK.IsError())
	assert.True(t, NodeOutcomeDegraded.IsError())
	assert.True(t, NodeOutcomeDown.IsError())
}
