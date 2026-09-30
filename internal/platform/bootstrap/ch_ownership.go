package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	chdrv "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	chpf "nexus/internal/platform/clickhouse"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// MustCHOwnership — стартовый гейт владения ClickHouse-БД (§70.5).
//
// Выполняется в main, а НЕ внутри App.Start, именно ради фатальности: ошибку из
// App.Start сервис-обёртка гасит логом и в неинтерактивном режиме (systemd/SCM)
// процесс остаётся жить — то есть «нода не должна стартовать» превратилось бы в
// «нода запущена и молчит». Здесь отказ означает os.Exit(1).
//
// Что делает:
//  1. при непустом instance.id один раз переименовывает БД сидированной команды
//     (`nexus_default` → `nexus_<id>_default`) — миграция 0008 зашивает имя
//     жёстко, а нода с идентификатором обязана писать в свою БД;
//  2. захватывает/подтверждает владение всеми БД этой ноды.
//
// chConn == nil (Web стартует без ClickHouse) — шаг 2 пропускается: без
// соединения ни писать, ни удалять нода всё равно не может. Шаг 1 выполняется
// и тогда — это операция только в PostgreSQL (§111.3), — но его отказ старт
// без ClickHouse не прекращает.
func MustCHOwnership(
	ctx context.Context,
	pool *pgxpool.Pool,
	chConn chdrv.Conn,
	cfg *config.Config,
	identity Identity,
	logger logging.Logger,
) {
	if err := CheckCHOwnership(ctx, pool, chConn, cfg, identity, logger); err != nil {
		os.Exit(1) // причина уже в логе уровня error
	}
}

// CheckCHOwnership — тело [MustCHOwnership] без выхода из процесса: причина
// отказа логируется здесь же (уровень error, с операцией), ошибка
// возвращается только как признак. Отдельно — чтобы integration-тест мог
// пройти настоящий стартовый гейт, например после восстановления
// конфигурации (§110): восстановленная нода обязана подниматься.
func CheckCHOwnership(
	ctx context.Context,
	pool *pgxpool.Pool,
	chConn chdrv.Conn,
	cfg *config.Config,
	identity Identity,
	logger logging.Logger,
) error {
	// §111: сверка имён — ДО проверки соединения. Иначе первый старт без
	// ClickHouse оставлял сидированное имя, и узлы создавались бы уже под ним.
	rebaseErr := rebaseSeededTeamDB(ctx, pool, identity, logger)

	if chConn == nil {
		if rebaseErr != nil {
			// До §111 сверка в этом состоянии не выполнялась вовсе, и Web
			// поднимался. Новых причин не стартовать без ClickHouse не добавляем:
			// тот же отказ наступит на первом старте с доступным ClickHouse.
			logger.Warn("team database check failed; start continues because clickhouse is not available",
				logger.Err(rebaseErr),
				logger.Str("instance", identity.ID.String()))
		}
		logger.Warn("clickhouse is not available; ownership gate skipped",
			logger.Str("instance", identity.ID.String()))
		return nil
	}

	if rebaseErr != nil {
		logger.ErrorWithOp("rebase seeded team database failed", rebaseErr, "bootstrap.MustCHOwnership")
		return rebaseErr
	}

	dbs, err := teamDatabases(ctx, pool)
	if err != nil {
		logger.ErrorWithOp("list team databases failed", err, "bootstrap.MustCHOwnership")
		return err
	}

	guard := chpf.NewGuard(staticConnProvider{chConn}, identity.ID, logger)
	if err := guard.EnsureAll(ctx, dbs, chpf.EnsureOptions{
		NeverClaimed: !identity.CHClaimed,
		FreshPG:      identity.FreshPG,
		Adopt:        cfg.Instance.AdoptUnowned,
	}); err != nil {
		logger.ErrorWithOp("clickhouse ownership check failed: refusing to start", err,
			"bootstrap.MustCHOwnership",
			logger.Str("instance", identity.ID.String()))
		return err
	}
	// Отмечаем захват: со следующего старта гейт первого запуска не действует —
	// иначе перезапуск ноды до создания первого узла упирался бы в её же БД.
	MarkCHClaimed(ctx, pool, logger)
	logger.Info("clickhouse ownership confirmed",
		logger.Str("instance", identity.ID.String()),
		logger.Int("databases", len(dbs)))
	return nil
}

// staticConnProvider — ConnProvider поверх исходного соединения. Manager на этом
// этапе ещё не создан (он живёт в App), а гейт разовый: hot-reload ему не нужен.
type staticConnProvider struct{ conn chdrv.Conn }

func (s staticConnProvider) Conn() chdrv.Conn { return s.conn }

// Причины отказа сверки имён баз команд (§111.3). Текст — оператору: сообщение
// «team … is already bound to …» он дочитывает до причины и действия.
var (
	errTeamDBUnexpected = errors.New("bootstrap: rename the database and update teams.ch_database manually")
	errSeededDBInUse    = errors.New("bootstrap: this node already has nodes or teams; " +
		"migrate the data manually before switching instance.id (DEPLOYMENT.md, changing instance.id on a running node)")
	errSeededDBClaimed = errors.New("bootstrap: this node has already claimed its clickhouse databases under that name; " +
		"rename them manually before switching instance.id (DEPLOYMENT.md, changing instance.id on a running node)")
)

