//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/bootstrap"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// §111: право переименовать базу сидированной команды (nexus_default →
// nexus_<id>_default) определяется состоянием ноды, а не тем, какой процесс
// вставил строку instance_identity. Все сценарии идут через настоящие стартовые
// шаги cmd/web: заявку идентификатора и гейт владения.

func instanceCfg(id string) *config.Config {
	cfg := &config.Config{}
	cfg.Instance.ID = id
	return cfg
}

// claimIdentity — заявка идентификатора сервисом service БЕЗ гейта владения.
// Так выглядит старт Receiver или Sender и старт Web, оборвавшийся до гейта
// (между ними в cmd/web — настройки, Redis и подключение к ClickHouse).
func claimIdentity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, service string) bootstrap.Identity {
	t.Helper()
	return bootstrap.MustInstanceIdentity(ctx, pool, instanceCfg(id), config.Flags{}, service, logging.NewNoop())
}

func teamDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var db string
	require.NoError(t, pool.QueryRow(ctx, `SELECT ch_database FROM teams WHERE slug = $1`, slug).Scan(&db))
	return db
}

func chClaimedFlag(t *testing.T, ctx context.Context, pool *pgxpool.Pool) bool {
	t.Helper()
	var claimed bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT ch_claimed FROM instance_identity WHERE id = 1`).Scan(&claimed))
	return claimed
}

// assertRebased — нода id поднята: база Default названа по идентификатору,
// захвачена в ClickHouse её маркером, захват отмечен в PostgreSQL.
func assertRebased(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ch chdriver.Conn, id string) {
	t.Helper()
	want := "nexus_" + id + "_default"
	assert.Equal(t, want, teamDatabase(t, ctx, pool, "default"))
	v, _, err := newGuard(ch, id).Check(ctx, want)
	require.NoError(t, err)
	assert.Equal(t, chpf.VerdictOwned, v, "база %s захвачена нодой %s", want, id)
	assert.True(t, chClaimedFlag(t, ctx, pool), "захват отмечен в instance_identity")
}

// TestMultiInstance_RebaseFirstStart — пустая нода с instance.id поднимается
// при любом порядке сервисов и любом числе попыток первого старта. До §111
// каждый из сценариев заканчивался отказом «team "default" is already bound to
// "nexus_default" on a used database», а лечился пересозданием базы PostgreSQL.
func TestMultiInstance_RebaseFirstStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ch, _, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()

	t.Run("S1: первый старт Web оборвался до гейта", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		claimIdentity(t, ctx, pool, "r1", "web")
		assert.Equal(t, "nexus_default", teamDatabase(t, ctx, pool, "default"), "обрыв — база ещё сидированная")

		startWeb(t, ctx, pool, ch, "r1")
		assertRebased(t, ctx, pool, ch, "r1")
	})

	t.Run("S2: идентификатор первым заявил другой сервис", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		claimIdentity(t, ctx, pool, "r2", "receiver")
		claimIdentity(t, ctx, pool, "r2", "sender")

		startWeb(t, ctx, pool, ch, "r2")
		assertRebased(t, ctx, pool, ch, "r2")
	})

	t.Run("S3: первый старт Web без ClickHouse", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		identity := claimIdentity(t, ctx, pool, "r3", "web")
		require.NoError(t, bootstrap.CheckCHOwnership(ctx, pool, nil, instanceCfg("r3"), identity, logging.NewNoop()))
		assert.Equal(t, "nexus_r3_default", teamDatabase(t, ctx, pool, "default"),
			"переименование — операция только в PostgreSQL, ClickHouse для неё не нужен")
		assert.False(t, chClaimedFlag(t, ctx, pool), "без ClickHouse захвата не было")

		startWeb(t, ctx, pool, ch, "r3")
		assertRebased(t, ctx, pool, ch, "r3")
	})

	t.Run("S4: две реплики Web стартуют одновременно", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		// Обе реплики прошли заявку до того, как любая дошла до гейта. До §111
		// строку instance_identity вставляла первая, и вторая, как «не
		// заявитель», получала отказ сверки имён.
		replicas := []bootstrap.Identity{
			claimIdentity(t, ctx, pool, "r4", "web"),
			claimIdentity(t, ctx, pool, "r4", "web"),
		}
		errs := make(chan error, len(replicas))
		start := make(chan struct{})
		for _, identity := range replicas {
			go func() {
				<-start
				errs <- bootstrap.CheckCHOwnership(ctx, pool, ch, instanceCfg("r4"), identity, logging.NewNoop())
			}()
		}
		close(start)
		for range replicas {
			require.NoError(t, <-errs, "гейт пропускает обе реплики")
		}
		assertRebased(t, ctx, pool, ch, "r4")
	})

	t.Run("повторный старт ничего не переименовывает", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		startWeb(t, ctx, pool, ch, "r5")
		startWeb(t, ctx, pool, ch, "r5")
		assertRebased(t, ctx, pool, ch, "r5")
	})
}

// createEmptyMarker — база с таблицей-маркером БЕЗ строки: так выглядит захват
// базы соседней репликой между её CREATE TABLE и INSERT.
func createEmptyMarker(t *testing.T, ctx context.Context, conn chdriver.Conn, db string) {
	t.Helper()
	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+db))
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
CREATE TABLE %s.%s (
	instance_id String, claimed_at DateTime, claimed_by String, schema_ver UInt8
) ENGINE = TinyLog`, db, chpf.MarkerTable)))
}

