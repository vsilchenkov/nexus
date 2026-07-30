package bootstrap

import (
	"context"
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
// chConn == nil (Web стартует без ClickHouse) — гейт пропускается: без
// соединения ни писать, ни удалять нода всё равно не может.
func MustCHOwnership(
	ctx context.Context,
	pool *pgxpool.Pool,
	chConn chdrv.Conn,
	cfg *config.Config,
	identity Identity,
	logger logging.Logger,
) {
	if chConn == nil {
		logger.Warn("clickhouse is not available; ownership gate skipped",
			logger.Str("instance", identity.ID.String()))
		return
	}

	if err := rebaseSeededTeamDB(ctx, pool, identity, logger); err != nil {
		logger.ErrorWithOp("rebase seeded team database failed", err, "bootstrap.MustCHOwnership")
		os.Exit(1)
	}

	dbs, err := teamDatabases(ctx, pool)
	if err != nil {
		logger.ErrorWithOp("list team databases failed", err, "bootstrap.MustCHOwnership")
		os.Exit(1)
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
		os.Exit(1)
	}
	// Отмечаем захват: со следующего старта гейт первого запуска не действует —
	// иначе перезапуск ноды до создания первого узла упирался бы в её же БД.
	MarkCHClaimed(ctx, pool, logger)
	logger.Info("clickhouse ownership confirmed",
		logger.Str("instance", identity.ID.String()),
		logger.Int("databases", len(dbs)))
}

// staticConnProvider — ConnProvider поверх исходного соединения. Manager на этом
// этапе ещё не создан (он живёт в App), а гейт разовый: hot-reload ему не нужен.
type staticConnProvider struct{ conn chdrv.Conn }

func (s staticConnProvider) Conn() chdrv.Conn { return s.conn }

// rebaseSeededTeamDB приводит имена БД ВСЕХ команд ноды к её идентификатору.
// На практике это ровно одна сидированная миграцией 0008 команда `default`
// (`nexus_default` → `nexus_<id>_default`): команды, созданные уже на ноде с
// идентификатором, имеют правильное имя и пропускаются. Проход по всем нужен,
// чтобы рассинхрон (ручная правка teams.ch_database) не остался незамеченным.
//
// Переименование выполняется только на первом запуске ноды (идентификатор
// заявлен нами, PostgreSQL свежая) и только пока имя равно сидированному. Если
// идентификатор задан, а условия не сошлись — это ошибка: молчаливое
// продолжение оставило бы новую ноду писать в БД соседа.
func rebaseSeededTeamDB(ctx context.Context, pool *pgxpool.Pool, identity Identity, logger logging.Logger) error {
	if identity.ID.IsZero() {
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT slug, ch_database FROM teams ORDER BY slug`)
	if err != nil {
		return fmt.Errorf("read teams: %w", err)
	}
	defer rows.Close()

	type team struct{ slug, db string }
	var teams []team
	for rows.Next() {
		var t team
		if err := rows.Scan(&t.slug, &t.db); err != nil {
			return fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read teams: %w", err)
	}

	for _, t := range teams {
		want := identity.ID.CHDatabase(t.slug)
		if t.db == want {
			continue
		}
		if t.db != domain.CHDatabaseForSlug(t.slug) {
			return fmt.Errorf("team %q points at clickhouse database %q, expected %q for instance %q: "+
				"rename the database and update teams.ch_database manually",
				t.slug, t.db, want, identity.ID)
		}
		if !identity.Claimed || !identity.FreshPG {
			return fmt.Errorf("instance %q is configured, but team %q is already bound to %q on a used database: "+
				"migrate the data manually before switching instance.id", identity.ID, t.slug, t.db)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE teams SET ch_database = $2, updated_at = now() WHERE slug = $1`, t.slug, want); err != nil {
			return fmt.Errorf("rebase team %q database to %s: %w", t.slug, want, err)
		}
		logger.Info("seeded team database rebased for instance",
			logger.Str("team", t.slug),
			logger.Str("from", t.db),
			logger.Str("to", want))
	}
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
