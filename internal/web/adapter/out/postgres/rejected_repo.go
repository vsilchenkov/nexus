package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// RejectedRepoPg — PG-реализация port.RejectedRepo (§94, таблицы из миграции
// 0040).
//
// Работает через DBTX, а не *pgxpool.Pool: репозиторий должен одинаково жить с
// пулом и с pgx.Tx (требование UnitOfWork, см. db.go).
type RejectedRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.RejectedRepo = (*RejectedRepoPg)(nil)

func NewRejectedRepoPg(db DBTX, logger logging.Logger) *RejectedRepoPg {
	return &RejectedRepoPg{db: db, logger: logger}
}

// rejectedGroupCols — порядок колонок для scanGroup. Держать в одном месте:
// расхождение SELECT и Scan даёт ошибку только в рантайме.
//
// Команда приезжает из LEFT JOIN teams: слог хранится как пришёл в URL и может
// не соответствовать ни одной команде — тогда обе колонки пусты, и группа
// считается «команда не опознана» (§94.6).
const rejectedGroupCols = `g.id, g.team_slug, g.node_path, g.reason, g.http_method, g.status,
	g.first_seen, g.last_seen, g.count, g.clients, g.resolved_at, COALESCE(g.resolved_by, ''),
	COALESCE(t.id::text, ''), COALESCE(t.name, '')`

const rejectedGroupFrom = ` FROM rejected_groups g LEFT JOIN teams t ON t.slug = g.team_slug`

func (r *RejectedRepoPg) scanGroup(row rowScanner) (*domain.RejectedGroup, error) {
	var g domain.RejectedGroup
	err := row.Scan(&g.ID, &g.TeamSlug, &g.NodePath, &g.Reason, &g.HTTPMethod, &g.Status,
		&g.FirstSeen, &g.LastSeen, &g.Count, &g.Clients, &g.ResolvedAt, &g.ResolvedBy,
		&g.TeamID, &g.TeamName)
	if err != nil {
		// isInvalidUUID: `:id` из пути не UUID — это «не найдено» (404), а не
		// 500 с шумом в Sentry. Тот же приём, что в остальных репозиториях.
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, domain.ErrRejectedGroupNotFound
		}
		return nil, fmt.Errorf("scan rejected_groups: %w", err)
	}
	return &g, nil
}

// rejectedWhere собирает общую часть WHERE для List, Count и Summary:
// разъехавшиеся условия означали бы, что счётчик «из M» и строка на рабочем
// столе считают не то, что показано на экране.
func rejectedWhere(f port.RejectedFilter) (string, []any) {
	q := ""
	args := []any{}
	// Окно — по last_seen: группа попадает в выдачу, если отказы в неё
	// приходили внутри периода. По first_seen окно прятало бы давнюю группу,
	// в которую стучатся прямо сейчас.
	if !f.From.IsZero() {
		q += fmt.Sprintf(" AND g.last_seen >= $%d", len(args)+1)
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		q += fmt.Sprintf(" AND g.last_seen <= $%d", len(args)+1)
		args = append(args, f.To)
	}
	if len(f.Reasons) > 0 {
		reasons := make([]string, 0, len(f.Reasons))
		for _, x := range f.Reasons {
			reasons = append(reasons, string(x))
		}
		q += fmt.Sprintf(" AND g.reason = ANY($%d)", len(args)+1)
		args = append(args, reasons)
	}
	// nil = ограничения нет (администратор). Непустой список — область
	// видимости роли; пустой не-nil сюда не приходит по контракту порта.
	if f.TeamSlugs != nil {
		q += fmt.Sprintf(" AND g.team_slug = ANY($%d)", len(args)+1)
		args = append(args, f.TeamSlugs)
	}
	if f.UnknownTeamOnly {
		q += " AND t.id IS NULL"
	}
	if !f.IncludeResolved {
		q += " AND g.resolved_at IS NULL"
	}
	if f.Query != "" {
		// Поиск идёт и по клиентам: оператор ищет «кто это ходит», зная IP или
		// имя хоста, а не путь. EXISTS, а не JOIN — иначе группа с несколькими
		// подходящими клиентами задвоилась бы в выдаче.
		n := len(args) + 1
		q += fmt.Sprintf(` AND (g.node_path ILIKE '%%' || $%d || '%%'
			OR g.team_slug ILIKE '%%' || $%d || '%%'
			OR EXISTS (SELECT 1 FROM rejected_clients c WHERE c.group_id = g.id
				AND (host(c.client_ip) ILIKE '%%' || $%d || '%%'
					OR c.client_host ILIKE '%%' || $%d || '%%'
					OR c.user_agent ILIKE '%%' || $%d || '%%')))`, n, n, n, n, n)
		args = append(args, f.Query)
	}
	return q, args
}