// writeMarkerRowAfter дописывает строку маркера через delay — «соседняя реплика
// дошла до INSERT». Ошибка вставки приходит в канал.
func writeMarkerRowAfter(ctx context.Context, conn chdriver.Conn, db, id string, delay time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() {
		time.Sleep(delay)
		done <- conn.Exec(ctx, fmt.Sprintf(
			"INSERT INTO %s.%s (instance_id, claimed_at, claimed_by, schema_ver) VALUES (?, now(), ?, 1)",
			db, chpf.MarkerTable), id, "sibling@test")
	}()
	return done
}

func markerOwners(t *testing.T, ctx context.Context, conn chdriver.Conn, db string) []string {
	t.Helper()
	rows, err := conn.Query(ctx, fmt.Sprintf("SELECT instance_id FROM %s.%s", db, chpf.MarkerTable))
	require.NoError(t, err)
	defer rows.Close()
	owners := []string{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		owners = append(owners, id)
	}
	require.NoError(t, rows.Err())
	return owners
}

// TestMultiInstance_ReplicaFirstStartRace — §111.4, вторая половина S4: реплика
// ноды с идентификатором, заставшая захват базы соседней репликой на полпути
// (маркер создан, строки ещё нет), дожидается его, а не получает отказ гейта
// первого запуска на собственной базе. До §111 — `database already exists on
// first run of this instance … (verdict=unknown)` примерно в каждом пятом старте.
func TestMultiInstance_ReplicaFirstStartRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ch, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()
	firstRun := chpf.EnsureOptions{NeverClaimed: true, FreshPG: true}

	t.Run("маркер соседней реплики ещё пуст — вторая дожидается строки", func(t *testing.T) {
		const db = "nexus_m1_default"
		createEmptyMarker(t, ctx, ch, db)
		sibling := writeMarkerRowAfter(ctx, ch, db, "m1", 300*time.Millisecond)

		require.NoError(t, newGuard(ch, "m1").EnsureAll(ctx, []string{db}, firstRun))
		require.NoError(t, <-sibling)
		assert.Equal(t, []string{"m1"}, markerOwners(t, ctx, ch, db), "ждавшая реплика строку не дублирует")
	})

	t.Run("сосед умер до записи строки — маркер дописывается", func(t *testing.T) {
		const db = "nexus_m2_default"
		createEmptyMarker(t, ctx, ch, db)

		require.NoError(t, newGuard(ch, "m2").EnsureAll(ctx, []string{db}, firstRun))
		assert.Equal(t, []string{"m2"}, markerOwners(t, ctx, ch, db))
	})

	t.Run("в пустой маркер вписалась чужая нода — отказ", func(t *testing.T) {
		const db = "nexus_m3_default"
		createEmptyMarker(t, ctx, ch, db)
		sibling := writeMarkerRowAfter(ctx, ch, db, "other", 300*time.Millisecond)

		err := newGuard(ch, "m3").EnsureAll(ctx, []string{db}, firstRun)
		require.NoError(t, <-sibling)
		require.ErrorIs(t, err, chpf.ErrForeignDatabase)
		assert.Equal(t, []string{"other"}, markerOwners(t, ctx, ch, db), "чужой маркер не тронут")
	})

	t.Run("пустой маркер, но в базе есть таблица — отказ", func(t *testing.T) {
		const db = "nexus_m4_default"
		createEmptyMarker(t, ctx, ch, db)
		createLogTable(t, ctx, ch, db+".logs")

		err := newGuard(ch, "m4").EnsureAll(ctx, []string{db}, firstRun)
		require.ErrorIs(t, err, chpf.ErrFirstRunDatabaseExists)
		assert.Empty(t, markerOwners(t, ctx, ch, db), "база с данными не захвачена")
	})

	t.Run("нода без идентификатора — правило прежнее", func(t *testing.T) {
		// Маркер соседа у такой ноды неотличим от своего (instance_id=” у обеих),
		// поэтому она пустой маркер не дожидается и не дописывает.
		const db = "nexus_m5"
		createEmptyMarker(t, ctx, ch, db)

		err := newGuard(ch, instanceLegacy).EnsureAll(ctx, []string{db}, firstRun)
		require.ErrorIs(t, err, chpf.ErrFirstRunDatabaseExists)
		assert.Empty(t, markerOwners(t, ctx, ch, db))
	})

	t.Run("две реплики с разносом старта", func(t *testing.T) {
		// Окно между CREATE DATABASE и INSERT маркера — десятки миллисекунд; до
		// §111 разнос 6–20 мс давал отказ второй реплике в трети раундов.
		for _, delay := range []time.Duration{0, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond} {
			for round := range 6 {
				db := fmt.Sprintf("nexus_m6_d%d_r%d", delay.Milliseconds(), round)
				errs := make(chan error, 2)
				for replica := range 2 {
					go func() {
						time.Sleep(time.Duration(replica) * delay)
						errs <- newGuard(ch, "m6").EnsureAll(ctx, []string{db}, firstRun)
					}()
				}
				for range 2 {
					require.NoError(t, <-errs, "разнос %s, раунд %d", delay, round)
				}
				v, _, err := newGuard(ch, "m6").Check(ctx, db)
				require.NoError(t, err)
				assert.Equal(t, chpf.VerdictOwned, v)
			}
		}
	})
}