// teamBinding — команда и имя её ClickHouse-БД, как они записаны в PostgreSQL.
type teamBinding struct{ slug, db string }

// rebaseSeededTeamDB приводит имена БД ВСЕХ команд ноды к её идентификатору.
// На практике это ровно одна сидированная миграцией 0008 команда `default`
// (`nexus_default` → `nexus_<id>_default`): команды, созданные уже на ноде с
// идентификатором, имеют правильное имя и пропускаются. Проход по всем нужен,
// чтобы рассинхрон (ручная правка teams.ch_database) не остался незамеченным.
//
// Решение по каждой команде — [decideRebase]. Если идентификатор задан, а
// условия переименования не сошлись — это ошибка: молчаливое продолжение
// оставило бы новую ноду писать в БД соседа.
func rebaseSeededTeamDB(ctx context.Context, pool *pgxpool.Pool, identity Identity, logger logging.Logger) error {
	if identity.ID.IsZero() {
		return nil
	}
	teams, err := loadTeamBindings(ctx, pool)
	if err != nil {
		return err
	}
	for _, t := range teams {
		want, rename, err := decideRebase(identity, t.slug, t.db)
		if err != nil {
			return err
		}
		if !rename {
			logger.Debug("team database matches instance",
				logger.Str("team", t.slug),
				logger.Str("database", t.db))
			continue
		}
		if err := renameSeededTeamDB(ctx, pool, t, want, logger); err != nil {
			return err
		}
	}
	return nil
}

// decideRebase решает судьбу базы одной команды на ноде с непустым
// идентификатором (§111.3): want — имя, которое база обязана носить, rename —
// нужно ли переписать на него сидированное имя.
//
// Переименование разрешено, только пока нода только что поднята: PostgreSQL
// свежая И базы ClickHouse ещё не захватывались. Оба признака перепроверяются
// на каждом старте — в отличие от факта «строку instance_identity вставил этот
// процесс», который до §111 доставался тому, кто успел первым (оборванный
// старт Web, Receiver, Sender, соседняя реплика), и оставлял пустую ноду
// неподнимающейся.
func decideRebase(identity Identity, slug, current string) (want string, rename bool, err error) {
	want = identity.ID.CHDatabase(slug)
	switch {
	case current == want:
		return want, false, nil
	case current != domain.CHDatabaseForSlug(slug):
		return want, false, fmt.Errorf("team %q points at clickhouse database %q, expected %q for instance %q: %w",
			slug, current, want, identity.ID, errTeamDBUnexpected)
	case !identity.FreshPG:
		return want, false, fmt.Errorf("instance %q is configured, but team %q is already bound to %q: %w",
			identity.ID, slug, current, errSeededDBInUse)
	case identity.CHClaimed:
		return want, false, fmt.Errorf("instance %q is configured, but team %q is already bound to %q: %w",
			identity.ID, slug, current, errSeededDBClaimed)
	}
	return want, true, nil
}

func loadTeamBindings(ctx context.Context, pool *pgxpool.Pool) ([]teamBinding, error) {
	rows, err := pool.Query(ctx, `SELECT slug, ch_database FROM teams ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("read teams: %w", err)
	}
	defer rows.Close()

	var teams []teamBinding
	for rows.Next() {
		var t teamBinding
		if err := rows.Scan(&t.slug, &t.db); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read teams: %w", err)
	}
	return teams, nil
}

// renameSeededTeamDB переписывает сидированное имя базы команды на want.
//
// Запись условная — compare-and-set по прежнему имени: реплики Web, стартующие
// одновременно (§93), не мешают друг другу. Проигравшая обновляет ноль строк и
// сверяет, что в строке теперь ровно want; любое другое имя — рассинхрон.
func renameSeededTeamDB(ctx context.Context, pool *pgxpool.Pool, t teamBinding, want string, logger logging.Logger) error {
	tag, err := pool.Exec(ctx,
		`UPDATE teams SET ch_database = $2, updated_at = now() WHERE slug = $1 AND ch_database = $3`,
		t.slug, want, t.db)
	if err != nil {
		return fmt.Errorf("rebase team %q database to %s: %w", t.slug, want, err)
	}
	if tag.RowsAffected() > 0 {
		logger.Info("seeded team database rebased for instance",
			logger.Str("team", t.slug),
			logger.Str("from", t.db),
			logger.Str("to", want))
		return nil
	}

	var current string
	if err := pool.QueryRow(ctx, `SELECT ch_database FROM teams WHERE slug = $1`, t.slug).Scan(&current); err != nil {
		return fmt.Errorf("reread team %q database: %w", t.slug, err)
	}
	if current != want {
		return fmt.Errorf("team %q points at clickhouse database %q, expected %q: %w",
			t.slug, current, want, errTeamDBUnexpected)
	}
	logger.Debug("seeded team database already rebased by another replica",
		logger.Str("team", t.slug),
		logger.Str("database", want))
	return nil
}

// teamDatabases — имена БД всех команд этой ноды.
func teamDatabases(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT ch_database FROM teams WHERE ch_database <> '' ORDER BY ch_database`)
	if err != nil {
		return nil, fmt.Errorf("read team databases: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var db string
		if err := rows.Scan(&db); err != nil {
			return nil, fmt.Errorf("scan team database: %w", err)
		}
		out = append(out, db)
	}
	return out, rows.Err()
}
