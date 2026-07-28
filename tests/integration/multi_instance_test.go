//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	senderusecase "nexus/internal/sender/usecase"
)

// §70: две ноды Nexus на ОДНОМ ClickHouse. Нода — самостоятельное развёртывание
// со своими PostgreSQL/Redis/Kafka; общий у них только сторедж логов. В тестах
// нода моделируется своим Guard'ом с собственным instance.id — этого достаточно,
// потому что маркер владения живёт в самом ClickHouse, а не в PostgreSQL.
const (
	instanceLegacy = ""   // нода, существовавшая до §70: базы nexus_<slug>
	instanceKZ     = "kz" // вторая нода: базы nexus_kz_<slug>
)

// fixedConn — ConnProvider поверх постоянного соединения (hot-reload здесь не
// участвует, поэтому Manager не нужен).
type fixedConn struct{ conn chdriver.Conn }

func (f fixedConn) Conn() chdriver.Conn { return f.conn }

func newGuard(conn chdriver.Conn, id string) *chpf.Guard {
	return chpf.NewGuard(fixedConn{conn}, domain.InstanceID(id), logging.NewNoop())
}

func chRowCount(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) uint64 {
	t.Helper()
	var n uint64
	// #nosec G201 -- имя таблицы задаётся самим тестом, пользовательского ввода нет.
	require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
	return n
}

