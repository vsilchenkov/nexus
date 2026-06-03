//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// makeNodeWithHeaders создаёт узел с заданными forward_headers на реальном PG.
func makeNodeWithHeaders(t *testing.T, ctx context.Context, nodeRepo *pgrepo.NodeRepoPg, teamID, path string, headers []string) {
	t.Helper()
	n := &domain.Node{Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com", ForwardHeaders: headers}
	n.SetDefaults()
	n.TeamID = teamID
	if err := nodeRepo.Create(ctx, n); err != nil {
		t.Fatalf("create node %q: %v", path, err)
	}
}

// TestHeaderCatalog_Repo_E2E: справочник заголовков на реальном PG (§24).
// Проверяет case-insensitive UNIQUE, prefix-поиск и usage_count on-read из
// nodes.forward_headers (с учётом регистронезависимости).
func TestHeaderCatalog_Repo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewHeaderCatalogRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	// Create + case-insensitive UNIQUE.
	h := &domain.HeaderCatalogEntry{Name: "X-Request-ID", Description: "trace id", CreatedBy: "admin"}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	dup := &domain.HeaderCatalogEntry{Name: "x-request-id"}
	if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrHeaderAlreadyExists) {
		t.Fatalf("dup create err = %v, want ErrHeaderAlreadyExists", err)
	}

	// GetByName — без учёта регистра.
	got, err := repo.GetByName(ctx, "X-REQUEST-ID")
	if err != nil || got.ID != h.ID {
		t.Fatalf("GetByName case-insensitive: got=%+v err=%v", got, err)
	}

	// usage_count on-read: считается из forward_headers узлов (регистр игнор).
	_ = repo.Create(ctx, &domain.HeaderCatalogEntry{Name: "X-Trace"})
	makeNodeWithHeaders(t, ctx, nodeRepo, teamID, "svc/a", []string{"X-Trace", "X-Request-ID"})
	makeNodeWithHeaders(t, ctx, nodeRepo, teamID, "svc/b", []string{"x-trace"}) // другой регистр

	res, err := repo.Search(ctx, "X-Trace", 20)
	if err != nil || len(res) == 0 {
		t.Fatalf("search X-Trace: res=%+v err=%v", res, err)
	}
	if res[0].Name != "X-Trace" || res[0].UsageCount != 2 {
		t.Fatalf("X-Trace usage_count = %d (name %q), want 2 (case-insensitive on-read)", res[0].UsageCount, res[0].Name)
	}

	// Prefix-поиск + сортировка по usage_count desc: X-Trace(2) выше X-Request-ID(1).
	prefix, err := repo.Search(ctx, "X-", 20)
	if err != nil {
		t.Fatalf("search prefix: %v", err)
	}
	if len(prefix) < 2 || prefix[0].Name != "X-Trace" {
		t.Fatalf("prefix order = %+v, want X-Trace first", names(prefix))
	}
}

// TestHeaderCatalog_Usecase_IdempotentCreate: повторный Create по
// case-insensitive имени возвращает существующую запись, а не ошибку (§24).
func TestHeaderCatalog_Usecase_IdempotentCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	repo := pgrepo.NewHeaderCatalogRepoPg(pool, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uc := usecase.NewHeaderCatalogUsecase(repo, auditUC, logger)

	first := &domain.HeaderCatalogEntry{Name: "X-Internal-Job-ID"}
	if err := uc.Create(ctx, usecase.SystemActor(), first); err != nil {
		t.Fatalf("create: %v", err)
	}
	second := &domain.HeaderCatalogEntry{Name: "x-internal-JOB-id"}
	if err := uc.Create(ctx, usecase.SystemActor(), second); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent create returned different id: %q vs %q", second.ID, first.ID)
	}
}

func names(items []*domain.HeaderCatalogEntry) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.Name
	}
	return out
}
