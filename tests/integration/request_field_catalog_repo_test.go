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

// makeNodeWithDynFields создаёт узел с заданными именами динамических полей
// (исходящего auth_dynamic_field и входящего incoming_auth_dynamic_field).
func makeNodeWithDynFields(t *testing.T, ctx context.Context, nodeRepo *pgrepo.NodeRepoPg, teamID, path, authField, incField string) {
	t.Helper()
	n := &domain.Node{
		Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com",
		AuthDynamicField:         authField,
		IncomingAuthDynamicField: incField,
	}
	n.SetDefaults()
	n.TeamID = teamID
	if err := nodeRepo.Create(ctx, n); err != nil {
		t.Fatalf("create node %q: %v", path, err)
	}
}

// TestRequestFieldCatalog_Repo_E2E (§41): справочник полей запроса на реальном
// PG. Проверяет case-insensitive UNIQUE, prefix-поиск и usage_count on-read из
// ОБЕИХ колонок (auth_dynamic_field и incoming_auth_dynamic_field).
func TestRequestFieldCatalog_Repo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewRequestFieldCatalogRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	// Create + case-insensitive UNIQUE.
	e := &domain.RequestFieldCatalogEntry{Name: "apikey", Description: "client api key", CreatedBy: "admin"}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	dup := &domain.RequestFieldCatalogEntry{Name: "ApiKey"}
	if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrRequestFieldAlreadyExists) {
		t.Fatalf("dup create err = %v, want ErrRequestFieldAlreadyExists", err)
	}

	// GetByName — без учёта регистра.
	got, err := repo.GetByName(ctx, "APIKEY")
	if err != nil || got.ID != e.ID {
		t.Fatalf("GetByName case-insensitive: got=%+v err=%v", got, err)
	}

	// usage_count on-read: имя в исходящем И во входящем поле разных узлов → 2.
	makeNodeWithDynFields(t, ctx, nodeRepo, teamID, "svc/out", "apikey", "Authorization")
	makeNodeWithDynFields(t, ctx, nodeRepo, teamID, "svc/in", "token", "apikey")

	res, err := repo.Search(ctx, "apikey", 20)
	if err != nil || len(res) == 0 {
		t.Fatalf("search apikey: res=%+v err=%v", res, err)
	}
	if res[0].Name != "apikey" || res[0].UsageCount != 2 {
		t.Fatalf("apikey usage_count = %d, want 2 (обе колонки, case-insensitive)", res[0].UsageCount)
	}
}

// TestRequestFieldCatalog_Usecase_IdempotentCreate (§41): повторный Create по
// case-insensitive имени возвращает существующую запись, а не ошибку.
func TestRequestFieldCatalog_Usecase_IdempotentCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	repo := pgrepo.NewRequestFieldCatalogRepoPg(pool, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uc := usecase.NewRequestFieldCatalogUsecase(repo, auditUC, logger)

	first := &domain.RequestFieldCatalogEntry{Name: "X-Auth-Token"}
	if err := uc.Create(ctx, usecase.SystemActor(), first); err != nil {
		t.Fatalf("create: %v", err)
	}
	second := &domain.RequestFieldCatalogEntry{Name: "x-auth-TOKEN"}
	if err := uc.Create(ctx, usecase.SystemActor(), second); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent create returned different id: %q vs %q", second.ID, first.ID)
	}
}