// createLogTable — минимальная таблица логов с тем же партиционированием, что у
// боевых (PARTITION BY toYYYYMM(date_create)) — от него зависит housekeeping.
func createLogTable(t *testing.T, ctx context.Context, conn chdriver.Conn, table string) {
	t.Helper()
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	ID String, date_create Date, node_id String
) ENGINE = MergeTree PARTITION BY toYYYYMM(date_create) ORDER BY (date_create, ID)`, table)))
}

func insertOldRow(t *testing.T, ctx context.Context, conn chdriver.Conn, table, id string) {
	t.Helper()
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (ID, date_create, node_id) VALUES (?, ?, ?)", table),
		id, time.Now().AddDate(0, -6, 0), "node-1"))
}

// staticNodeLister — NodeLister поверх фиксированного списка узлов.
type staticNodeLister struct{ nodes []*domain.Node }

func (s staticNodeLister) ListForHousekeeping(context.Context) ([]*domain.Node, error) {
	return s.nodes, nil
}

// TestMultiInstance_ClaimAndForeign — базовый сценарий: каждая нода захватывает
// свою БД, чужую захватить не может, и строгий гейт её отклоняет.
func TestMultiInstance_ClaimAndForeign(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	legacy := newGuard(conn, instanceLegacy)
	kz := newGuard(conn, instanceKZ)

	require.NoError(t, legacy.Claim(ctx, "nexus_default"))
	require.NoError(t, kz.Claim(ctx, "nexus_kz_default"))
	require.NoError(t, legacy.Claim(ctx, "nexus_default"), "повторный захват своей БД идемпотентен")

	require.ErrorIs(t, kz.Claim(ctx, "nexus_default"), chpf.ErrForeignDatabase)

	v, owner, err := kz.Check(ctx, "nexus_default")
	require.NoError(t, err)
	assert.Equal(t, chpf.VerdictForeign, v)
	require.NotNil(t, owner)
	assert.Equal(t, instanceLegacy, owner.InstanceID)

	require.ErrorIs(t, kz.AssertOwnsTable(ctx, "nexus_default.logs"), chpf.ErrForeignDatabase)
	require.NoError(t, kz.AssertOwnsTable(ctx, "nexus_kz_default.logs"))
}

// TestMultiInstance_FirstRunGuard — §70.5. Нода со свежей PostgreSQL и пустым
// instance.id не стартует, если её БД в ClickHouse уже существует: значит, туда
// пишет другая нода. По маркеру две такие ноды неразличимы (обе пишут
// instance_id=”), поэтому гейт опирается именно на признак свежей PostgreSQL.
func TestMultiInstance_FirstRunGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	first := newGuard(conn, instanceLegacy)
	require.NoError(t, first.Claim(ctx, "nexus_default"))

	second := newGuard(conn, instanceLegacy)
	err := second.EnsureAll(ctx, []string{"nexus_default"}, chpf.EnsureOptions{FreshPG: true})
	require.ErrorIs(t, err, chpf.ErrFirstRunDatabaseExists)

	// Аварийный обход (--ch-adopt): PostgreSQL пересоздали, ClickHouse остался.
	require.NoError(t, second.EnsureAll(ctx, []string{"nexus_default"},
		chpf.EnsureOptions{FreshPG: true, Adopt: true}))

	// Чужой маркер не перебивается даже с Adopt.
	kz := newGuard(conn, instanceKZ)
	require.ErrorIs(t,
		kz.EnsureAll(ctx, []string{"nexus_default"}, chpf.EnsureOptions{FreshPG: false, Adopt: true}),
		chpf.ErrForeignDatabase)
}

// TestMultiInstance_AdoptLegacyDatabase — обновление действующей ноды до §70:
// её БД существует без маркера, PostgreSQL не свежая → база усыновляется.
func TestMultiInstance_AdoptLegacyDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_legacy"))

	g := newGuard(conn, instanceLegacy)
	v, _, err := g.Check(ctx, "nexus_legacy")
	require.NoError(t, err)
	assert.Equal(t, chpf.VerdictUnclaimed, v)

	require.NoError(t, g.EnsureAll(ctx, []string{"nexus_legacy"}, chpf.EnsureOptions{FreshPG: false}))

	v, _, err = g.Check(ctx, "nexus_legacy")
	require.NoError(t, err)
	assert.Equal(t, chpf.VerdictOwned, v, "после усыновления БД считается своей")
}

// TestMultiInstance_ConcurrentClaim — гонка захвата одной БД восемью нодами:
// CREATE TABLE без IF NOT EXISTS работает как compare-and-swap, поэтому
// побеждает ровно один, и в маркере остаётся один идентификатор.
func TestMultiInstance_ConcurrentClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const db = "nexus_race"
	ids := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}

	errs := make(chan error, len(ids))
	start := make(chan struct{})
	for _, id := range ids {
		go func(id string) {
			g := newGuard(conn, id)
			<-start
			errs <- g.Claim(ctx, db)
		}(id)
	}
	close(start)

	var won int
	for range ids {
		if err := <-errs; err == nil {
			won++
		}
	}
	assert.Equal(t, 1, won, "захват атомарен — выигрывает ровно одна нода")

	rows, err := conn.Query(ctx, fmt.Sprintf(
		"SELECT DISTINCT instance_id FROM %s.%s", db, chpf.MarkerTable))
	require.NoError(t, err)
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		owners = append(owners, id)
	}
	require.NoError(t, rows.Err())
	assert.Len(t, owners, 1, "в маркере ровно один идентификатор: %v", owners)
}

// TestMultiInstance_HousekeepingSkipsForeignTable — самый разрушительный
// сценарий §70: DROP PARTITION сносит партицию ЦЕЛИКОМ, поэтому уборка ноды kz
// не должна трогать таблицу соседней ноды, даже если та числится в её
// собственной PostgreSQL (совпадение имён или ручная правка).
func TestMultiInstance_HousekeepingSkipsForeignTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	legacy := newGuard(conn, instanceLegacy)
	kz := newGuard(conn, instanceKZ)
	require.NoError(t, legacy.Claim(ctx, "nexus_default"))
	require.NoError(t, kz.Claim(ctx, "nexus_kz_default"))

	const foreign = "nexus_default.orders"
	createLogTable(t, ctx, conn, foreign)
	insertOldRow(t, ctx, conn, foreign, "11111111-1111-1111-1111-111111111111")
	before := chRowCount(t, ctx, conn, foreign)
	require.Positive(t, before)

	hk := senderusecase.NewCHHousekeeping(fixedConn{conn}, staticNodeLister{[]*domain.Node{{
		ID: "node-kz", Path: "svc/orders", ClickHouseTable: foreign, ClickHouseRetentionDays: 1,
	}}}, logging.NewNoop()).WithOwnership(kz)
	require.NoError(t, hk.RunOnce(ctx))

	assert.Equal(t, before, chRowCount(t, ctx, conn, foreign),
		"уборка чужой ноды не имеет права удалять партиции")

	// Контроль: на своей таблице та же уборка отрабатывает как раньше.
	const own = "nexus_kz_default.orders"
	createLogTable(t, ctx, conn, own)
	insertOldRow(t, ctx, conn, own, "22222222-2222-2222-2222-222222222222")
	require.Positive(t, chRowCount(t, ctx, conn, own))

	hkOwn := senderusecase.NewCHHousekeeping(fixedConn{conn}, staticNodeLister{[]*domain.Node{{
		ID: "node-kz", Path: "svc/orders", ClickHouseTable: own, ClickHouseRetentionDays: 1,
	}}}, logging.NewNoop()).WithOwnership(kz)
	require.NoError(t, hkOwn.RunOnce(ctx))
	assert.Zero(t, chRowCount(t, ctx, conn, own), "своя старая партиция удаляется")
}

// TestMultiInstance_FilterManagedTables — стартовые ALTER'ы: чужие таблицы
// исключаются, свои и «ничейные» (нода до §70 ещё не проставила маркер)
// остаются — иначе обновление ноды молча выключило бы обслуживание схемы.
func TestMultiInstance_FilterManagedTables(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	legacy := newGuard(conn, instanceLegacy)
	kz := newGuard(conn, instanceKZ)
	require.NoError(t, legacy.Claim(ctx, "nexus_default"))
	require.NoError(t, kz.Claim(ctx, "nexus_kz_default"))
	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_kz_legacy"))

	kept := kz.FilterManagedTables(ctx, []string{
		"nexus_kz_default.logs", // своя
		"nexus_default.logs",    // чужая
		"nexus_kz_legacy.logs",  // без маркера
	})
	assert.Equal(t, []string{"nexus_kz_default.logs", "nexus_kz_legacy.logs"}, kept)
}

// TestMultiInstance_MarkerHiddenFromOrphanScan — маркер владения не должен
// попадать в список «бесхозных таблиц»: иначе админ удалит его кнопкой, и БД
// станет ничьей. Проверяется тем же запросом, что выполняет OrphanScanner.
func TestMultiInstance_MarkerHiddenFromOrphanScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	kz := newGuard(conn, instanceKZ)
	require.NoError(t, kz.Claim(ctx, "nexus_kz_default"))
	createLogTable(t, ctx, conn, "nexus_kz_default.orphan_42")

	rows, err := conn.Query(ctx, `
SELECT name
FROM system.tables
WHERE database = ?
  AND engine LIKE '%MergeTree%'
  AND name NOT LIKE '.inner%'
  AND name NOT LIKE '.tmp%'
  AND name NOT LIKE '\_\_nexus\_%'
ORDER BY name`, "nexus_kz_default")
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"orphan_42"}, names,
		"маркер владения не виден orphan-скану, обычная таблица — видна")
}