// TestMultiInstance_RebaseRefusals — защита действующих нод §70.5 сохранена:
// §111 снимает отказ только с ноды, которую действительно только что подняли.
func TestMultiInstance_RebaseRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ch, _, chCleanup := startClickHouse(t, ctx)
	defer chCleanup()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)

	t.Run("на ноде уже есть узел", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		node := nonDefaultRequestNode(resolveDefaultTeamID(t, ctx, pool))
		require.NoError(t, pgrepo.NewNodeRepoPg(pool, cipher, logging.NewNoop()).Create(ctx, node))
		identity := claimIdentity(t, ctx, pool, "u1", "web")
		require.False(t, identity.FreshPG)

		err := bootstrap.CheckCHOwnership(ctx, pool, ch, instanceCfg("u1"), identity, logging.NewNoop())
		require.ErrorContains(t, err, "is already bound to")
		assert.Equal(t, "nexus_default", teamDatabase(t, ctx, pool, "default"), "база не тронута")

		// Без ClickHouse та же нода стартует, как и до §111: отказ наступит на
		// первом старте с доступным ClickHouse.
		require.NoError(t, bootstrap.CheckCHOwnership(ctx, pool, nil, instanceCfg("u1"), identity, logging.NewNoop()))
		assert.Equal(t, "nexus_default", teamDatabase(t, ctx, pool, "default"), "база не тронута")
	})

	t.Run("нода уже захватила базу под старым именем", func(t *testing.T) {
		pool, cleanup := startPostgres(t, ctx)
		defer cleanup()
		// Нода работала без идентификатора и захватила nexus_default, затем
		// оператор переписал идентификатор флагом --instance-id-force.
		startWeb(t, ctx, pool, ch, "")
		require.True(t, chClaimedFlag(t, ctx, pool))
		identity := bootstrap.MustInstanceIdentity(ctx, pool, instanceCfg("u2"),
			config.Flags{InstanceIDForce: true}, "web", logging.NewNoop())
		require.True(t, identity.FreshPG)
		require.True(t, identity.CHClaimed)

		err := bootstrap.CheckCHOwnership(ctx, pool, ch, instanceCfg("u2"), identity, logging.NewNoop())
		require.ErrorContains(t, err, "is already bound to")
		assert.Equal(t, "nexus_default", teamDatabase(t, ctx, pool, "default"), "база не тронута")
	})
}
