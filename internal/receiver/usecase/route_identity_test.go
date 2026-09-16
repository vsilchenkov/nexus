package usecase

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// headerCapturingProducer запоминает ключ, заголовки и конверт последнего Produce.
type headerCapturingProducer struct {
	key     string
	headers map[string]string
	value   []byte
}

func (p *headerCapturingProducer) Produce(_ context.Context, _, key string, value []byte, headers map[string]string) error {
	p.key, p.value, p.headers = key, value, headers
	return nil
}

// TestRouteAsync_EnvelopeCarriesNodeIdentity (§101.4) — конверт и заголовки Kafka
// несут id узла и слог команды, где он найден. Путь уникален лишь внутри команды:
// без id Sender исполнял конверт узлом случайной из одноимённых команд.
func TestRouteAsync_EnvelopeCarriesNodeIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		teamSlug string
		wantTeam string
	}{
		{"слог из адреса", "webhook", "webhook"},
		{"адрес без слога — default-команда", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := methodTestNode()
			node.ID = "d3aae718-2b4c-4e6d-8f10-000000000002"
			prod := &headerCapturingProducer{}
			u := NewRouteAsyncUsecase(stubNodeReader{node: node}, prod, "nexus.async", 5, logging.NewNoop())

			_, err := u.RouteAsync(context.Background(), RouteInput{
				TeamSlug: tc.teamSlug, NodePath: "demo/path", Method: "POST",
			})
			require.NoError(t, err)

			assert.Equal(t, "demo/path", prod.key,
				"ключ партиции остаётся путём: смена переставила бы сообщения узла между партициями")
			assert.Equal(t, node.ID, prod.headers["node_id"])
			assert.Equal(t, tc.wantTeam, prod.headers["team"])

			var env Envelope
			require.NoError(t, json.Unmarshal(prod.value, &env))
			assert.Equal(t, node.ID, env.NodeID)
			assert.Equal(t, tc.wantTeam, env.Team)
		})
	}
}

// TestRoute_SendRequestCarriesTeamSlug (§101.4) — sync-путь узел в Sender не
// ищет, поэтому слог команды для метки team несёт сам gRPC-запрос.
func TestRoute_SendRequestCarriesTeamSlug(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: methodTestNode()}, sender, 5, logging.NewNoop())

	_, err := u.Route(context.Background(), RouteInput{TeamSlug: "webhook", NodePath: "demo/path", Method: "POST"})
	require.NoError(t, err)
	require.NotNil(t, sender.last)
	assert.Equal(t, "webhook", sender.last.GetTeamSlug())
}
