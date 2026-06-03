package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// mapNodeReader — port.NodeReader, отдающий узлы по ключу "<team>/<path>".
// Пустой teamSlug нормализуется в DefaultTeamSlug (как реальный Reader).
type mapNodeReader struct {
	nodes map[string]*domain.Node
}

func (r mapNodeReader) Get(_ context.Context, teamSlug, path string) (*domain.Node, error) {
	if teamSlug == "" {
		teamSlug = domain.DefaultTeamSlug
	}
	if n, ok := r.nodes[teamSlug+"/"+path]; ok {
		return n, nil
	}
	return nil, domain.ErrNodeNotFound
}

func TestResolveNode_Fallback(t *testing.T) {
	t.Parallel()

	legacy := &domain.Node{Path: "webhook/sendasynq"} // default-team, путь со слешем
	teamScoped := &domain.Node{Path: "orders"}        // команда partner, узел orders
	reader := mapNodeReader{nodes: map[string]*domain.Node{
		domain.DefaultTeamSlug + "/webhook/sendasynq": legacy,
		"partner/orders": teamScoped,
	}}
	ctx := context.Background()

	t.Run("legacy slash-path найден по адресу без слога (фикс #8)", func(t *testing.T) {
		// URL /api/v1/requestAsync/webhook/sendasynq → split → (webhook, sendasynq).
		got, err := resolveNode(ctx, reader, "webhook", "sendasynq")
		require.NoError(t, err)
		assert.Equal(t, legacy, got)
	})

	t.Run("явный слог команды имеет приоритет", func(t *testing.T) {
		// URL /api/v1/request/partner/orders → split → (partner, orders).
		got, err := resolveNode(ctx, reader, "partner", "orders")
		require.NoError(t, err)
		assert.Equal(t, teamScoped, got)
	})

	t.Run("single-segment default-team путь", func(t *testing.T) {
		reader2 := mapNodeReader{nodes: map[string]*domain.Node{
			domain.DefaultTeamSlug + "/ping": {Path: "ping"},
		}}
		got, err := resolveNode(ctx, reader2, "", "ping")
		require.NoError(t, err)
		assert.Equal(t, "ping", got.Path)
	})

	t.Run("реально несуществующий узел → not found", func(t *testing.T) {
		_, err := resolveNode(ctx, reader, "nope", "missing")
		assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	})
}
