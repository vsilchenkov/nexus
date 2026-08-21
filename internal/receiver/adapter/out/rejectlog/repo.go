// Package rejectlog — журнал отказов на входе (§94): агрегация в памяти
// Receiver и сброс накопленного в PostgreSQL.
package rejectlog

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
)

// Repo пишет накопленные агрегаты в таблицы миграции 0040.
//
// Берёт *pgxpool.Pool напрямую (как nodecache): это адаптер конкретной СУБД,
// транзакции здесь свои и в UnitOfWork Web не участвуют.
type Repo struct {
	pg *pgxpool.Pool
}

func NewRepo(pg *pgxpool.Pool) *Repo {
	return &Repo{pg: pg}
}

// Flush записывает пачку агрегатов одной транзакцией.
//
// Транзакция общая на всю пачку, а не на группу: сброс идёт раз в несколько
// секунд и содержит единицы групп, а сотня отдельных транзакций стоила бы
// столько же fsync'ов. Ошибка теряет всю пачку — это осознанный best-effort
// (§94.4): журнал наблюдаемости не имеет права ни задерживать боевой трафик,
// ни удерживать его в памяти в ожидании починки базы.
//
// Count в агрегате — ПРИРОСТ: значения складываются с сохранёнными, поэтому
// несколько реплик Receiver пишут в одну группу без всякой синхронизации.
func (r *Repo) Flush(ctx context.Context, aggs []domain.RejectedAggregate) error {
	if len(aggs) == 0 {
		return nil
	}
	tx, err := r.pg.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rejectlog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op после успешного Commit

	for i := range aggs {
		if err := writeAggregate(ctx, tx, &aggs[i]); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rejectlog: commit: %w", err)
	}
	return nil
}

func writeAggregate(ctx context.Context, tx pgx.Tx, a *domain.RejectedAggregate) error {
	groupID, err := upsertGroup(ctx, tx, a)
	if err != nil {
		return err
	}
	if err := upsertClients(ctx, tx, groupID, a.Clients); err != nil {
		return err
	}
	if err := insertSamples(ctx, tx, groupID, a.Samples); err != nil {
		return err
	}
	return nil
}

// upsertGroup создаёт или обновляет группу и возвращает её id.
//
// first_seen/last_seen сводятся через LEAST/GREATEST, а не присваиваются: пачки
// от разных реплик приходят в произвольном порядке, и «последняя запись
// выигрывает» двигала бы last_seen назад.
//
// resolved_at сбрасывается при любом новом отказе (§94.6): иначе однажды
// закрытая группа скрывала бы возобновившуюся проблему.
func upsertGroup(ctx context.Context, tx pgx.Tx, a *domain.RejectedAggregate) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
INSERT INTO rejected_groups
    (team_slug, node_path, reason, http_method, status, first_seen, last_seen, count, clients)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0)
ON CONFLICT ON CONSTRAINT rejected_groups_key DO UPDATE SET
    status      = EXCLUDED.status,
    first_seen  = LEAST(rejected_groups.first_seen, EXCLUDED.first_seen),
    last_seen   = GREATEST(rejected_groups.last_seen, EXCLUDED.last_seen),
    count       = rejected_groups.count + EXCLUDED.count,
    resolved_at = NULL,
    resolved_by = NULL
RETURNING id`,
		a.Key.TeamSlug, a.Key.NodePath, string(a.Key.Reason), a.Key.HTTPMethod,
		a.Status, a.FirstSeen, a.LastSeen, a.Count).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("rejectlog: upsert group: %w", err)
	}
	return id, nil
}

func upsertClients(ctx context.Context, tx pgx.Tx, groupID string, clients []domain.RejectedClient) error {
	if len(clients) == 0 {
		return nil
	}
	for _, c := range clients {
		// client_host и user_agent не затираются пустым значением: PTR-имя
		// приходит асинхронно (§67 — первый запрос знает только адрес, имя
		// появляется после фонового резолва), и пустое «ещё не знаю» не должно
		// стирать уже известное имя.
		_, err := tx.Exec(ctx, `
INSERT INTO rejected_clients
    (group_id, client_ip, client_host, user_agent, count, first_seen, last_seen)
VALUES ($1::uuid, $2::inet, $3, $4, $5, $6, $7)
ON CONFLICT (group_id, client_ip) DO UPDATE SET
    client_host = CASE WHEN EXCLUDED.client_host <> '' THEN EXCLUDED.client_host
                       ELSE rejected_clients.client_host END,
    user_agent  = CASE WHEN EXCLUDED.user_agent <> '' THEN EXCLUDED.user_agent
                       ELSE rejected_clients.user_agent END,
    count       = rejected_clients.count + EXCLUDED.count,
    first_seen  = LEAST(rejected_clients.first_seen, EXCLUDED.first_seen),
    last_seen   = GREATEST(rejected_clients.last_seen, EXCLUDED.last_seen)`,
			groupID, c.IP, c.Host, c.UserAgent, c.Count, c.FirstSeen, c.LastSeen)
		if err != nil {
			return fmt.Errorf("rejectlog: upsert client: %w", err)
		}
	}

	// Вытеснение сверх лимита: сканер с тысячей адресов иначе раздул бы одну
	// группу до тысячи строк. Держим самых недавних — интерес представляют
	// активные клиенты.
	if _, err := tx.Exec(ctx, `
DELETE FROM rejected_clients
WHERE group_id = $1::uuid AND client_ip NOT IN (
    SELECT client_ip FROM rejected_clients WHERE group_id = $1::uuid
    ORDER BY last_seen DESC LIMIT $2)`,
		groupID, domain.RejectedMaxClientsPerGroup); err != nil {
		return fmt.Errorf("rejectlog: trim clients: %w", err)
	}

	// Денормализованный счётчик клиентов группы — после вытеснения, иначе он
	// показывал бы строки, которых уже нет.
	if _, err := tx.Exec(ctx, `
UPDATE rejected_groups SET clients = (
    SELECT count(*) FROM rejected_clients WHERE group_id = $1::uuid)
WHERE id = $1::uuid`, groupID); err != nil {
		return fmt.Errorf("rejectlog: update clients count: %w", err)
	}
	return nil
}

func insertSamples(ctx context.Context, tx pgx.Tx, groupID string, samples []domain.RejectedSample) error {
	if len(samples) == 0 {
		return nil
	}
	for _, s := range samples {
		headers, err := json.Marshal(s.Headers)
		if err != nil {
			return fmt.Errorf("rejectlog: marshal sample headers: %w", err)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO rejected_samples
    (group_id, at, client_ip, http_method, raw_path, status, body_bytes, request_id, headers)
VALUES ($1::uuid, $2, $3::inet, $4, $5, $6, $7, $8, $9::jsonb)`,
			groupID, s.At, s.ClientIP, s.HTTPMethod, s.RawPath, s.Status, s.BodyBytes,
			s.RequestID, headers)
		if err != nil {
			return fmt.Errorf("rejectlog: insert sample: %w", err)
		}
	}

	// Кольцо последних N: старые сэмплы удаляются сразу после вставки новых,
	// иначе долгоживущая группа копила бы их без предела.
	if _, err := tx.Exec(ctx, `
DELETE FROM rejected_samples
WHERE group_id = $1::uuid AND id NOT IN (
    SELECT id FROM rejected_samples WHERE group_id = $1::uuid
    ORDER BY at DESC, id DESC LIMIT $2)`,
		groupID, domain.RejectedMaxSamplesPerGroup); err != nil {
		return fmt.Errorf("rejectlog: trim samples: %w", err)
	}
	return nil
}
