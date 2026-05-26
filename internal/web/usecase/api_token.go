package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// APITokenPrefix — общий префикс для всех токенов шины (§7.14):
// позволяет middleware отличать API-токен от прокси-Bearer'ов.
const APITokenPrefix = "db_"

const tokenRandomBytes = 32

// APITokenUsecase — CRUD + проверка токенов.
type APITokenUsecase struct {
	repo   port.APITokenRepo
	audit  *AuditUsecase
	users  port.UserRepo
	logger logging.Logger
}

func NewAPITokenUsecase(
	repo port.APITokenRepo,
	users port.UserRepo,
	audit *AuditUsecase,
	logger logging.Logger,
) *APITokenUsecase {
	return &APITokenUsecase{repo: repo, users: users, audit: audit, logger: logger}
}

// CreatedToken — то, что возвращается при создании. Поле PlainToken
// отдаётся клиенту ОДИН РАЗ (§7.14: «показывается ровно один раз»).
type CreatedToken struct {
	Token *domain.APIToken
	Plain string
}

// Create генерирует токен и сохраняет SHA-256(token) в БД.
func (u *APITokenUsecase) Create(
	ctx context.Context,
	actor Actor,
	userID, name string,
	scopes []string,
	expiresAt *time.Time,
) (*CreatedToken, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}
	plain, err := generateToken()
	if err != nil {
		return nil, err
	}
	hash := hashToken(plain)
	t := &domain.APIToken{
		UserID:    userID,
		Name:      name,
		TokenHash: hash,
		Prefix:    plain[:min(8, len(plain))],
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	}
	if err := u.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	u.audit.Log(ctx, actor, domain.ActionAPITokenCreate, "api_token", t.ID, map[string]any{
		"name":   name,
		"scopes": scopes,
	})
	return &CreatedToken{Token: t, Plain: plain}, nil
}

func (u *APITokenUsecase) ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	return u.repo.ListByUser(ctx, userID)
}

func (u *APITokenUsecase) Revoke(ctx context.Context, actor Actor, id, userID string) error {
	if err := u.repo.Revoke(ctx, id, userID); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionAPITokenRevoke, "api_token", id, nil)
	return nil
}

func (u *APITokenUsecase) Delete(ctx context.Context, actor Actor, id, userID string) error {
	if err := u.repo.Delete(ctx, id, userID); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionAPITokenDelete, "api_token", id, nil)
	return nil
}

// Verify проверяет переданное значение токена (с db_ префиксом).
// Возвращает токен и его владельца. last_used_at обновляется асинхронно
// (через горутину; в Phase 4 — батчинг через канал).
func (u *APITokenUsecase) Verify(ctx context.Context, value string) (*domain.APIToken, *domain.User, error) {
	if len(value) < len(APITokenPrefix)+8 || value[:len(APITokenPrefix)] != APITokenPrefix {
		return nil, nil, errors.New("not an api token")
	}
	hash := hashToken(value)
	t, err := u.repo.GetByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil, domain.ErrUnauthorized
		}
		return nil, nil, fmt.Errorf("lookup token: %w", err)
	}
	if !t.IsActive(time.Now()) {
		return nil, nil, domain.ErrUnauthorized
	}
	user, err := u.users.Get(ctx, t.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup user: %w", err)
	}
	if !user.Active {
		return nil, nil, domain.ErrUserInactive
	}
	// Best-effort last_used_at; не блокирует ответ.
	go func() { _ = u.repo.TouchLastUsed(context.Background(), t.ID) }()
	return t, user, nil
}

func generateToken() (string, error) {
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return APITokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
