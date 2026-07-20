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
	"nexus/internal/web/usecase"
)

// TestNodeRepoCountByCHTable_E2E: подсчёт узлов, делящих одну CH-таблицу.
//
// Это защита переноса узла между командами: общая таблица логов не должна
// уезжать за одним узлом (иначе остальные остаются со ссылкой на
// несуществующую таблицу и их логирование тихо ломается). Подсчёт идёт по
// ВСЕМ командам — как раз потому, что после переноса «соседи» оказываются в
// разных командах.
func TestNodeRepoCountByCHTable_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	logger := logging.NewNoop()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil,
		time.Minute, 0, defaultTeam, nil, logger)

	// Три узла на общей таблице + один со своей.
	shared := make([]*domain.Node, 0, 3)
	for _, p := range []string{"svc/a", "svc/b", "svc/c"} {
		n := &domain.Node{
			Path: p, RootMethod: domain.RootMethodRequest, URLMode: domain.URLModeStatic,
			TargetURL: "https://example.com/hook", ClickHouseTable: "shared_logs",
		}
		if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		shared = append(shared, n)
	}
	lonely := &domain.Node{
		Path: "svc/solo", RootMethod: domain.RootMethodRequest, URLMode: domain.URLModeStatic,
		TargetURL: "https://example.com/hook", ClickHouseTable: "solo_logs",
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), lonely); err != nil {
		t.Fatalf("create solo: %v", err)
	}

	// Имя таблицы нормализуется до "<ch_database>.<table>" (§10.C.2), поэтому
	// считаем по фактически сохранённому значению, а не по исходной строке.
	stored, err := nodeRepo.Get(ctx, shared[0].ID)
	if err != nil {
		t.Fatalf("get shared node: %v", err)
	}
	sharedTable := stored.ClickHouseTable

	tests := []struct {
		name    string
		table   string
		exclude string
		want    int
	}{
		{name: "общая таблица без исключений", table: sharedTable, want: 3},
		{name: "общая таблица минус сам узел", table: sharedTable, exclude: shared[0].ID, want: 2},
		{name: "личная таблица минус сам узел", table: lonelyTable(t, ctx, nodeRepo, lonely.ID), exclude: lonely.ID, want: 0},
		{name: "неизвестная таблица", table: "nexus_default.nope", want: 0},
		{name: "пустое имя таблицы", table: "", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nodeRepo.CountByCHTable(ctx, tc.table, tc.exclude)
			if err != nil {
				t.Fatalf("count by ch table: %v", err)
			}
			if got != tc.want {
				t.Fatalf("count(%q, exclude=%q) = %d, want %d", tc.table, tc.exclude, got, tc.want)
			}
		})
	}

	// Несуществующий UUID в exclude не должен ронять запрос (::uuid-каст).
	if _, err := nodeRepo.CountByCHTable(ctx, sharedTable, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("count with unknown exclude id: %v", err)
	}
}

func lonelyTable(t *testing.T, ctx context.Context, repo *pgrepo.NodeRepoPg, id string) string {
	t.Helper()
	n, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get lonely node: %v", err)
	}
	return n.ClickHouseTable
}
