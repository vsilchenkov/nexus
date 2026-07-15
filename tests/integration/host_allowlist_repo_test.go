//go:build integration

package integration

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// makeFromRequestNode создаёт узел url_mode=from_request на реальном PG для
// тестов привязки хостов (§23).
func makeFromRequestNode(t *testing.T, ctx context.Context, nodeRepo *pgrepo.NodeRepoPg, teamID, path string) *domain.Node {
	t.Helper()
	n := &domain.Node{Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com"}
	n.SetDefaults()
	n.URLMode = domain.URLModeFromRequest
	n.TeamID = teamID
	if err := nodeRepo.Create(ctx, n); err != nil {
		t.Fatalf("create node %q: %v", path, err)
	}
	return n
}

// TestHostAllowlist_Repo_E2E: каталог разрешённых хостов на реальном PG (§23).
// Проверяет case-insensitive UNIQUE, trigger usage_count при link/unlink,
// FK RESTRICT на удаление используемого паттерна и ListByNode.
func TestHostAllowlist_Repo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	repo := pgrepo.NewHostAllowlistRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	// Create + case-insensitive UNIQUE.
	exact := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact, CreatedBy: "admin"}
	if err := repo.Create(ctx, exact); err != nil {
		t.Fatalf("create exact: %v", err)
	}
	if exact.ID == "" {
		t.Fatal("host id empty after create")
	}
	dup := &domain.HostAllowlistEntry{Pattern: "API.Partner.COM", Kind: domain.HostKindExact}
	if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrHostAlreadyExists) {
		t.Fatalf("dup create err = %v, want ErrHostAlreadyExists", err)
	}

	// GetByPattern — без учёта регистра.
	got, err := repo.GetByPattern(ctx, "api.PARTNER.com")
	if err != nil || got.ID != exact.ID {
		t.Fatalf("GetByPattern case-insensitive: got=%+v err=%v", got, err)
	}

	// Search с фильтром по kind.
	wild := &domain.HostAllowlistEntry{Pattern: "*.notify.io", Kind: domain.HostKindWildcard}
	if err := repo.Create(ctx, wild); err != nil {
		t.Fatalf("create wildcard: %v", err)
	}
	exactOnly, err := repo.Search(ctx, "", domain.HostKindExact, 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(exactOnly) != 1 || exactOnly[0].Kind != domain.HostKindExact {
		t.Fatalf("kind filter returned %+v", exactOnly)
	}

	node := makeFromRequestNode(t, ctx, nodeRepo, teamID, "svc/hook")

	// Link → trigger инкрементит usage_count; повторный Link идемпотентен.
	if err := repo.Link(ctx, node.ID, exact.ID); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := repo.Link(ctx, node.ID, exact.ID); err != nil {
		t.Fatalf("link again: %v", err)
	}
	after, _ := repo.Get(ctx, exact.ID)
	if after.UsageCount != 1 {
		t.Fatalf("usage_count after double link = %d, want 1 (trigger + ON CONFLICT)", after.UsageCount)
	}

	// FK RESTRICT: удалить используемый паттерн нельзя.
	if err := repo.Delete(ctx, exact.ID); !errors.Is(err, domain.ErrHostInUse) {
		t.Fatalf("delete in-use err = %v, want ErrHostInUse", err)
	}

	// ListByNode.
	byNode, err := repo.ListByNode(ctx, node.ID)
	if err != nil || len(byNode) != 1 || byNode[0].ID != exact.ID {
		t.Fatalf("ListByNode = %+v err=%v", byNode, err)
	}

	// Unlink → usage_count назад в 0, теперь удаление проходит.
	if err := repo.Unlink(ctx, node.ID, exact.ID); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	after, _ = repo.Get(ctx, exact.ID)
	if after.UsageCount != 0 {
		t.Fatalf("usage_count after unlink = %d, want 0", after.UsageCount)
	}
	if err := repo.Delete(ctx, exact.ID); err != nil {
		t.Fatalf("delete after unlink: %v", err)
	}
}

// TestHostAllowlist_NodeDeleteCascade: удаление узла каскадит node_allowed_hosts,
// trigger декрементит usage_count (§23).
func TestHostAllowlist_NodeDeleteCascade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewHostAllowlistRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	h := &domain.HostAllowlistEntry{Pattern: "*.partner.com", Kind: domain.HostKindWildcard}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	node := makeFromRequestNode(t, ctx, nodeRepo, teamID, "svc/cascade")
	if err := repo.Link(ctx, node.ID, h.ID); err != nil {
		t.Fatalf("link: %v", err)
	}

	if err := nodeRepo.Delete(ctx, node.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	after, _ := repo.Get(ctx, h.ID)
	if after.UsageCount != 0 {
		t.Fatalf("usage_count after node delete = %d, want 0 (cascade + trigger)", after.UsageCount)
	}
}

