//go:build integration

// Package integration — end-to-end тесты Web-репозиториев на реальном
// PostgreSQL через testcontainers (§10.1, §17.4 ТЗ).
//
// Запуск: `make test-integration` или `go test -tags=integration ./tests/integration/...`
// Требуется Docker daemon.
package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

const testEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes base64

func startPostgres(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()

	c, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("nexus"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}

	// Накатываем миграции из ../migrations
	mp, _ := filepath.Abs("../../migrations")
	mg, err := pgpf.NewMigratorFromDSN(dsn, mp, logging.NewNoop())
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := mg.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	mg.Close()

	cleanup := func() {
		pool.Close()
		_ = c.Terminate(ctx)
	}
	return pool, cleanup
}

// resolveDefaultTeamID — UUID 'default'-team из сидинга миграции 0008.
// Используется в integration-тестах NodeUsecase, которым нужен UUID
// для передачи в конструктор.
func resolveDefaultTeamID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		"SELECT id::text FROM teams WHERE slug = $1",
		"default").Scan(&id); err != nil {
		t.Fatalf("resolve default team: %v", err)
	}
	return id
}

// TestNodeRepoCreate_E2E: создаём узел через NodeUsecase с UnitOfWork.
// Проверяем, что узел сохранён в БД и в audit_log появилась запись.
func TestNodeRepoCreate_E2E(t *testing.T) {
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
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, time.Minute, 0, defaultTeam, logger)

	n := &domain.Node{
		Path:       "test/path",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if n.ID == "" {
		t.Fatal("node id is empty after create")
	}

	got, err := nodeRepo.Get(ctx, n.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Path != "test/path" {
		t.Fatalf("path mismatch: %q", got.Path)
	}

	// Audit-запись должна существовать (атомарная транзакция).
	entries, err := auditRepo.List(ctx, port.AuditFilter{TargetID: n.ID, Limit: 10})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != domain.ActionNodeCreate {
		t.Fatalf("expected one node.create audit entry, got %+v", entries)
	}
}

// nopCache — заглушка port.NodeCache: NodeUsecase кеширует через write-through,
// но в тестах кеш не нужен; ошибки игнорируем.
type nopCache struct{}

func (nopCache) Get(_ context.Context, _ string) (*domain.Node, error) { return nil, domain.ErrNotFound }
func (nopCache) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopCache) Set(_ context.Context, _ *domain.Node, _ time.Duration) error { return nil }
func (nopCache) Invalidate(_ context.Context, _ string) error                  { return nil }
func (nopCache) InvalidateByPath(_ context.Context, _ string) error            { return nil }
