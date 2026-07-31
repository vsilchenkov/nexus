//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
)

// §74.3: откат кода не должен упираться в стартовый гейт миграций.
//
// Исходное поведение: golang-migrate требует, чтобы версия из schema_migrations
// существовала в каталоге миграций бинаря, поэтому откаченный Web/Receiver падал
// с «no migration found for version N» и уходил в crash-loop. Схема при этом
// обратно совместима по контракту §74.2, то есть падение было чисто гейтовым.

// trimmedMigrations копирует каталог миграций, отбрасывая последние n версий —
// это каталог внутри ОБРАЗА ПРЕДЫДУЩЕЙ ВЕРСИИ, каким его видит откаченный сервис.
func trimmedMigrations(t *testing.T, srcDir string, cutoff uint) string {
	t.Helper()

	entries, err := os.ReadDir(srcDir)
	require.NoError(t, err)

	dst := t.TempDir()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, ok := strings.Cut(e.Name(), "_")
		require.True(t, ok, "неожиданное имя миграции: %s", e.Name())
		v, err := strconv.ParseUint(num, 10, 64)
		require.NoError(t, err)
		if uint(v) > cutoff {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600))
	}
	return dst
}

func TestMigrateSchemaAheadOfBinary(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := startPostgres(t, ctx) // накатывает полный каталог миграций
	defer cleanup()
	dsn := pool.Config().ConnString()

	fullDir, err := filepath.Abs("../../migrations")
	require.NoError(t, err)

	mgFull, err := pgpf.NewMigratorFromDSN(dsn, fullDir, logging.NewNoop())
	require.NoError(t, err)
	defer mgFull.Close()

	latest, err := mgFull.MaxLocalVersion()
	require.NoError(t, err)
	require.Greater(t, latest, uint(4), "тесту нужен каталог хотя бы из пяти миграций")

	// Штатное состояние: код и схема согласованы.
	st, err := mgFull.EnsureUp()
	require.NoError(t, err)
	assert.False(t, st.Ahead)
	assert.False(t, st.Dirty)
	assert.Equal(t, latest, st.DBVersion)
	assert.Equal(t, latest, st.MaxLocal)

	// Откат кода: у бинаря каталог на 4 миграции короче, схема осталась новой.
	const rolledBack = 4
	mgOld, err := pgpf.NewMigratorFromDSN(dsn, trimmedMigrations(t, fullDir, latest-rolledBack), logging.NewNoop())
	require.NoError(t, err)
	defer mgOld.Close()

	st, err = mgOld.EnsureUp()
	require.NoError(t, err, "схема новее бинаря не должна прекращать старт (§74.3)")
	assert.True(t, st.Ahead)
	assert.Equal(t, latest, st.DBVersion)
	assert.Equal(t, latest-rolledBack, st.MaxLocal)

	// Схему при этом не тронули: версия прежняя, dirty не появился.
	v, dirty, err := mgOld.Status()
	require.NoError(t, err)
	assert.Equal(t, latest, v)
	assert.False(t, dirty)

	// Прежнее поведение (голый Up) на этой же схеме по-прежнему падает — именно
	// от него и падал откаченный сервис.
	assert.Error(t, mgOld.Up(), "Up на схеме новее каталога обязан возвращать ошибку гейта")

	// Откат доведён до конца: схема опущена образом НОВОЙ версии (§74.7),
	// после чего старый бинарь стартует уже в штатном состоянии.
	require.NoError(t, mgFull.Down(rolledBack))
	st, err = mgOld.EnsureUp()
	require.NoError(t, err)
	assert.False(t, st.Ahead)
	assert.Equal(t, latest-rolledBack, st.DBVersion)
}

func TestMigrateDirtySchemaStopsStart(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	dsn := pool.Config().ConnString()

	dir, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	mg, err := pgpf.NewMigratorFromDSN(dsn, dir, logging.NewNoop())
	require.NoError(t, err)
	defer mg.Close()

	// Оборванная миграция: состояние схемы неизвестно.
	_, err = pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`)
	require.NoError(t, err)

	st, err := mg.EnsureUp()
	require.Error(t, err, "на dirty-схеме сервис стартовать не должен")
	assert.ErrorIs(t, err, pgpf.ErrDirtySchema)
	assert.True(t, st.Dirty)
	assert.False(t, st.Ahead)
}

// §74.4: выход из dirty. Force объявляет версию и снимает флаг, SQL не выполняя.
func TestMigrateForceClearsDirty(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	dsn := pool.Config().ConnString()

	dir, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	mg, err := pgpf.NewMigratorFromDSN(dsn, dir, logging.NewNoop())
	require.NoError(t, err)
	defer mg.Close()

	latest, err := mg.MaxLocalVersion()
	require.NoError(t, err)

	// Моделируем оборванную последнюю миграцию: её изменений в схеме нет
	// (откатили вниз), но в schema_migrations записана её версия и dirty —
	// ровно то состояние, в котором ни up, ни down больше не работают.
	require.NoError(t, mg.Down(1))
	_, err = pool.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = true`, latest)
	require.NoError(t, err)
	_, err = mg.EnsureUp()
	require.ErrorIs(t, err, pgpf.ErrDirtySchema)

	// Опечатка в номере не должна «объявить» несуществующее состояние схемы.
	require.ErrorIs(t, mg.Force(int(latest)+100), pgpf.ErrUnknownMigration)
	v, dirty, err := mg.Status()
	require.NoError(t, err)
	assert.Equal(t, latest, v, "отклонённый force не должен менять версию")
	assert.True(t, dirty, "отклонённый force не должен снимать dirty")

	// Оператор убедился, что изменений оборванной миграции в схеме нет, и
	// объявляет предыдущую версию. SQL при этом не выполняется.
	require.NoError(t, mg.Force(int(latest)-1))
	v, dirty, err = mg.Status()
	require.NoError(t, err)
	assert.Equal(t, latest-1, v)
	assert.False(t, dirty)

	// После этого штатный старт снова работает и доводит схему до конца.
	st, err := mg.EnsureUp()
	require.NoError(t, err)
	assert.False(t, st.Ahead)
	assert.Equal(t, latest, st.DBVersion)
}