func (r *RejectedRepoPg) ListRejected(ctx context.Context, f port.RejectedFilter) ([]*domain.RejectedGroup, error) {
	where, args := rejectedWhere(f)
	q := `SELECT ` + rejectedGroupCols + rejectedGroupFrom + ` WHERE 1=1` + where +
		` ORDER BY g.last_seen DESC, g.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET $%d", len(args)+1)
		args = append(args, f.Offset)
	}

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list rejected_groups: %w", err)
	}
	defer rows.Close()

	var out []*domain.RejectedGroup
	for rows.Next() {
		g, err := r.scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CountRejected — сколько групп подходит под фильтр целиком. Limit и Offset
// намеренно игнорируются: счётчик отвечает на вопрос «из скольких».
func (r *RejectedRepoPg) CountRejected(ctx context.Context, f port.RejectedFilter) (int64, error) {
	where, args := rejectedWhere(f)
	var n int64
	err := r.db.QueryRow(ctx, `SELECT count(*)`+rejectedGroupFrom+` WHERE 1=1`+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count rejected_groups: %w", err)
	}
	return n, nil
}

// SummaryRejected считает всё одним запросом: строка рабочего стола и бейдж
// раздела обновляются на каждом тике автообновления, и четыре отдельных
// похода в PG за одними и теми же строками здесь ничем не оправданы.
func (r *RejectedRepoPg) SummaryRejected(ctx context.Context, f port.RejectedFilter) (port.RejectedSummary, error) {
	where, args := rejectedWhere(f)
	q := `WITH f AS (SELECT g.id, g.count, g.resolved_at` + rejectedGroupFrom + ` WHERE 1=1` + where + `)
SELECT COALESCE(SUM(f.count), 0),
       COUNT(*),
       COUNT(*) FILTER (WHERE f.resolved_at IS NULL),
       (SELECT COUNT(DISTINCT c.client_ip) FROM rejected_clients c
          WHERE c.group_id IN (SELECT id FROM f))
FROM f`
	var s port.RejectedSummary
	if err := r.db.QueryRow(ctx, q, args...).Scan(&s.Count, &s.Groups, &s.Unresolved, &s.Clients); err != nil {
		return port.RejectedSummary{}, fmt.Errorf("summary rejected_groups: %w", err)
	}
	return s, nil
}

func (r *RejectedRepoPg) GetRejected(ctx context.Context, id string) (*domain.RejectedGroup, error) {
	return r.scanGroup(r.db.QueryRow(ctx,
		`SELECT `+rejectedGroupCols+rejectedGroupFrom+` WHERE g.id = $1::uuid`, id))
}

func (r *RejectedRepoPg) ListRejectedClients(ctx context.Context, groupID string, limit int) ([]domain.RejectedClient, error) {
	if limit <= 0 {
		limit = domain.RejectedMaxClientsPerGroup
	}
	rows, err := r.db.Query(ctx, `
SELECT host(client_ip), client_host, user_agent, count, first_seen, last_seen
FROM rejected_clients WHERE group_id = $1::uuid
ORDER BY count DESC, last_seen DESC LIMIT $2`, groupID, limit)
	if err != nil {
		if isInvalidUUID(err) {
			return nil, domain.ErrRejectedGroupNotFound
		}
		return nil, fmt.Errorf("list rejected_clients: %w", err)
	}
	defer rows.Close()

	var out []domain.RejectedClient
	for rows.Next() {
		var c domain.RejectedClient
		if err := rows.Scan(&c.IP, &c.Host, &c.UserAgent, &c.Count, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, fmt.Errorf("scan rejected_clients: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *RejectedRepoPg) ListRejectedSamples(ctx context.Context, groupID string, limit int) ([]domain.RejectedSample, error) {
	if limit <= 0 {
		limit = domain.RejectedMaxSamplesPerGroup
	}
	rows, err := r.db.Query(ctx, `
SELECT at, host(client_ip), http_method, raw_path, status, body_bytes, request_id, headers
FROM rejected_samples WHERE group_id = $1::uuid
ORDER BY at DESC, id DESC LIMIT $2`, groupID, limit)
	if err != nil {
		if isInvalidUUID(err) {
			return nil, domain.ErrRejectedGroupNotFound
		}
		return nil, fmt.Errorf("list rejected_samples: %w", err)
	}
	defer rows.Close()

	var out []domain.RejectedSample
	for rows.Next() {
		var s domain.RejectedSample
		var headersRaw []byte
		if err := rows.Scan(&s.At, &s.ClientIP, &s.HTTPMethod, &s.RawPath,
			&s.Status, &s.BodyBytes, &s.RequestID, &headersRaw); err != nil {
			return nil, fmt.Errorf("scan rejected_samples: %w", err)
		}
		// Битый JSON в колонке (руками поправленная строка) не должен ронять
		// всю выдачу: сэмпл покажется без заголовков.
		if len(headersRaw) > 0 {
			if err := json.Unmarshal(headersRaw, &s.Headers); err != nil {
				r.logger.Warn("rejected sample headers are not a json object",
					r.logger.Str("group", groupID), r.logger.Err(err))
			}
		}
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *RejectedRepoPg) ResolveRejected(ctx context.Context, id, by string, at time.Time) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE rejected_groups SET resolved_at = $2, resolved_by = $3 WHERE id = $1::uuid`,
		id, at, by)
	if err != nil {
		if isInvalidUUID(err) {
			return domain.ErrRejectedGroupNotFound
		}
		return fmt.Errorf("resolve rejected_groups: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrRejectedGroupNotFound
	}
	return nil
}

