//go:build integration

// Package integration — end-to-end тесты Web-репозиториев на реальном
// PostgreSQL через testcontainers (§10.1, §17.4 ТЗ).
//
// Запуск: `make test-integration` или `go test -tags=integration ./tests/integration/...`
// Требуется Docker daemon.
package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
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
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	n := &domain.Node{
		Path:       "test/path",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
		Comment:    "интеграция с системой X — события заказов", // §29: round-trip
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
	// §29: комментарий должен сохраниться и прочитаться без искажений.
	if got.Comment != "интеграция с системой X — события заказов" {
		t.Fatalf("comment round-trip mismatch: %q", got.Comment)
	}
	// §36: DLQ-настройки (новые столбцы) должны заполниться дефолтами и
	// прочитаться обратно без рассинхрона $-параметров (см. неочевидность B1).
	if got.DLQTTLSeconds != 86_400 {
		t.Fatalf("dlq_ttl_seconds round-trip mismatch: %d", got.DLQTTLSeconds)
	}
	if got.DLQRetryDelaySeconds != 300 {
		t.Fatalf("dlq_retry_delay_seconds round-trip mismatch: %d", got.DLQRetryDelaySeconds)
	}

	// Audit-запись должна существовать (атомарная транзакция).
	entries, err := auditRepo.List(ctx, port.AuditFilter{TargetID: n.ID, Limit: 10})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != domain.ActionNodeCreate {
		t.Fatalf("expected one node.create audit entry, got %+v", entries)
	}

	// §29: Update должен переписать комментарий (проверяем после audit-ассерта,
	// т.к. Update добавит свою audit-запись).
	got.Comment = "обновлённое описание"
	if err := nodeUC.Update(ctx, usecase.SystemActor(), got, ""); err != nil {
		t.Fatalf("update node: %v", err)
	}
	after, err := nodeRepo.Get(ctx, n.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.Comment != "обновлённое описание" {
		t.Fatalf("comment after update mismatch: %q", after.Comment)
	}
}

// TestNodeRepoRabbitMQAsync_E2E: создаём узел RabbitMQAsync (§27), проверяем
// round-trip полей rmq_*/pull_*, что rmq_password шифруется/дешифруется и не
// хранится в открытом виде, и что миграция 0014 + chk_rmq_fields на месте.
func TestNodeRepoRabbitMQAsync_E2E(t *testing.T) {
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
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	n := &domain.Node{
		Path:        "billing-events",
		RootMethod:  domain.RootMethodRabbitMQAsync,
		TargetURL:   "https://api.partner.com/billing/webhook",
		RMQHost:     "rmq.internal",
		RMQQueue:    "billing.events.outbound",
		RMQUser:     "nexus-billing",
		RMQPassword: "s3cr3t-amqp",
		// несовместимое поле — usecase обязан сбросить его (§27.6).
		URLMode: domain.URLModeFromRequest,
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create rmq node: %v", err)
	}

	got, err := nodeRepo.Get(ctx, n.ID)
	if err != nil {
		t.Fatalf("get rmq node: %v", err)
	}
	if got.RMQHost != "rmq.internal" || got.RMQQueue != "billing.events.outbound" {
		t.Fatalf("rmq fields round-trip mismatch: %+v", got)
	}
	if got.RMQPassword != "s3cr3t-amqp" {
		t.Fatalf("rmq_password decrypt mismatch: %q", got.RMQPassword)
	}
	if got.RMQVHost != "/" || got.RMQPort != 5672 || got.PullIntervalSec != 5 ||
		got.PullBatchSize != 100 || got.PullPrefetch != 100 {
		t.Fatalf("pull defaults not persisted: %+v", got)
	}
	if got.URLMode != domain.URLModeStatic {
		t.Fatalf("incompatible url_mode not cleared: %q", got.URLMode)
	}

	// rmq_password не должен лежать в БД в открытом виде.
	var raw string
	if err := pool.QueryRow(ctx, "SELECT rmq_password FROM nodes WHERE id = $1", n.ID).Scan(&raw); err != nil {
		t.Fatalf("read raw rmq_password: %v", err)
	}
	if raw == "s3cr3t-amqp" || raw == "" {
		t.Fatalf("rmq_password stored in plaintext or empty: %q", raw)
	}

	// chk_rmq_fields: прямой INSERT RabbitMQAsync без queue должен упасть.
	_, err = pool.Exec(ctx,
		`INSERT INTO nodes (path, root_method, target_url, rmq_host, team_id)
		 VALUES ('bad-rmq', 'RabbitMQAsync', 'https://x', 'h', $1)`, defaultTeam)
	if err == nil {
		t.Fatal("expected chk_rmq_fields to reject node without queue/interval")
	}
}

// TestNodeRepoIncomingAuthDynamic_E2E (§41): round-trip входящих динамических
// колонок (incoming_auth_dynamic_source/field) через NodeUsecase + node_repo,
// и проверка, что дефолты (header/Authorization) проставляются для узлов,
// которые их не задают.
func TestNodeRepoIncomingAuthDynamic_E2E(t *testing.T) {
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
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	// Узел с входящим token из query-параметра apikey.
	n := &domain.Node{
		Path:                      "dyn/incoming",
		RootMethod:                domain.RootMethodRequest,
		URLMode:                   domain.URLModeStatic,
		TargetURL:                 "https://example.com/hook",
		IncomingAuthType:          domain.IncomingAuthTypeToken,
		IncomingAuthCredentials:   "s3cr3t",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
		IncomingAuthDynamicField:  "apikey",
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}
	got, err := nodeRepo.Get(ctx, n.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.IncomingAuthDynamicSource != domain.IncomingAuthSourceQuery {
		t.Fatalf("incoming source round-trip mismatch: %q", got.IncomingAuthDynamicSource)
	}
	if got.IncomingAuthDynamicField != "apikey" {
		t.Fatalf("incoming field round-trip mismatch: %q", got.IncomingAuthDynamicField)
	}

	// Узел без явных входящих динамических полей → дефолты header/Authorization.
	n2 := &domain.Node{
		Path:       "dyn/defaults",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n2); err != nil {
		t.Fatalf("create node2: %v", err)
	}
	got2, err := nodeRepo.Get(ctx, n2.ID)
	if err != nil {
		t.Fatalf("get node2: %v", err)
	}
	if got2.IncomingAuthDynamicSource != domain.IncomingAuthSourceHeader ||
		got2.IncomingAuthDynamicField != "Authorization" {
		t.Fatalf("incoming dynamic defaults not applied: src=%q field=%q",
			got2.IncomingAuthDynamicSource, got2.IncomingAuthDynamicField)
	}
}

// TestNodeRepoGet_InvalidUUID_E2E: невалидный `:id` (не UUID) в Get → not-found
// (404), а не 500 + шум в Sentry (NEXUS-7, §44.I). Запрос приводит `$1::uuid`,
// pg отдаёт SQLSTATE 22P02 — репозитории трактуют его как Err…NotFound. Имя с
// префиксом TestNodeRepo — чтобы попасть под фильтр прогона test-int-pg.
func TestNodeRepoGet_InvalidUUID_E2E(t *testing.T) {
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
	userRepo := pgrepo.NewUserRepoPg(pool, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)

	const badID = "not-a-uuid"

	if _, err := nodeRepo.Get(ctx, badID); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("node Get(bad uuid): want ErrNodeNotFound, got %v", err)
	}
	if _, err := userRepo.Get(ctx, badID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("user Get(bad uuid): want ErrUserNotFound, got %v", err)
	}
	if _, err := teamRepo.GetByID(ctx, badID); !errors.Is(err, domain.ErrTeamNotFound) {
		t.Fatalf("team GetByID(bad uuid): want ErrTeamNotFound, got %v", err)
	}
}

// nopCache — заглушка port.NodeCache: NodeUsecase кеширует через write-through,
// но в тестах кеш не нужен; ошибки игнорируем.
type nopCache struct{}

func (nopCache) Get(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopCache) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNotFound
}
func (nopCache) Set(_ context.Context, _ *domain.Node, _ time.Duration) error { return nil }
func (nopCache) Invalidate(_ context.Context, _ string) error                 { return nil }
func (nopCache) InvalidateByPath(_ context.Context, _ string) error           { return nil }
