package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSplitTeamSlugAndPath фиксирует контракт парсинга URL Receiver'а
// (Phase 10.E.1): /api/v1/request/<team_slug>/<node_path> для multi-tenancy
// и /api/v1/request/<node_path> для legacy default-team.
//
// Cross-team изоляция в Receiver работает на уровне БД: NodeReader.Get
// делает SELECT через JOIN с teams (WHERE slug=$1 AND path=$2). Если
// клиент указал чужой team_slug, узел не найдётся — вернётся
// ErrNodeNotFound (404), а не утечка существования узла другой команды.
func TestSplitTeamSlugAndPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		wantTeamSlug string
		wantNodePath string
	}{
		// Catch-all gin всегда передаёт строку с ведущим '/'.
		{"multi-tenant simple", "/acme/order", "acme", "order"},
		{"multi-tenant nested", "/acme/order/v2", "acme", "order/v2"},
		{"multi-tenant deep", "/globex/foo/bar/baz", "globex", "foo/bar/baz"},
		{"legacy single segment", "/order", "", "order"},
		{"legacy with subpath", "/order_root", "", "order_root"},
		// Trim ведущего '/' и пустой ввод — конкретный handler потом
		// вернёт 400 на пустой node_path.
		{"empty raw", "", "", ""},
		{"only slash", "/", "", ""},
		// Без ведущего '/' (на всякий случай — Gin его всегда даёт, но
		// функция остаётся robust):
		{"no leading slash", "acme/order", "acme", "order"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTeam, gotPath := splitTeamSlugAndPath(tc.raw)
			assert.Equal(t, tc.wantTeamSlug, gotTeam, "team_slug mismatch")
			assert.Equal(t, tc.wantNodePath, gotPath, "node_path mismatch")
		})
	}
}
