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
		got, remainder, err := resolveNode(ctx, reader, "webhook", "sendasynq")
		require.NoError(t, err)
		assert.Equal(t, legacy, got)
		assert.Empty(t, remainder)
	})

	t.Run("явный слог команды имеет приоритет", func(t *testing.T) {
		// URL /api/v1/request/partner/orders → split → (partner, orders).
		got, remainder, err := resolveNode(ctx, reader, "partner", "orders")
		require.NoError(t, err)
		assert.Equal(t, teamScoped, got)
		assert.Empty(t, remainder)
	})

	t.Run("single-segment default-team путь", func(t *testing.T) {
		reader2 := mapNodeReader{nodes: map[string]*domain.Node{
			domain.DefaultTeamSlug + "/ping": {Path: "ping"},
		}}
		got, remainder, err := resolveNode(ctx, reader2, "", "ping")
		require.NoError(t, err)
		assert.Equal(t, "ping", got.Path)
		assert.Empty(t, remainder)
	})

	t.Run("реально несуществующий узел → not found", func(t *testing.T) {
		_, _, err := resolveNode(ctx, reader, "nope", "missing")
		assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	})
}

// TestResolveNode_PathPassthrough — §39: префиксный матч с приклеиванием хвоста.
func TestResolveNode_PathPassthrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("passthrough on → найден узел-префикс + remainder (с явным слогом)", func(t *testing.T) {
		// URL /api/v1/request/vika/ozon/GetAuthToken → split → (vika, ozon/GetAuthToken).
		ozon := &domain.Node{Path: "ozon", PathPassthrough: true}
		reader := mapNodeReader{nodes: map[string]*domain.Node{"vika/ozon": ozon}}

		got, remainder, err := resolveNode(ctx, reader, "vika", "ozon/GetAuthToken")
		require.NoError(t, err)
		assert.Equal(t, ozon, got)
		assert.Equal(t, "GetAuthToken", remainder)
	})

	t.Run("passthrough on → multi-segment remainder", func(t *testing.T) {
		svc := &domain.Node{Path: "svc", PathPassthrough: true}
		reader := mapNodeReader{nodes: map[string]*domain.Node{"team/svc": svc}}

		got, remainder, err := resolveNode(ctx, reader, "team", "svc/user/123")
		require.NoError(t, err)
		assert.Equal(t, svc, got)
		assert.Equal(t, "user/123", remainder)
	})

	t.Run("passthrough on → legacy без слога (default-team)", func(t *testing.T) {
		// URL /api/v1/request/ozon/GetAuthToken → split → (ozon, GetAuthToken).
		ozon := &domain.Node{Path: "ozon", PathPassthrough: true}
		reader := mapNodeReader{nodes: map[string]*domain.Node{
			domain.DefaultTeamSlug + "/ozon": ozon,
		}}

		got, remainder, err := resolveNode(ctx, reader, "ozon", "GetAuthToken")
		require.NoError(t, err)
		assert.Equal(t, ozon, got)
		assert.Equal(t, "GetAuthToken", remainder)
	})

	t.Run("passthrough off → 404 (текущее поведение сохранено)", func(t *testing.T) {
		ozon := &domain.Node{Path: "ozon", PathPassthrough: false}
		reader := mapNodeReader{nodes: map[string]*domain.Node{"vika/ozon": ozon}}

		_, _, err := resolveNode(ctx, reader, "vika", "ozon/GetAuthToken")
		assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("longest-prefix wins: более длинный узел без passthrough → 404, не уезжает на короткий", func(t *testing.T) {
		// Узел a (passthrough) и a/b (без). Запрос a/b/c должен матчить a/b
		// (точнее всех), а раз у него passthrough off → 404, а не уехать на a.
		aNode := &domain.Node{Path: "a", PathPassthrough: true}
		abNode := &domain.Node{Path: "a/b", PathPassthrough: false}
		reader := mapNodeReader{nodes: map[string]*domain.Node{
			"team/a":   aNode,
			"team/a/b": abNode,
		}}

		_, _, err := resolveNode(ctx, reader, "team", "a/b/c")
		assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("точное совпадение приоритетнее префикса", func(t *testing.T) {
		// Узлы svc (passthrough) и svc/user (точный). Запрос svc/user → точный.
		svc := &domain.Node{Path: "svc", PathPassthrough: true}
		svcUser := &domain.Node{Path: "svc/user", PathPassthrough: false}
		reader := mapNodeReader{nodes: map[string]*domain.Node{
			"team/svc":      svc,
			"team/svc/user": svcUser,
		}}

		got, remainder, err := resolveNode(ctx, reader, "team", "svc/user")
		require.NoError(t, err)
		assert.Equal(t, svcUser, got)
		assert.Empty(t, remainder)
	})
}

// TestResolveNode_OverlappingNodes (§39, вопрос пользователя): узел-passthrough
// `ozon` и точный узел `ozon/GetAuthToken` в одной команде. Точный матч всегда
// приоритетнее префикса; «затеняется» ровно подпуть точного узла. Если точный
// узел без passthrough, путь ГЛУБЖЕ него даёт 404 (longest-existing-prefix wins,
// без провала на короткий passthrough-узел).
func TestResolveNode_OverlappingNodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ozon := &domain.Node{Path: "ozon", PathPassthrough: true}                   // Нода 1: passthrough
	ozonAuth := &domain.Node{Path: "ozon/GetAuthToken", PathPassthrough: false} // Нода 2: прямой
	reader := mapNodeReader{nodes: map[string]*domain.Node{
		"vika/ozon":              ozon,
		"vika/ozon/GetAuthToken": ozonAuth,
	}}

	cases := []struct {
		name          string
		path          string // nodePath после splitTeamSlugAndPath (team=vika)
		wantNode      *domain.Node
		wantRemainder string
		wantNotFound  bool
	}{
		{"точный подпуть → прямой узел (Нода 2), без приклеивания", "ozon/GetAuthToken", ozonAuth, "", false},
		{"другой подпуть → passthrough (Нода 1) + хвост", "ozon/Orders", ozon, "Orders", false},
		{"базовый путь узла → Нода 1 без хвоста", "ozon", ozon, "", false},
		{"глубже точного узла (passthrough off) → 404, НЕ уезжает на Ноду 1", "ozon/GetAuthToken/v2", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node, remainder, err := resolveNode(ctx, reader, "vika", c.path)
			if c.wantNotFound {
				assert.ErrorIs(t, err, domain.ErrNodeNotFound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.wantNode, node)
			assert.Equal(t, c.wantRemainder, remainder)
		})
	}
}

// TestAppendPathSuffix — §39: корректное приклеивание хвоста к целевому URL.
func TestAppendPathSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		target    string
		remainder string
		want      string
	}{
		{"пустой remainder → URL без изменений", "https://api.ozon.ru:443", "", "https://api.ozon.ru:443"},
		{"корневой target + один сегмент", "https://api.ozon.ru:443", "GetAuthToken", "https://api.ozon.ru:443/GetAuthToken"},
		{"target с путём + хвост", "https://api.partner.com/hook", "user/123", "https://api.partner.com/hook/user/123"},
		{"target с trailing slash без дублей", "https://api.partner.com/hook/", "user/123", "https://api.partner.com/hook/user/123"},
		{"сохранение query целевого URL", "https://api.partner.com/hook?k=v", "tail", "https://api.partner.com/hook/tail?k=v"},
		{"traversal «..» обезврежен (не выходит за базу)", "https://api.partner.com/base", "../evil", "https://api.partner.com/evil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, AppendPathSuffix(tt.target, tt.remainder))
		})
	}
}
