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
			n := &Node{RootMethod: tt.root, IncomingMethod: HTTPMethodAny, SSEDecline: true, BreakerIgnoreTimeouts: true}

			cleared := n.NormalizeForRootMethod()

			assert.Equal(t, tt.wantSSE, n.SSEDecline)
			assert.Equal(t, tt.wantCleared, slices.Contains(cleared, "sse_decline"),
				"сброс обязан попасть в cleared_incompatible_fields аудита")
			assert.True(t, n.BreakerIgnoreTimeouts, "флаг таймаутов нормализация не трогает")
		})
	}
}

// TestNode_NormalizeForRootMethod_SSEDeclineNeedsGET — флаг осмыслен, только
// если sync-узел принимает GET: входящий ANY или GET. Узел с другим методом
// отвечает на GET отказом §40, и скрытый в форме флаг не должен молча
// подменять этот отказ своим 405 (замечание со стенда §107).
func TestNode_NormalizeForRootMethod_SSEDeclineNeedsGET(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		incoming HTTPMethod
		wantSSE  bool
	}{
		{"ANY keeps flag", HTTPMethodAny, true},
		{"GET keeps flag", HTTPMethodGET, true},
		{"POST clears flag", HTTPMethodPOST, false},
		{"PUT clears flag", HTTPMethodPUT, false},
		{"DELETE clears flag", HTTPMethodDELETE, false},
		{"empty means POST, clears flag", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := &Node{RootMethod: RootMethodRequest, IncomingMethod: tt.incoming, SSEDecline: true}

			cleared := n.NormalizeForRootMethod()

			assert.Equal(t, tt.wantSSE, n.SSEDecline)
			assert.Equal(t, !tt.wantSSE, slices.Contains(cleared, "sse_decline"))
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
