//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/queuecancel"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
	webuc "nexus/internal/web/usecase"
	webport "nexus/internal/web/usecase/port"
)

// pfNodeRepo — минимальный NodeRepo: PurgeFailed зовёт только Get.
type pfNodeRepo struct {
	webport.NodeRepo
	node *domain.Node
}

func (r *pfNodeRepo) Get(_ context.Context, _ string) (*domain.Node, error) { return r.node, nil }

// pfAuditRepo — noop-аудит со счётчиком записей.
type pfAuditRepo struct{ writes int }

func (r *pfAuditRepo) Write(_ context.Context, _ *domain.AuditEntry) error { r.writes++; return nil }
func (r *pfAuditRepo) List(_ context.Context, _ webport.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (r *pfAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) { return 0, nil }

// TestAsyncQueue_PurgeFailed_E2E (§35/§36): очистка «Неудачных доставок» —
// (1) отменяет (qcancel-tombstone в Redis) ID неудачных, чтобы DLQ-репроцессор
// перестал их повторять, и (2) lightweight-DELETE'ит записи done=0 из CH-таблицы
// узла. Записи done=1 остаются; их ID НЕ отменяются.
func TestAsyncQueue_PurgeFailed_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, chCfg, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()
	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	const table = "nexus_default.purge_failed"
	createNodeLogTable(t, ctx, conn, table)

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	writer := chlog.New(provider, chCfg, logger)
	defer writer.Stop(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(id string, status int32, done bool) *domain.LogRecord {
		return &domain.LogRecord{
			ID: id, Type: domain.RootMethodRequestAsync, URL: "https://x", Method: "POST",
			Request: "{}", Response: "{}", Status: status, DateCreate: now,
			DateRequest: now, DateResponse: now, Duration: 1, Done: done,
			ChecksumRequest: strings.Repeat("a", 32), ChecksumResponse: strings.Repeat("b", 32),
			Host: "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
		}
	}
	failedIDs := []string{
		"00000000-0000-0000-0000-0000000000f1",
		"00000000-0000-0000-0000-0000000000f2",
		"00000000-0000-0000-0000-0000000000f3",
	}
	const okID = "00000000-0000-0000-0000-0000000000aa"
	for _, id := range failedIDs {
		writer.Write(ctx, table, mk(id, 0, false))
	}
	writer.Write(ctx, table, mk(okID, 200, true))
	require.NoError(t, writer.Flush(ctx))

	// Дождаться всех 4 строк.
	deadline := time.Now().Add(20 * time.Second)
	var total uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&total))
		if total >= 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 4, total)

	cancelSet := queuecancel.New(redisClient)
	logReader := webch.NewLogReader(provider, logger)
	node := &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", ClickHouseTable: table}
	auditRepo := &pfAuditRepo{}
	aqUC := webuc.NewAsyncQueueUsecase(nil, cancelSet, logReader, &pfNodeRepo{node: node},
		webuc.NewAuditUsecase(auditRepo, logger),
		"nexus-sender", "nexus.async", time.Hour, 1000, logger)

	// PurgeFailed всё (нулевое окно): чистит 3 done=0, отменяет их ID.
	r, err := aqUC.PurgeFailed(ctx, webuc.SystemActor(), "n1", "t1", time.Time{}, time.Time{})
	require.NoError(t, err)
	require.EqualValues(t, 3, r.Cancelled, "3 неудачные записи очищены")

	// Неудачные ID — tombstoned (репроцессор дропнет при следующем проходе).
	for _, id := range failedIDs {
		c, e := cancelSet.IsCancelled(ctx, id)
		require.NoError(t, e)
		require.True(t, c, "failed id %s must be tombstoned", id)
	}
	// done=1 ID — НЕ отменён.
	cOK, e := cancelSet.IsCancelled(ctx, okID)
	require.NoError(t, e)
	require.False(t, cOK, "delivered id must not be cancelled")

	// CH: записи done=0 удалены (вид «Неудачные» чист), done=1 осталась.
	require.Eventually(t, func() bool {
		n, qe := logReader.CountFailed(ctx, table, "", 0, 0)
		return qe == nil && n == 0
	}, 20*time.Second, 500*time.Millisecond, "failed records must be deleted from CH")

	var remaining uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&remaining))
	require.EqualValues(t, 1, remaining, "delivered (done=1) record stays")
	require.Equal(t, 1, auditRepo.writes, "одна audit-запись очистки")
}
