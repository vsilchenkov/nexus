package domain

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSSESubscribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		accept []string
		want   bool
	}{
		{"mcp client subscription", "GET", []string{"text/event-stream"}, true},
		{"media type is case-insensitive", "GET", []string{"Text/Event-Stream"}, true},
		{"method is case-insensitive", "get", []string{"text/event-stream"}, true},
		{"parameters allowed", "GET", []string{"text/event-stream; charset=utf-8"}, true},
		{"one of a list", "GET", []string{"application/json, text/event-stream;q=0.9"}, true},
		{"second Accept header line", "GET", []string{"application/json", "text/event-stream"}, true},
		{"spaces around item", "GET", []string{"application/json ,  text/event-stream  "}, true},
		{"unreadable q counts as weight", "GET", []string{"text/event-stream;q=abc"}, true},
		{"q=0 refuses the type", "GET", []string{"text/event-stream;q=0"}, false},
		{"q=0.000 refuses the type", "GET", []string{"text/event-stream; q=0.000"}, false},
		{"POST tool call with SSE accept", "POST", []string{"application/json, text/event-stream"}, false},
		{"DELETE session close", "DELETE", []string{"text/event-stream"}, false},
		{"plain GET", "GET", []string{"application/json"}, false},
		{"wildcard is not a subscription", "GET", []string{"*/*"}, false},
		{"text wildcard is not a subscription", "GET", []string{"text/*"}, false},
		{"no Accept", "GET", nil, false},
		{"broken item skipped, next one counts", "GET", []string{"text/;;, text/event-stream"}, true},
		{"only broken items", "GET", []string{"/, ;;;"}, false},
		{"similar type", "GET", []string{"text/event-streams"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for _, v := range tt.accept {
				h.Add("Accept", v)
			}
			assert.Equal(t, tt.want, IsSSESubscribe(tt.method, h))
		})
	}
}

func FuzzIsSSESubscribe(f *testing.F) {
	for _, seed := range []string{
		"text/event-stream", "text/event-stream;q=0", "application/json, text/event-stream",
		"", ",,,", "text/event-stream; q=\"0\"", "text/event-stream;charset=\"utf-8",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, accept string) {
		h := http.Header{"Accept": []string{accept}}
		// Инвариант: не-GET подпиской не бывает никогда, а разбор не паникует
		// на любом входе.
		IsSSESubscribe(http.MethodGet, h)
		if IsSSESubscribe(http.MethodPost, h) {
			t.Fatalf("POST must never be a subscription, accept=%q", accept)
		}
	})
}

func TestNode_DeclineStream(t *testing.T) {
	t.Parallel()

	sse := http.Header{"Accept": []string{"text/event-stream"}}
	tests := []struct {
		name      string
		node      Node
		method    string
		wantAllow string
		declined  bool
	}{
		{"flag off passes subscription", Node{RootMethod: RootMethodRequest, IncomingMethod: HTTPMethodAny}, "GET", "", false},
		{"ANY node", Node{RootMethod: RootMethodRequest, SSEDecline: true, IncomingMethod: HTTPMethodAny}, "GET", allowForAnyMethod, true},
		// Узел без GET отвечает на подписку отказом метода §40 и без флага:
		// устаревший флаг (ручная правка БД) не подменяет этот отказ своим 405.
		{"POST node ignores stale flag", Node{RootMethod: RootMethodRequest, SSEDecline: true, IncomingMethod: HTTPMethodPOST}, "GET", "", false},
		{"empty method means POST, flag ignored", Node{RootMethod: RootMethodRequest, SSEDecline: true}, "GET", "", false},
		{"GET-only node", Node{RootMethod: RootMethodRequest, SSEDecline: true, IncomingMethod: HTTPMethodGET}, "GET", "", true},
		{"tool call passes", Node{RootMethod: RootMethodRequest, SSEDecline: true, IncomingMethod: HTTPMethodAny}, "POST", "", false},
		{"async node ignores stale flag", Node{RootMethod: RootMethodRequestAsync, SSEDecline: true}, "GET", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.node.DeclineStream(tt.method, sse)
			if !tt.declined {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrStreamDeclined)
			var declined *StreamDeclinedError
			require.True(t, errors.As(err, &declined))
			assert.Equal(t, tt.wantAllow, declined.Allow)
		})
	}
}