// TestHostAllowlist_Usecase_SnapshotRebuild: через HostAllowlistUsecase.Attach/
// Detach проверяет, что денормализованный снимок nodes.url_allowed_hosts
// пересобирается с re:-кодированием regex (§23).
func TestHostAllowlist_Usecase_SnapshotRebuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewHostAllowlistRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	uc := usecase.NewHostAllowlistUsecase(repo, nodeRepo, nopCache{}, teamRepo, uow, auditUC, time.Minute, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	exact := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	rgx := &domain.HostAllowlistEntry{Pattern: `^x\.io$`, Kind: domain.HostKindRegex}
	if err := repo.Create(ctx, exact); err != nil {
		t.Fatalf("create exact: %v", err)
	}
	if err := repo.Create(ctx, rgx); err != nil {
		t.Fatalf("create regex: %v", err)
	}
	node := makeFromRequestNode(t, ctx, nodeRepo, teamID, "svc/snap")

	if err := uc.Attach(ctx, usecase.SystemActor(), node.ID, exact.ID, ""); err != nil {
		t.Fatalf("attach exact: %v", err)
	}
	if err := uc.Attach(ctx, usecase.SystemActor(), node.ID, rgx.ID, ""); err != nil {
		t.Fatalf("attach regex: %v", err)
	}

	snap, _ := nodeRepo.Get(ctx, node.ID)
	want := []string{"api.partner.com", `re:^x\.io$`}
	slices.Sort(snap.URLAllowedHosts)
	if !slices.Equal(snap.URLAllowedHosts, want) {
		t.Fatalf("snapshot = %v, want %v (re:-кодирование regex)", snap.URLAllowedHosts, want)
	}

	if err := uc.Detach(ctx, usecase.SystemActor(), node.ID, exact.ID, ""); err != nil {
		t.Fatalf("detach exact: %v", err)
	}
	snap, _ = nodeRepo.Get(ctx, node.ID)
	if !slices.Equal(snap.URLAllowedHosts, []string{`re:^x\.io$`}) {
		t.Fatalf("snapshot after detach = %v, want [re:^x\\.io$]", snap.URLAllowedHosts)
	}
}

// TestMigrations_0011_0012_DownUp: round-trip отката и повторного наката
// миграций каталогов (§23, §24) — down-файлы корректны.
func TestMigrations_0011_0012_DownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	regclass := func(name string) (exists bool) {
		var v *string
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1)::text", name).Scan(&v); err != nil {
			t.Fatalf("to_regclass %s: %v", name, err)
		}
		return v != nil
	}

	for _, tbl := range []string{"host_allowlist", "node_allowed_hosts", "headers_catalog"} {
		if !regclass(tbl) {
			t.Fatalf("table %s missing after Up", tbl)
		}
	}

	mp, _ := filepath.Abs("../../migrations")
	mg, err := pgpf.NewMigratorFromDSN(pool.Config().ConnString(), mp, logging.NewNoop())
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer mg.Close()

	// Откатываем всё до версии 10 (т.е. 0011 включительно), а не фиксированные
	// 2 шага — иначе тест ломается при каждой новой миграции поверх 0012
	// (так и случилось с 0013 manager-роли и 0014 RabbitMQAsync). Шагов =
	// текущая_версия − 10.
	ver, _, verr := mg.Status()
	if verr != nil {
		t.Fatalf("status: %v", verr)
	}
	steps := int(ver) - 10
	if steps < 2 {
		t.Fatalf("unexpected schema version %d (<12)", ver)
	}
	if err := mg.Down(steps); err != nil {
		t.Fatalf("down %d (to version 10): %v", steps, err)
	}
	for _, tbl := range []string{"host_allowlist", "node_allowed_hosts", "headers_catalog"} {
		if regclass(tbl) {
			t.Fatalf("table %s still present after Down", tbl)
		}
	}

	// Повторный накат.
	if err := mg.Up(); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	for _, tbl := range []string{"host_allowlist", "node_allowed_hosts", "headers_catalog"} {
		if !regclass(tbl) {
			t.Fatalf("table %s missing after re-Up", tbl)
		}
	}
}
