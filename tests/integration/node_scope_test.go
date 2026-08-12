//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase/port"
)

// TestNodeScope_Filters_E2E (§86.3/§86.4): сквозной скоуп и сужение по id узлов
// на реальном PostgreSQL.
//
// Integration, а не unit: обе формы — `team_id = ANY($1)` и новая
// `id = ANY($n::uuid[])` — проверяются только настоящим PostgreSQL (кодирование
// []string в pgx, приведение к uuid[]). Главное утверждение теста — сужение по
// id идёт ПОВЕРХ команды, а не вместо неё: иначе набор id стал бы способом
// прочитать чужую команду.
func TestNodeScope_Filters_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)

	alpha := &domain.Team{Slug: "alpha86n", Name: "Alpha", CHDatabase: "nexus_alpha86n"}
	beta := &domain.Team{Slug: "beta86n", Name: "Beta", CHDatabase: "nexus_beta86n"}
	for _, tm := range []*domain.Team{alpha, beta} {
		if err := teamRepo.Create(ctx, tm); err != nil {
			t.Fatalf("create team %s: %v", tm.Slug, err)
		}
	}

	// Одинаковый путь в разных командах — это норма (UNIQUE(team_id, path)) и
	// ровно тот случай, ради которого §86.7 сшивает строки по id, а не по пути.
	mk := func(teamID, path string) *domain.Node {
		n := &domain.Node{Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com"}
		n.SetDefaults()
		n.TeamID = teamID
		if err := repo.Create(ctx, n); err != nil {
			t.Fatalf("create node %s/%s: %v", teamID, path, err)
		}
		return n
	}
	aParcel := mk(alpha.ID, "svc/parcel")
	aGeo := mk(alpha.ID, "svc/geo")
	bParcel := mk(beta.ID, "svc/parcel")

	paths := func(nodes []*domain.Node) map[string]int {
		got := map[string]int{}
		for _, n := range nodes {
			got[n.ID]++
		}
		return got
	}

	// Сквозной скоуп: обе команды, включая одноимённые узлы.
	got, err := repo.List(ctx, port.ListNodesFilter{TeamIDs: []string{alpha.ID, beta.ID}})
	if err != nil {
		t.Fatalf("list by TeamIDs: %v", err)
	}
	ids := paths(got)
	if ids[aParcel.ID] != 1 || ids[bParcel.ID] != 1 || ids[aGeo.ID] != 1 {
		t.Fatalf("сквозной скоуп потерял узлы: %v", ids)
	}

	// Сужение по id внутри сквозного скоупа.
	got, err = repo.List(ctx, port.ListNodesFilter{
		TeamIDs: []string{alpha.ID, beta.ID},
		IDs:     []string{aParcel.ID, bParcel.ID},
	})
	if err != nil {
		t.Fatalf("list by IDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("сужение по id вернуло %d узлов, ожидалось 2", len(got))
	}

	// ГЛАВНОЕ: id чужой команды не проходит — сужение работает ПОВЕРХ команды.
	got, err = repo.List(ctx, port.ListNodesFilter{
		TeamID: alpha.ID,
		IDs:    []string{bParcel.ID},
	})
	if err != nil {
		t.Fatalf("list foreign id: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("узел чужой команды прошёл через набор id: %d", len(got))
	}

	// Однокомандный путь не задет.
	got, err = repo.List(ctx, port.ListNodesFilter{TeamID: alpha.ID})
	if err != nil {
		t.Fatalf("list by TeamID: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("однокомандный скоуп изменился: %d узлов", len(got))
	}
}
