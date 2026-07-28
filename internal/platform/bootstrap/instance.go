package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// pgUndefinedTable — SQLSTATE 42P01. Возникает, если сервис стартовал раньше,
// чем миграция 0029 создала instance_identity (Sender миграции не накатывает).
const pgUndefinedTable = "42P01"

// Identity — идентичность ноды после сверки с PostgreSQL (§70.5).
//
// FreshPG отвечает на вопрос «эту ноду только что подняли?»: в базе нет ни
// одного узла и есть только сидированная команда. Именно он, а не маркер
// владения, служит признаком первого запуска — пустой instance.id одинаков у
// всех нод, существовавших до §70, поэтому по маркеру `instance_id=”` две
// такие ноды неразличимы.
//
// Claimed — строку instance_identity создали мы (то есть до нас идентификатор
// не заявлял никто). Вместе с FreshPG даёт право один раз переименовать
// ch_database сидированной команды под суффикс ноды (§70.5).
type Identity struct {
	ID      domain.InstanceID
	FreshPG bool
	Claimed bool
	// CHClaimed — нода уже успешно захватывала свои БД в ClickHouse (флаг
	// instance_identity.ch_claimed). Именно он, а не FreshPG, отличает первый
	// запуск от последующих для гейта владения: у только что развёрнутой ноды
	// узлов нет и на втором старте, и гейт срабатывал бы на её собственную БД.
	CHClaimed bool
}

// MustInstanceIdentity заявляет идентификатор ноды в её PostgreSQL и сверяет
// его с конфигом. Вызывается всеми тремя сервисами сразу после подключения к
// PostgreSQL — идентичность не зависит от ClickHouse (Receiver в него не ходит).
//
// Расхождение сохранённого значения с конфигом завершает процесс: сменить
// instance.id на живой ноде нельзя, потому что имена уже созданных БД
// ClickHouse от этого не меняются, и нода начала бы писать в пустое место.
// Осознанная смена — флагом --instance-id-force, который переписывает значение
// и предупреждает, что БД нужно переименовать вручную.
//
// Отсутствие таблицы (сервис стартовал раньше миграций) не фатально: работаем
// по значению из конфига и консервативно считаем ноду не свежей — так гейт
// первого запуска (§70.5) не сработает вхолостую и не остановит старт.
func MustInstanceIdentity(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg *config.Config,
	flags config.Flags,
	service string,
	logger logging.Logger,
) Identity {
	// Флаг запуска — разовая форма настройки конфига: дальше по коду читается
	// только cfg.Instance.AdoptUnowned, чтобы не тащить Flags в App'ы сервисов.
	if flags.CHAdopt {
		cfg.Instance.AdoptUnowned = true
	}

	want := domain.InstanceID(cfg.Instance.ID)
	if err := want.Validate(); err != nil {
		logger.ErrorWithOp("invalid instance.id", err, "bootstrap.MustInstanceIdentity",
			logger.Str("instance_id", cfg.Instance.ID))
		os.Exit(1)
	}

	id, err := claimInstanceID(ctx, pool, want, service)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUndefinedTable {
			logger.Warn("instance_identity table is missing; migrations not applied yet",
				logger.Str("instance_id", want.String()),
				logger.Str("service", service))
			return Identity{ID: want}
		}
		logger.ErrorWithOp("claim instance id failed", err, "bootstrap.MustInstanceIdentity")
		os.Exit(1)
	}

	if id.stored != want {
		if !flags.InstanceIDForce {
			logger.Error("instance.id mismatch: refusing to start",
				logger.Str("stored", id.stored.String()),
				logger.Str("config", want.String()),
				logger.Str("hint", "renaming instance.id does not rename existing ClickHouse databases; "+
					"restore the previous value or rerun with --instance-id-force after renaming them manually"))
			os.Exit(1)
		}
		if err := forceInstanceID(ctx, pool, want, service); err != nil {
			logger.ErrorWithOp("force instance id failed", err, "bootstrap.MustInstanceIdentity")
			os.Exit(1)
		}
		logger.Warn("instance.id overwritten by --instance-id-force; ClickHouse databases were NOT renamed",
			logger.Str("previous", id.stored.String()),
			logger.Str("current", want.String()))
	}

	fresh, err := isFreshPG(ctx, pool)
	if err != nil {
		// Не фатально: FreshPG используется только гейтом первого запуска, и
		// консервативное false лишь снимает лишнюю строгость (§70.5).
		logger.Warn("detect fresh postgres failed; assuming an already used instance",
			logger.Err(err))
	}

	logger.Debug("instance identity resolved",
		logger.Str("instance_id", want.String()),
		logger.Str("service", service),
		logger.Any("claimed", id.claimed),
		logger.Any("fresh_pg", fresh))

	return Identity{ID: want, FreshPG: fresh, Claimed: id.claimed, CHClaimed: id.chClaimed}
}