// ResolveAllRejected помечает просмотренными все неотмеченные группы области
// видимости.
//
// Условия скоупа собирает тот же rejectedWhere, что и список: разъехавшись,
// кнопка «пометить все» пометила бы не то, что человек видит в разделе — в том
// числе чужие команды. Псевдоним `g` живёт только внутри подзапроса, поэтому
// текст условий переиспользуется как есть, без переписывания псевдонимов.
func (r *RejectedRepoPg) ResolveAllRejected(ctx context.Context, f port.RejectedFilter, by string, at time.Time) (int, error) {
	// Отмеченные не трогаем: иначе у давно просмотренной группы переписалось бы
	// имя и время, и «кто это смотрел» стало бы неправдой.
	f.IncludeResolved = false
	where, args := rejectedWhere(f)
	q := fmt.Sprintf(`
UPDATE rejected_groups SET resolved_at = $%d, resolved_by = $%d
WHERE id IN (SELECT g.id%s WHERE 1=1%s)`,
		len(args)+1, len(args)+2, rejectedGroupFrom, where)
	args = append(args, at, by)

	tag, err := r.db.Exec(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("resolve all rejected_groups: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *RejectedRepoPg) DeleteRejected(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM rejected_groups WHERE id = $1::uuid`, id)
	if err != nil {
		if isInvalidUUID(err) {
			return domain.ErrRejectedGroupNotFound
		}
		return fmt.Errorf("delete rejected_groups: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrRejectedGroupNotFound
	}
	return nil
}

func (r *RejectedRepoPg) DeleteRejectedOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM rejected_groups WHERE last_seen < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old rejected_groups: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *RejectedRepoPg) TrimRejectedGroups(ctx context.Context, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	// NOT IN по подзапросу с LIMIT: тот же приём, что у кольца сэмплов. Индекс
	// rejected_groups_last_seen_idx обслуживает и подзапрос, и сортировку.
	tag, err := r.db.Exec(ctx, `
DELETE FROM rejected_groups WHERE id NOT IN (
    SELECT id FROM rejected_groups ORDER BY last_seen DESC LIMIT $1)`, keep)
	if err != nil {
		return 0, fmt.Errorf("trim rejected_groups: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *RejectedRepoPg) PurgeRejected(ctx context.Context) (int, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM rejected_groups`)
	if err != nil {
		return 0, fmt.Errorf("purge rejected_groups: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
