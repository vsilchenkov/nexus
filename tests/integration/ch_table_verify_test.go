//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase"
)

// TestCHTableVerify_E2E (§64): проверка структуры внешней таблицы на реальном
// ClickHouse. Ключевой сценарий фичи — оператор указывает таблицу, которую
// наполняет посторонний сервис, и должен до сохранения узла узнать, пригодна ли
// она для чтения логов Nexus'ом.
func TestCHTableVerify_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_default"))

	logger := logging.NewNoop()
	insp := webch.NewSchemaInspector(clickhouse.StaticProvider(conn), logger)
	uc := usecase.NewCHTableVerifyUsecase(insp, logger)

	t.Run("таблица по эталонной схеме — пригодна", func(t *testing.T) {
		const table = "nexus_default.verify_ok"
		createNodeLogTable(t, ctx, conn, table)

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.True(t, res.OK, "missing=%v mismatched=%+v", res.Missing, res.Mismatched)
		assert.False(t, res.TableMissing)
	})

	t.Run("лишние колонки постороннего писателя не мешают", func(t *testing.T) {
		const table = "nexus_default.verify_extra"
		createNodeLogTable(t, ctx, conn, table)
		require.NoError(t, conn.Exec(ctx,
			"ALTER TABLE "+table+" ADD COLUMN tenant_code String DEFAULT ''"))

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.True(t, res.OK, "лишняя колонка не должна делать таблицу непригодной")
	})

	t.Run("нет обязательной колонки", func(t *testing.T) {
		const table = "nexus_default.verify_missing_col"
		createNodeLogTable(t, ctx, conn, table)
		require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+table+" DROP COLUMN node_id"))

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, []string{"node_id"}, res.Missing)
	})

	t.Run("§67: без client_host — Missing; EnsureClientHostColumn чинит AFTER IP", func(t *testing.T) {
		// Модель внешней таблицы (§64), созданной до §67: колонки client_host нет.
		const table = "nexus_default.verify_no_client_host"
		createNodeLogTable(t, ctx, conn, table)
		require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+table+" DROP COLUMN client_host"))

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, []string{"client_host"}, res.Missing)

		// Тот же ALTER, что выполняет владелец внешней таблицы вручную (release
		// notes) и стартовый ensure для управляемых таблиц: колонка встаёт AFTER IP.
		clickhouse.EnsureClientHostColumn(ctx, conn, []string{table}, logger)
		res, err = uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.True(t, res.OK, "missing=%v mismatched=%+v", res.Missing, res.Mismatched)

		var pos, ipPos uint64
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT position FROM system.columns
			 WHERE database = 'nexus_default' AND table = 'verify_no_client_host' AND name = 'client_host'`).Scan(&pos))
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT position FROM system.columns
			 WHERE database = 'nexus_default' AND table = 'verify_no_client_host' AND name = 'IP'`).Scan(&ipPos))
		assert.Equal(t, ipPos+1, pos, "client_host должен стоять сразу после IP")
	})

	t.Run("неверный тип колонки", func(t *testing.T) {
		const table = "nexus_default.verify_bad_type"
		createNodeLogTable(t, ctx, conn, table)
		require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+table+" MODIFY COLUMN status String"))

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.False(t, res.OK)
		require.Len(t, res.Mismatched, 1)
		assert.Equal(t, domain.LogColumnMismatch{Name: "status", Want: "Int32", Got: "String"}, res.Mismatched[0])
	})

	t.Run("done Bool вместо UInt8 — таблица пригодна", func(t *testing.T) {
		// Bool — алиас UInt8 в ClickHouse, а LogRecord.Done в Go объявлен как
		// bool: драйвер одинаково принимает обе формы и на Append, и на Scan.
		// Такие таблицы есть на стендах (схему объявляли до §19) — помечать их
		// непригодными нельзя.
		const table = "nexus_default.verify_bool_done"
		createNodeLogTable(t, ctx, conn, table)
		require.NoError(t, conn.Exec(ctx, "ALTER TABLE "+table+" MODIFY COLUMN done Bool"))

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.True(t, res.OK, "mismatched=%+v", res.Mismatched)
	})

	t.Run("таблицы нет", func(t *testing.T) {
		res, err := uc.Verify(ctx, "nexus_default.verify_absent")
		require.NoError(t, err, "отсутствие таблицы — результат проверки, а не ошибка")
		assert.False(t, res.OK)
		assert.True(t, res.TableMissing)
	})

	t.Run("таблица в чужой БД (не БД команды) проверяется так же", func(t *testing.T) {
		require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS external_db"))
		const table = "external_db.audit_log"
		createNodeLogTable(t, ctx, conn, table)

		res, err := uc.Verify(ctx, table)
		require.NoError(t, err)
		assert.True(t, res.OK, "внешняя таблица не обязана лежать в БД команды")
	})
}