// claimResult — что лежало в instance_identity и создали ли строку мы.
type claimResult struct {
	stored    domain.InstanceID
	claimed   bool
	chClaimed bool
}

// claimInstanceID вставляет идентификатор, если его ещё не заявляли, и
// возвращает актуальное значение. ON CONFLICT DO NOTHING делает вызов
// безопасным при одновременном старте трёх сервисов: значение у них одно.
func claimInstanceID(ctx context.Context, pool *pgxpool.Pool, want domain.InstanceID, service string) (claimResult, error) {
	tag, err := pool.Exec(ctx, `
INSERT INTO instance_identity (id, instance_id, claimed_by)
VALUES (1, $1, $2)
ON CONFLICT (id) DO NOTHING`, want.String(), claimedBy(service))
	if err != nil {
		return claimResult{}, fmt.Errorf("insert instance identity: %w", err)
	}

	var (
		stored    string
		chClaimed bool
	)
	if err := pool.QueryRow(ctx,
		`SELECT instance_id, ch_claimed FROM instance_identity WHERE id = 1`).Scan(&stored, &chClaimed); err != nil {
		return claimResult{}, fmt.Errorf("read instance identity: %w", err)
	}
	return claimResult{
		stored:    domain.InstanceID(stored),
		claimed:   tag.RowsAffected() > 0,
		chClaimed: chClaimed,
	}, nil
}

func forceInstanceID(ctx context.Context, pool *pgxpool.Pool, want domain.InstanceID, service string) error {
	if _, err := pool.Exec(ctx, `
UPDATE instance_identity
SET instance_id = $1, claimed_at = now(), claimed_by = $2
WHERE id = 1`, want.String(), claimedBy(service)); err != nil {
		return fmt.Errorf("update instance identity: %w", err)
	}
	return nil
}

// isFreshPG — в базе нет узлов и нет команд, кроме сидированной миграцией 0008.
// Это признак «ноду только что подняли»: любая рабочая нода имеет либо узлы,
// либо созданные оператором команды.
func isFreshPG(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var fresh bool
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM nodes) = 0
   AND (SELECT count(*) FROM teams) <= 1`).Scan(&fresh); err != nil {
		return false, fmt.Errorf("count nodes and teams: %w", err)
	}
	return fresh, nil
}

// claimedBy — диагностическая подпись «кто заявил»: сервис и хост. На решения
// не влияет, поэтому ошибка os.Hostname игнорируется осознанно.
func claimedBy(service string) string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	s := service + "@" + host
	const maxLen = 64 // = VARCHAR(64) в миграции 0029
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// MarkCHClaimed отмечает, что нода успешно захватила свои ClickHouse-БД.
// После этого гейт первого запуска (§70.5) на неё больше не действует —
// иначе повторный старт до создания первого узла упирался бы в собственную БД.
//
// Ошибка не фатальна: гейт лишь сработает ещё раз на следующем старте, а
// маркер владения к тому времени уже наш — операция пройдёт как обычная
// проверка.
func MarkCHClaimed(ctx context.Context, pool *pgxpool.Pool, logger logging.Logger) {
	if _, err := pool.Exec(ctx,
		`UPDATE instance_identity SET ch_claimed = true WHERE id = 1`); err != nil {
		logger.Warn("mark clickhouse ownership claimed failed", logger.Err(err))
	}
}
