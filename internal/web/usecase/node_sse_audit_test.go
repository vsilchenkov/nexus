package usecase

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// TestDiffNodes_StreamAndTimeoutFlags — §107: включение и выключение обоих
// флагов попадает в аудит со значениями «до/после». Флаги меняют, что видит
// клиент узла (405 вместо подписки) и как ведёт себя защита, — без записи в
// журнале «почему breaker перестал открываться» не восстановить.
func TestDiffNodes_StreamAndTimeoutFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      string
		old, new domain.Node
	}{
		{"sse_decline on", "sse_decline", domain.Node{}, domain.Node{SSEDecline: true}},
		{"sse_decline off", "sse_decline", domain.Node{SSEDecline: true}, domain.Node{}},
		{"breaker_ignore_timeouts on", "breaker_ignore_timeouts", domain.Node{}, domain.Node{BreakerIgnoreTimeouts: true}},
		{"breaker_ignore_timeouts off", "breaker_ignore_timeouts", domain.Node{BreakerIgnoreTimeouts: true}, domain.Node{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := diffNodes(&tt.old, &tt.new)
			assert.Equal(t, map[string]any{
				"before": !tt.new.SSEDecline && !tt.new.BreakerIgnoreTimeouts,
				"after":  tt.new.SSEDecline || tt.new.BreakerIgnoreTimeouts,
			}, d[tt.key])
			assert.Len(t, d, 1, "меняется ровно одно поле")
		})
	}

	t.Run("unchanged flags stay out of diff", func(t *testing.T) {
		t.Parallel()
		n := domain.Node{SSEDecline: true, BreakerIgnoreTimeouts: true}
		assert.Empty(t, diffNodes(&n, &n))
	})
}
