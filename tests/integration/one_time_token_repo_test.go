//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// §88.4.1: одноразовые ссылки на живой PostgreSQL (на бою PG 12 — testcontainers
// поднимает её же). Проверяется то, чего не видит ни один unit-тест:
// атомарность гашения, поведение частичных индексов и CHECK на назначении.

// seedUser заводит пользователя и возвращает его id.
//
// default_team_id объявлен NOT NULL, поэтому команда берётся из сидинга
// миграции 0008 — тем же хелпером, что и в остальных сценариях пакета.
// Пустой email пишем NULL: частичный индекс users_email_lower_idx (0036)
// покрывает только непустые адреса.
func seedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, login, email string) string {
	t.Helper()
	teamID := resolveDefaultTeamID(t, ctx, pool)

	var emailArg any
	if email != "" {
		emailArg = email
	}
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (login, name, email, role, active, default_team_id)
		 VALUES ($1, $1, $2, 'viewer', true, $3::uuid) RETURNING id::text`,
		login, emailArg, teamID).Scan(&id)
	require.NoError(t, err)
	return id
}

func newToken(userID, hash string, expiresAt time.Time) *domain.OneTimeToken {
	return &domain.OneTimeToken{
		UserID:    userID,
		Purpose:   domain.TokenPurposePasswordReset,
		TokenHash: hash,
		ExpiresAt: expiresAt,
		RequestIP: "10.1.2.3",
	}
}

func TestOneTimeTokenRepo_ConsumeIsSingleUse_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "ivanov@example.com")

	require.NoError(t, repo.Create(ctx, newToken(userID, "hash-1", now.Add(time.Hour))))

	got, err := repo.Consume(ctx, domain.TokenPurposePasswordReset, "hash-1", now)
	require.NoError(t, err)
	assert.Equal(t, userID, got.UserID)
	assert.Equal(t, domain.TokenPurposePasswordReset, got.Purpose)
	assert.Equal(t, "10.1.2.3", got.RequestIP)
	require.NotNil(t, got.UsedAt, "погашенная ссылка обязана нести момент использования")

	// Повторный переход по той же ссылке.
	_, err = repo.Consume(ctx, domain.TokenPurposePasswordReset, "hash-1", now)
	require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid)
}

func TestOneTimeTokenRepo_ExpiredAndUnknown_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")

	require.NoError(t, repo.Create(ctx, newToken(userID, "expired", now.Add(-time.Minute))))

	_, err := repo.Consume(ctx, domain.TokenPurposePasswordReset, "expired", now)
	require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid, "истёкшая ссылка не гасится")

	_, err = repo.Consume(ctx, domain.TokenPurposePasswordReset, "never-issued", now)
	require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid)

	ok, err := repo.Peek(ctx, domain.TokenPurposePasswordReset, "expired", now)
	require.NoError(t, err)
	assert.False(t, ok)
}

// §88.4.6: проверка ссылки НЕ расходует её — почтовые шлюзы с защитой от
// вредоносных ссылок сами открывают адреса из писем.
func TestOneTimeTokenRepo_PeekDoesNotConsume_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")

	require.NoError(t, repo.Create(ctx, newToken(userID, "hash-peek", now.Add(time.Hour))))

	for range 3 {
		ok, err := repo.Peek(ctx, domain.TokenPurposePasswordReset, "hash-peek", now)
		require.NoError(t, err)
		assert.True(t, ok, "многократная проверка не должна гасить ссылку")
	}

	_, err := repo.Consume(ctx, domain.TokenPurposePasswordReset, "hash-peek", now)
	require.NoError(t, err, "после проверок ссылка обязана остаться рабочей")
}

// Ключевой тест обобщения таблицы (§88.4.1): токен чужого назначения не
// находится. Без фильтра по purpose ссылку на слабое действие можно было бы
// предъявить на сильном эндпоинте.
func TestOneTimeTokenRepo_PurposeIsolation_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")

	require.NoError(t, repo.Create(ctx, newToken(userID, "hash-x", now.Add(time.Hour))))

	const other = domain.TokenPurpose("email_change")

	_, err := repo.Consume(ctx, other, "hash-x", now)
	require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid,
		"токен чужого назначения не должен гаситься")

	ok, err := repo.Peek(ctx, other, "hash-x", now)
	require.NoError(t, err)
	assert.False(t, ok, "токен чужого назначения не должен находиться проверкой")

	n, err := repo.CountActive(ctx, other, userID, now)
	require.NoError(t, err)
	assert.Zero(t, n, "счётчик активных считает только своё назначение")

	// Своим назначением ссылка по-прежнему рабочая.
	_, err = repo.Consume(ctx, domain.TokenPurposePasswordReset, "hash-x", now)
	require.NoError(t, err)
}

// CHECK на purpose обязан отвергать незнакомое назначение: опечатка иначе
// выдавала бы токены, которые молча никогда не найдутся.
func TestOneTimeTokenRepo_UnknownPurposeRejected_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")

	bad := newToken(userID, "hash-bad", now.Add(time.Hour))
	bad.Purpose = "passwrod_reset" // опечатка
	require.Error(t, repo.Create(ctx, bad))
}

func TestOneTimeTokenRepo_CountActiveAndInvalidate_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")
	otherID := seedUser(t, ctx, pool, "petrov", "")

	require.NoError(t, repo.Create(ctx, newToken(userID, "a", now.Add(time.Hour))))
	require.NoError(t, repo.Create(ctx, newToken(userID, "b", now.Add(time.Hour))))
	require.NoError(t, repo.Create(ctx, newToken(userID, "expired", now.Add(-time.Minute))))
	require.NoError(t, repo.Create(ctx, newToken(otherID, "c", now.Add(time.Hour))))

	n, err := repo.CountActive(ctx, domain.TokenPurposePasswordReset, userID, now)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "истёкшая не считается активной")

	// §88.7: смена пароля гасит все выданные ссылки пользователя.
	killed, err := repo.InvalidateByUser(ctx, domain.TokenPurposePasswordReset, userID, now)
	require.NoError(t, err)
	assert.Equal(t, 2, killed)

	n, err = repo.CountActive(ctx, domain.TokenPurposePasswordReset, userID, now)
	require.NoError(t, err)
	assert.Zero(t, n)

	_, err = repo.Consume(ctx, domain.TokenPurposePasswordReset, "a", now)
	require.ErrorIs(t, err, domain.ErrOneTimeTokenInvalid)

	// Чужие ссылки не задеты.
	n, err = repo.CountActive(ctx, domain.TokenPurposePasswordReset, otherID, now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "гашение обязано ограничиваться своим пользователем")
}

func TestOneTimeTokenRepo_DeleteExpiredBefore_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")

	require.NoError(t, repo.Create(ctx, newToken(userID, "old", now.Add(-8*24*time.Hour))))
	require.NoError(t, repo.Create(ctx, newToken(userID, "recent", now.Add(-time.Hour))))
	require.NoError(t, repo.Create(ctx, newToken(userID, "live", now.Add(time.Hour))))

	// Хвост 7 дней: свежепротухшие остаются для расследований.
	deleted, err := repo.DeleteExpiredBefore(ctx, now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	ok, err := repo.Peek(ctx, domain.TokenPurposePasswordReset, "live", now)
	require.NoError(t, err)
	assert.True(t, ok)
}

// Гонка двух одновременных подтверждений разрешается на уровне СУБД: ровно
// один вызов обязан выиграть. Без атомарного CAS (например, SELECT + UPDATE)
// оба увидели бы used_at IS NULL и сменили бы пароль дважды.
func TestOneTimeTokenRepo_ConcurrentConsume_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewOneTimeTokenRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC()
	userID := seedUser(t, ctx, pool, "ivanov", "")
	require.NoError(t, repo.Create(ctx, newToken(userID, "race", now.Add(time.Hour))))

	const racers = 8
	var (
		mu      sync.Mutex
		wins    int
		start   = make(chan struct{})
		wg      sync.WaitGroup
		lastErr error
	)
	for range racers {
		wg.Go(func() {
			<-start
			_, err := repo.Consume(ctx, domain.TokenPurposePasswordReset, "race", now)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
				return
			}
			if !isTokenInvalid(err) {
				lastErr = err
			}
		})
	}
	close(start)
	wg.Wait()

	require.NoError(t, lastErr, "проигравшие обязаны получать ErrOneTimeTokenInvalid, а не сбой БД")
	assert.Equal(t, 1, wins, "ровно один вызов гасит ссылку")
}

func isTokenInvalid(err error) bool {
	return err != nil && err.Error() == domain.ErrOneTimeTokenInvalid.Error()
}

// §74: откат — эксплуатационная процедура, а не теоретическая возможность.
// Проверяем, что 0036 действительно откатывается и накатывается обратно:
// ошибка в down-файле иначе всплыла бы в аварии, когда её меньше всего ждут.
func TestOneTimeTokens_MigrationDownUp_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()
	// DSN берём из самого пула — отдельный хелпер ради этого не нужен.
	dsn := pool.Config().ConnString()

	tableExists := func() bool {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                 WHERE table_name = 'one_time_tokens')`).Scan(&ok))
		return ok
	}
	indexExists := func() bool {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			                 WHERE indexname = 'users_email_lower_idx')`).Scan(&ok))
		return ok
	}

	require.True(t, tableExists(), "после Up таблица должна быть")
	require.True(t, indexExists(), "после Up индекс по email должен быть")

	mp, err := filepath.Abs("../../migrations")
	require.NoError(t, err)

	// Глубина отката СЧИТАЕТСЯ, а не зашита единицей: `Down(1)` откатывал бы
	// последнюю миграцию, и как только поверх 0036 легла следующая (0037, §89),
	// тест начал проверять чужой откат — таблица оставалась на месте, а падение
	// выглядело как «down сломан». Теперь откатываем ровно до 0036 включительно,
	// сколько бы миграций ни добавили сверху.
	depth := migrationsAtOrAbove(t, mp, 36)
	mg, err := pgpf.NewMigratorFromDSN(dsn, mp, logging.NewNoop())
	require.NoError(t, err)
	require.NoError(t, mg.Down(depth), "0036 обязана откатываться")
	mg.Close()

	assert.False(t, tableExists(), "down обязан убрать таблицу")
	assert.False(t, indexExists(), "down обязан убрать индекс по email")

	mg2, err := pgpf.NewMigratorFromDSN(dsn, mp, logging.NewNoop())
	require.NoError(t, err)
	require.NoError(t, mg2.Up(), "после отката миграция обязана накатываться заново")
	mg2.Close()

	assert.True(t, tableExists())
	assert.True(t, indexExists())
}

func TestUserRepo_GetByEmail_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewUserRepoPg(pool, logging.NewNoop())
	seedUser(t, ctx, pool, "ivanov", "Ivanov@Example.COM")

	t.Run("без учёта регистра", func(t *testing.T) {
		u, err := repo.GetByEmail(ctx, "ivanov@example.com")
		require.NoError(t, err)
		assert.Equal(t, "ivanov", u.Login)
	})

	t.Run("нет такого адреса", func(t *testing.T) {
		_, err := repo.GetByEmail(ctx, "nobody@example.com")
		require.ErrorIs(t, err, domain.ErrUserNotFound)
	})

	// Колонка не уникальна: общий адрес не должен выдавать ссылку на чужую
	// учётную запись — трактуем как «не найден» (§88.4.2).
	t.Run("адрес делят двое", func(t *testing.T) {
		seedUser(t, ctx, pool, "petrov", "shared@example.com")
		seedUser(t, ctx, pool, "sidorov", "shared@example.com")

		_, err := repo.GetByEmail(ctx, "shared@example.com")
		require.ErrorIs(t, err, domain.ErrUserEmailAmbiguous)
	})
}

// migrationsAtOrAbove — сколько миграций в каталоге имеют номер >= from.
//
// Нужна тестам отката: `Down(N)` считает шаги от ХВОСТА, поэтому «откатить
// миграцию 00NN» означает «откатить всё, что легло поверх неё, плюс её саму».
// Зашитая единица превращает такой тест в проверку последней миграции, какой бы
// она ни оказалась, — и падает он не там, где сломано (§89.9).
func migrationsAtOrAbove(t *testing.T, dir string, from int) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	n := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		require.NoErrorf(t, err, "имя миграции без номера: %s", name)
		if v >= from {
			n++
		}
	}
	require.Positivef(t, n, "не найдено миграций с номером >= %04d", from)
	return n
}
