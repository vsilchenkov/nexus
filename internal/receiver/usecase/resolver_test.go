package usecase

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"

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
		got, remainder, team, err := resolveNode(ctx, reader, "webhook", "sendasynq")
		require.NoError(t, err)
		assert.Equal(t, legacy, got)
		assert.Empty(t, remainder)
		// §101.3: «webhook» оказался частью пути — узел лежит в default-команде.
		assert.Equal(t, domain.DefaultTeamSlug, team)
	})

	t.Run("явный слог команды имеет приоритет", func(t *testing.T) {
		// URL /api/v1/request/partner/orders → split → (partner, orders).
		got, remainder, team, err := resolveNode(ctx, reader, "partner", "orders")
		require.NoError(t, err)
		assert.Equal(t, teamScoped, got)
		assert.Empty(t, remainder)
		assert.Equal(t, "partner", team)
	})

	t.Run("single-segment default-team путь", func(t *testing.T) {
		reader2 := mapNodeReader{nodes: map[string]*domain.Node{
			domain.DefaultTeamSlug + "/ping": {Path: "ping"},
		}}
		got, remainder, team, err := resolveNode(ctx, reader2, "", "ping")
		require.NoError(t, err)
		assert.Equal(t, "ping", got.Path)
		assert.Empty(t, remainder)
		// Пустой слог адреса — это default-команда, а не «без команды».
		assert.Equal(t, domain.DefaultTeamSlug, team)
	})

	t.Run("реально несуществующий узел → not found", func(t *testing.T) {
		_, _, _, err := resolveNode(ctx, reader, "nope", "missing")
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

		got, remainder, team, err := resolveNode(ctx, reader, "vika", "ozon/GetAuthToken")
		require.NoError(t, err)
		assert.Equal(t, ozon, got)
		assert.Equal(t, "GetAuthToken", remainder)
		assert.Equal(t, "vika", team)
	})

	t.Run("passthrough on → multi-segment remainder", func(t *testing.T) {
		svc := &domain.Node{Path: "svc", PathPassthrough: true}
		reader := mapNodeReader{nodes: map[string]*domain.Node{"team/svc": svc}}

		got, remainder, _, err := resolveNode(ctx, reader, "team", "svc/user/123")
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

		got, remainder, team, err := resolveNode(ctx, reader, "ozon", "GetAuthToken")
		require.NoError(t, err)
		assert.Equal(t, ozon, got)
		assert.Equal(t, "GetAuthToken", remainder)
		// Префиксный матч по интерпретации B: узел из default-команды.
		assert.Equal(t, domain.DefaultTeamSlug, team)
	})

	t.Run("passthrough off → 404 (текущее поведение сохранено)", func(t *testing.T) {
		ozon := &domain.Node{Path: "ozon", PathPassthrough: false}
		reader := mapNodeReader{nodes: map[string]*domain.Node{"vika/ozon": ozon}}

		_, _, _, err := resolveNode(ctx, reader, "vika", "ozon/GetAuthToken")
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

		_, _, _, err := resolveNode(ctx, reader, "team", "a/b/c")
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

		got, remainder, _, err := resolveNode(ctx, reader, "team", "svc/user")
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
			node, remainder, _, err := resolveNode(ctx, reader, "vika", c.path)
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

// pgLikeNodeReader — mapNodeReader, который, как PostgreSQL с базой в UTF8,
// отвечает на невалидный UTF-8 в параметре инфра-ошибкой (SQLSTATE 22021), а
// не «узла нет». Считает такие обращения.
type pgLikeNodeReader struct {
	mapNodeReader
	invalid *int
}

var errPGInvalidByteSequence = errors.New(`invalid byte sequence for encoding "UTF8": 0xce 0xf0`)

func (r pgLikeNodeReader) Get(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	if !utf8.ValidString(teamSlug) || !utf8.ValidString(path) {
		*r.invalid++
		return nil, errPGInvalidByteSequence
	}
	return r.mapNodeReader.Get(ctx, teamSlug, path)
}

// TestResolveNode_InvalidUTF8 — §109.5 (NEXUS-12): адрес в cp1251 даёт 404, а
// не инфра-ошибку (502 + Sentry), и не доходит до хранилища; passthrough-узел
// с хвостом в чужой кодировке находится как прежде.
func TestResolveNode_InvalidUTF8(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	passthrough := &domain.Node{Path: "files", PathPassthrough: true}
	exact := &domain.Node{Path: "orders"}
	nodes := map[string]*domain.Node{
		"vika_dev5/files":  passthrough,
		"vika_dev5/orders": exact,
	}

	cases := []struct {
		name          string
		team, path    string
		wantNode      *domain.Node
		wantRemainder string
	}{
		{name: "путь в cp1251 → 404", team: "vika_dev5", path: "\xce\xf0"},
		{name: "слог в cp1251 → 404", team: "\xce\xf0", path: "orders"},
		{name: "хвост в cp1251 за точным узлом без passthrough → 404", team: "vika_dev5", path: "orders/\xce\xf0"},
		{
			name: "passthrough: хвост в cp1251 уходит получателю как есть",
			team: "vika_dev5", path: "files/\xce\xf0/1",
			wantNode: passthrough, wantRemainder: "\xce\xf0/1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			invalid := 0
			reader := pgLikeNodeReader{nodes: nodes, invalid: &invalid}

			node, remainder, _, err := resolveNode(ctx, reader, tc.team, tc.path)

			assert.Zero(t, invalid, "невалидный UTF-8 не должен доходить до хранилища")
			if tc.wantNode == nil {
				require.ErrorIs(t, err, domain.ErrNodeNotFound)
				assert.ErrorIs(t, err, errInvalidUTF8Path, "debug-строка отказа должна назвать причину")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantNode, node)
			assert.Equal(t, tc.wantRemainder, remainder)
		})
	}
}
