package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// OneTimeTokenRepoPg — одноразовые ссылки в PostgreSQL (§88.4.1, миграция 0036).
//
// Берёт DBTX, а не *pgxpool.Pool: репозиторий обязан одинаково работать с
// пулом и с pgx.Tx (требование UnitOfWork, CLAUDE.md).
type OneTimeTokenRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.OneTimeTokenRepo = (*OneTimeTokenRepoPg)(nil)

func NewOneTimeTokenRepoPg(db DBTX, logger logging.Logger) *OneTimeTokenRepoPg {
	return &OneTimeTokenRepoPg{db: db, logger: logger}
}

const oneTimeTokenCols = `id, user_id, purpose, token_hash, payload,
	created_at, expires_at, used_at, request_ip`

func scanOneTimeToken(row pgx.Row) (*domain.OneTimeToken, error) {
	var (
		t       domain.OneTimeToken
		purpose string
		payload []byte
		usedAt  *time.Time
		ip      *net.IPNet
	)
	err := row.Scan(&t.ID, &t.UserID, &purpose, &t.TokenHash, &payload,
		&t.CreatedAt, &t.ExpiresAt, &usedAt, &ip)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrOneTimeTokenInvalid
		}
		return nil, fmt.Errorf("scan one_time_token: %w", err)
	}
	t.Purpose = domain.TokenPurpose(purpose)
	t.UsedAt = usedAt
	if ip != nil {
		t.RequestIP = ip.IP.String()
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &t.Payload); err != nil {
			return nil, fmt.Errorf("unmarshal one_time_token payload: %w", err)
		}
	}
	return &t, nil
}

func (r *OneTimeTokenRepoPg) Create(ctx context.Context, t *domain.OneTimeToken) error {
	payload := []byte("{}")
	if len(t.Payload) > 0 {
		b, err := json.Marshal(t.Payload)
		if err != nil {
			return fmt.Errorf("marshal one_time_token payload: %w", err)
		}
		payload = b
	}
	// Пустой IP пишем NULL, а не '' — колонка INET пустую строку не примет.
	var ip any
	if t.RequestIP != "" {
		ip = t.RequestIP
	}

	_, err := r.db.Exec(ctx,
		`INSERT INTO one_time_tokens
		    (user_id, purpose, token_hash, payload, expires_at, request_ip)
		 VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
		t.UserID, string(t.Purpose), t.TokenHash, payload, t.ExpiresAt, ip)
	if err != nil {
		return fmt.Errorf("one_time_token create: %w", err)
	}
	return nil
}

// Consume — атомарный CAS: строка гасится и возвращается одним запросом, без
// транзакции и без окна гонки. purpose в условии ОБЯЗАТЕЛЕН (§88.4.1): без
// него токен слабого назначения предъявляется на сильном эндпоинте.
func (r *OneTimeTokenRepoPg) Consume(
	ctx context.Context, purpose domain.TokenPurpose, tokenHash string, at time.Time,
) (*domain.OneTimeToken, error) {
	row := r.db.QueryRow(ctx,
		`UPDATE one_time_tokens SET used_at = $3
		  WHERE token_hash = $1 AND purpose = $2
		    AND used_at IS NULL AND expires_at > $3
		 RETURNING `+oneTimeTokenCols,
		tokenHash, string(purpose), at)

	t, err := scanOneTimeToken(row)
	if err != nil {
		if errors.Is(err, domain.ErrOneTimeTokenInvalid) {
			// Не найдено / чужое назначение / погашено / истекло — наружу
			// одна причина: различать их для предъявителя нельзя (§88.4.6).
			r.logger.Debug("one_time_token: consume miss",
				r.logger.Str("purpose", string(purpose)))
			return nil, domain.ErrOneTimeTokenInvalid
		}
		return nil, err
	}
	return t, nil
}

func (r *OneTimeTokenRepoPg) Peek(
	ctx context.Context, purpose domain.TokenPurpose, tokenHash string, at time.Time,
) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM one_time_tokens
		     WHERE token_hash = $1 AND purpose = $2
		       AND used_at IS NULL AND expires_at > $3)`,
		tokenHash, string(purpose), at).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("one_time_token peek: %w", err)
	}
	return exists, nil
}

func (r *OneTimeTokenRepoPg) CountActive(
	ctx context.Context, purpose domain.TokenPurpose, userID string, at time.Time,
) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM one_time_tokens
		  WHERE user_id = $1 AND purpose = $2
		    AND used_at IS NULL AND expires_at > $3`,
		userID, string(purpose), at).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("one_time_token count active: %w", err)
	}
	return n, nil
}

func (r *OneTimeTokenRepoPg) InvalidateByUser(
	ctx context.Context, purpose domain.TokenPurpose, userID string, at time.Time,
) (int, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE one_time_tokens SET used_at = $3
		  WHERE user_id = $1 AND purpose = $2
		    AND used_at IS NULL AND expires_at > $3`,
		userID, string(purpose), at)
	if err != nil {
		return 0, fmt.Errorf("one_time_token invalidate: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *OneTimeTokenRepoPg) DeleteExpiredBefore(ctx context.Context, before time.Time) (int, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM one_time_tokens WHERE expires_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("one_time_token cleanup: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
