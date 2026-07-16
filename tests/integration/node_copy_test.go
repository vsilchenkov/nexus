//go:build integration

package integration

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// TestNodeUC_Copy_E2E (§53): копирование узла на реальном PG с UnitOfWork.
// Проверяет round-trip кредов через шифрование (decrypt источника →
// re-encrypt копии), принудительный paused, клонирование привязок
// allowlist-хостов со снимком, audit-запись node.copy, конфликт path и
// team-scope.
func TestNodeUC_Copy_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	hostRepo := pgrepo.NewHostAllowlistRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	// Источник: enabled-узел from_request с кредами и привязанным хостом.
	src := &domain.Node{
		Path:                    "copy/source",
		RootMethod:              domain.RootMethodRequest,
		URLMode:                 domain.URLModeFromRequest,
		TargetURL:               "https://example.com/hook",
		Status:                  domain.NodeStatusEnabled,
		AuthType:                domain.AuthTypeBasic,
		AuthCredentials:         "svc-user:sup3r-secret",
		IncomingAuthType:        domain.IncomingAuthTypeToken,
		IncomingAuthCredentials: "incoming-t0ken",
		Comment:                 "источник для копии",
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	host := &domain.HostAllowlistEntry{Pattern: "api.partner.com", Kind: domain.HostKindExact}
	if err := hostRepo.Create(ctx, host); err != nil {
		t.Fatalf("create host: %v", err)
	}
	if err := hostRepo.Link(ctx, src.ID, host.ID); err != nil {
		t.Fatalf("link host: %v", err)
	}

	// Копирование.
	clone, err := nodeUC.Copy(ctx, usecase.SystemActor(), src.ID, "copy/clone", defaultTeam)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if clone.ID == "" || clone.ID == src.ID {
		t.Fatalf("clone id invalid: %q", clone.ID)
	}

	// Round-trip: копия читается из БД с теми же plaintext-кредами
	// (доказывает decrypt источника → re-encrypt копии).
	got, err := nodeRepo.Get(ctx, clone.ID)
	if err != nil {
		t.Fatalf("get clone: %v", err)
	}
	if got.AuthCredentials != "svc-user:sup3r-secret" {
		t.Fatalf("auth creds round-trip mismatch: %q", got.AuthCredentials)
	}
	if got.IncomingAuthCredentials != "incoming-t0ken" {
		t.Fatalf("incoming creds round-trip mismatch: %q", got.IncomingAuthCredentials)
	}
	if got.Status != domain.NodeStatusPaused {
		t.Fatalf("clone status = %q, want paused", got.Status)
	}
	if got.Path != "copy/clone" || got.Comment != "источник для копии" {
		t.Fatalf("clone fields mismatch: path=%q comment=%q", got.Path, got.Comment)
	}

	// Креды копии не лежат в БД в открытом виде.
	var raw string
	if err := pool.QueryRow(ctx, "SELECT auth_credentials FROM nodes WHERE id = $1", clone.ID).Scan(&raw); err != nil {
		t.Fatalf("read raw creds: %v", err)
	}
	if raw == "svc-user:sup3r-secret" || raw == "" {
		t.Fatalf("clone creds stored in plaintext or empty: %q", raw)
	}

	// Привязки хостов склонированы, снимок пересобран.
	links, err := hostRepo.ListByNode(ctx, clone.ID)
	if err != nil {
		t.Fatalf("list clone links: %v", err)
	}
	if len(links) != 1 || links[0].ID != host.ID {
		t.Fatalf("clone host links = %+v, want 1 link to %s", links, host.ID)
	}
	if !slices.Contains(got.URLAllowedHosts, "api.partner.com") {
		t.Fatalf("clone url_allowed_hosts snapshot = %v, want api.partner.com", got.URLAllowedHosts)
	}

	// Источник не изменён.
	srcAfter, err := nodeRepo.Get(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source after copy: %v", err)
	}
	if srcAfter.Status != domain.NodeStatusEnabled || srcAfter.Path != "copy/source" {
		t.Fatalf("source mutated by copy: %+v", srcAfter)
	}

	// Audit: ровно одна запись node.copy на копию, с провенансом.
	entries, err := auditRepo.List(ctx, port.AuditFilter{TargetID: clone.ID, Limit: 10})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != domain.ActionNodeCopy {
		t.Fatalf("expected one node.copy audit entry, got %+v", entries)
	}
	if entries[0].Details["source_node_id"] != src.ID || entries[0].Details["source_path"] != "copy/source" {
		t.Fatalf("audit details mismatch: %+v", entries[0].Details)
	}

	// Конфликт path (UNIQUE(team_id, path)) → ErrNodeAlreadyExists.
	if _, err := nodeUC.Copy(ctx, usecase.SystemActor(), src.ID, "copy/clone", defaultTeam); !errors.Is(err, domain.ErrNodeAlreadyExists) {
		t.Fatalf("dup copy err = %v, want ErrNodeAlreadyExists", err)
	}

	// Team-scope: чужая команда → ErrNodeNotFound.
	if _, err := nodeUC.Copy(ctx, usecase.SystemActor(), src.ID, "copy/foreign", "00000000-0000-0000-0000-000000000001"); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("foreign team copy err = %v, want ErrNodeNotFound", err)
	}
}
