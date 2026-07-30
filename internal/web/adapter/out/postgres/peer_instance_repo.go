package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// PeerInstanceRepoPg — PG-реализация port.PeerInstanceRepo (§73, таблица
// peer_instances из миграции 0032).
//
// Работает через DBTX, а не *pgxpool.Pool: репозиторий должен одинаково жить с
// пулом и с pgx.Tx (требование UnitOfWork, см. db.go).
type PeerInstanceRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.PeerInstanceRepo = (*PeerInstanceRepoPg)(nil)

func NewPeerInstanceRepoPg(db DBTX, logger logging.Logger) *PeerInstanceRepoPg {
	return &PeerInstanceRepoPg{db: db, logger: logger}
}

// peerInstanceCols — порядок колонок для scanPeerInstance. Держать в одном
// месте: расхождение SELECT и Scan даёт ошибку только в рантайме.
const peerInstanceCols = `id, title, base_url, comment,
	last_status, last_version, last_instance_id, last_latency_ms, last_error, last_checked_at,
	created_by, updated_by, created_at, updated_at`

func (r *PeerInstanceRepoPg) scan(row rowScanner) (*domain.PeerInstance, error) {
	var p domain.PeerInstance
	err := row.Scan(&p.ID, &p.Title, &p.BaseURL, &p.Comment,
		&p.LastStatus, &p.LastVersion, &p.LastInstanceID, &p.LastLatencyMS, &p.LastError, &p.LastCheckedAt,
		&p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPeerInstanceNotFound
		}
		return nil, fmt.Errorf("scan peer_instances: %w", err)
	}
	return &p, nil
}

func (r *PeerInstanceRepoPg) ListPeerInstances(ctx context.Context) ([]*domain.PeerInstance, error) {
	rows, err := r.db.Query(ctx, `SELECT `+peerInstanceCols+` FROM peer_instances ORDER BY title, base_url`)
	if err != nil {
		return nil, fmt.Errorf("list peer_instances: %w", err)
	}
	defer rows.Close()
	var out []*domain.PeerInstance
	for rows.Next() {
		p, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PeerInstanceRepoPg) GetPeerInstance(ctx context.Context, id string) (*domain.PeerInstance, error) {
	return r.scan(r.db.QueryRow(ctx,
		`SELECT `+peerInstanceCols+` FROM peer_instances WHERE id = $1::uuid`, id))
}

func (r *PeerInstanceRepoPg) CreatePeerInstance(ctx context.Context, p *domain.PeerInstance) error {
	// created_by и updated_by получают ОТДЕЛЬНЫЕ плейсхолдеры ($4 и $5), хотя
	// значение одно: повторная подстановка одного $N в INSERT роняет pgx с 42P08
	// («could not determine data type of parameter») — грабли §66 и §71.
	err := r.db.QueryRow(ctx, `
INSERT INTO peer_instances (title, base_url, comment, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at, updated_at`,
		p.Title, p.BaseURL, p.Comment, p.CreatedBy, p.CreatedBy,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrPeerInstanceAlreadyExists
		}
		return fmt.Errorf("create peer_instances: %w", err)
	}
	p.UpdatedBy = p.CreatedBy
	return nil
}

// UpdatePeerInstance меняет только конфигурацию. Кеш последней пробы намеренно
// не сбрасывается даже при смене адреса: пустой статус в таблице читался бы как
// «инстанс не отвечает», хотя проверки ещё просто не было. Актуальность видна по
// last_checked_at, а интерфейс перепроверяет список сразу после сохранения.
func (r *PeerInstanceRepoPg) UpdatePeerInstance(ctx context.Context, p *domain.PeerInstance) error {
	tag, err := r.db.Exec(ctx, `
UPDATE peer_instances
SET title = $2, base_url = $3, comment = $4, updated_by = $5, updated_at = now()
WHERE id = $1::uuid`,
		p.ID, p.Title, p.BaseURL, p.Comment, p.UpdatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrPeerInstanceAlreadyExists
		}
		return fmt.Errorf("update peer_instances: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPeerInstanceNotFound
	}
	return nil
}

func (r *PeerInstanceRepoPg) DeletePeerInstance(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM peer_instances WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete peer_instances: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPeerInstanceNotFound
	}
	return nil
}

// SavePeerInstanceProbe сохраняет исход пробы.
//
// updated_at НЕ трогается: это отметка правки конфигурации оператором, и опрос
// (который идёт при каждом открытии вкладки) затирал бы её, превращая «изменено
// вчера» в «изменено только что».
//
// Пропажа строки не ошибка: между сбором списка и записью результата инстанс мог
// быть удалён другим администратором. Возвращать ErrPeerInstanceNotFound значило
// бы валить весь опрос из-за гонки, которую никто не может предотвратить.
func (r *PeerInstanceRepoPg) SavePeerInstanceProbe(ctx context.Context, id string, res port.PeerInstanceProbe) error {
	tag, err := r.db.Exec(ctx, `
UPDATE peer_instances
SET last_status = $2, last_version = $3, last_instance_id = $4,
    last_latency_ms = $5, last_error = $6, last_checked_at = $7
WHERE id = $1::uuid`,
		id, string(res.Status), res.Version, res.InstanceID, res.LatencyMS, res.Error, res.CheckedAt)
	if err != nil {
		return fmt.Errorf("save peer_instances probe: %w", err)
	}
	if tag.RowsAffected() == 0 {
		r.logger.Debug("peer instance probe result dropped: row is gone",
			r.logger.Str("instance_id", id))
	}
	return nil
}
