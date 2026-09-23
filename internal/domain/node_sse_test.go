package domain

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNode_NormalizeForRootMethod_SSEDecline — §107.2: отказ от SSE-подписки
// осмыслен только у sync-узла. У async-узла клиент получает ответ приёма, не
// дожидаясь получателя; у pull-узла входа нет вовсе. Флаг таймаутов breaker'а
// (§107.3) применим ко всем типам и нормализацией не трогается.
func TestNode_NormalizeForRootMethod_SSEDecline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		root        RootMethod
		wantSSE     bool
		wantCleared bool
	}{
		{"request keeps flag", RootMethodRequest, true, false},
		{"requestAsync clears flag", RootMethodRequestAsync, false, true},
		{"RabbitMQAsync clears flag", RootMethodRabbitMQAsync, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := &Node{RootMethod: tt.root, SSEDecline: true, BreakerIgnoreTimeouts: true}

			cleared := n.NormalizeForRootMethod()

			assert.Equal(t, tt.wantSSE, n.SSEDecline)
			assert.Equal(t, tt.wantCleared, slices.Contains(cleared, "sse_decline"),
				"сброс обязан попасть в cleared_incompatible_fields аудита")
			assert.True(t, n.BreakerIgnoreTimeouts, "флаг таймаутов нормализация не трогает")
		})
	}
}

// TestNode_NormalizeForRootMethod_SSEDeclineOffIsSilent — выключенный флаг у
// async-узла не даёт записи в аудит: сбрасывать нечего.
func TestNode_NormalizeForRootMethod_SSEDeclineOffIsSilent(t *testing.T) {
	t.Parallel()
	n := &Node{RootMethod: RootMethodRequestAsync}
	assert.Nil(t, n.NormalizeForRootMethod())
}
