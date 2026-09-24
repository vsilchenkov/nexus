package usecase

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// TestDryRun_StreamDeclined — §55.9 требует повторять шаги боевого Receiver'а:
// подписка на поток событий у узла с отказом от SSE получает проваленный шаг
// stream.declined первым и дальше не идёт (на бою — 405 без вызова
// получателя). Вызов инструмента (POST) и выключенный флаг отчёт не меняют.
func TestDryRun_StreamDeclined(t *testing.T) {
	t.Parallel()

	sse := http.Header{"Accept": []string{"text/event-stream"}}
	tests := []struct {
		name         string
		flag         bool
		method       string
		wantDeclined bool
	}{
		{"subscription declined", true, http.MethodGet, true},
		{"flag off", false, http.MethodGet, false},
		{"tool call", true, http.MethodPost, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			uc, audit := newDryRunUC()
			n := &domain.Node{
				Path:             "mcp-1c",
				RootMethod:       domain.RootMethodRequest,
				IncomingMethod:   domain.HTTPMethodAny,
				URLMode:          domain.URLModeStatic,
				TargetURL:        "http://mcp.example:8172/mcp",
				AuthType:         domain.AuthTypeNone,
				IncomingAuthType: domain.IncomingAuthTypeNone,
				Status:           domain.NodeStatusEnabled,
				SSEDecline:       tt.flag,
			}

			rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
				Node: n, Method: tt.method, Query: url.Values{}, Headers: sse.Clone(), UseMock: true,
			})

			require.NoError(t, err)
			require.NotEmpty(t, rep.Steps)
			first := rep.Steps[0]
			if !tt.wantDeclined {
				assert.NotEqual(t, "stream.declined", first.Name)
				assert.True(t, rep.OK, "steps=%+v", rep.Steps)
				return
			}
			assert.False(t, rep.OK)
			require.Len(t, rep.Steps, 1, "после отказа шаги не выполняются — как на бою")
			assert.Equal(t, "stream.declined", first.Name)
			assert.Equal(t, "failed", first.Status)
			assert.Equal(t, map[string]any{
				"status": http.StatusMethodNotAllowed,
				"allow":  "POST, PUT, PATCH, DELETE, HEAD, OPTIONS",
			}, first.Detail)
			require.Len(t, audit.entries, 1, "dry-run пишет аудит и при отказе")
		})
	}
}
