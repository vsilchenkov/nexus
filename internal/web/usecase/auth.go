package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

const sessionTokenBytes = 32

// AuthUsecase — login/logout/check; password operations.
type AuthUsecase struct {
	users      port.UserRepo
	sessions   port.SessionRepo
	audit      *AuditUsecase
	sessionTTL time.Duration
	logger     logging.Logger
}

func NewAuthUsecase(
	users port.UserRepo,
	sessions port.SessionRepo,
	audit *AuditUsecase,
	sessionTTL time.Duration,
	logger logging.Logger,
) *AuthUsecase {
	return &AuthUsecase{
		users:      users,
		sessions:   sessions,
		audit:      audit,
		sessionTTL: sessionTTL,
		logger:     logger,
	}
}

// Login проверяет пару login/password и создаёт сессию.
// Возвращает session-token (значение cookie) и пользователя.
func (u *AuthUsecase) Login(ctx context.Context, login, password, ip string) (string, *domain.User, error) {
	user, err := u.users.GetByLogin(ctx, login)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			u.audit.Log(ctx, Actor{UserLogin: login, IPAddress: ip},
				domain.ActionUserLoginFailed, "user", "", map[string]any{"reason": "not_found"})
			return "", nil, domain.ErrUnauthorized
		}
		return "", nil, fmt.Errorf("get user: %w", err)
	}
	if !user.Active {
		u.audit.Log(ctx, Actor{UserID: user.ID, UserLogin: user.Login, IPAddress: ip},
			domain.ActionUserLoginFailed, "user", user.ID, map[string]any{"reason": "inactive"})
		return "", nil, domain.ErrUserInactive
	}
	if user.PasswordHash == "" {
		// Дефолтный admin без пароля или пользователь без задан-пароля.
		return "", nil, domain.ErrUnauthorized
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		u.audit.Log(ctx, Actor{UserID: user.ID, UserLogin: user.Login, IPAddress: ip},
			domain.ActionUserLoginFailed, "user", user.ID, map[string]any{"reason": "bad_password"})
		return "", nil, domain.ErrUnauthorized
	}

	token, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	s := &domain.Session{
		Token:      token,
		UserID:     user.ID,
		Role:       user.Role,
		Lang:       user.Lang,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := u.sessions.Create(ctx, s, u.sessionTTL); err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}
	_ = u.users.UpdateLastLogin(ctx, user.ID, now)
	u.audit.Log(ctx, Actor{UserID: user.ID, UserLogin: user.Login, IPAddress: ip},
		domain.ActionUserLogin, "user", user.ID, nil)
	return token, user, nil
}

// Logout удаляет сессию.
func (u *AuthUsecase) Logout(ctx context.Context, token string) error {
	return u.sessions.Delete(ctx, token)
}

// Check валидирует session-token; продлевает TTL.
func (u *AuthUsecase) Check(ctx context.Context, token string) (*domain.Session, error) {
	s, err := u.sessions.Get(ctx, token)
	if err != nil {
		return nil, err
	}
	_ = u.sessions.Touch(ctx, token, u.sessionTTL)
	return s, nil
}

// Me возвращает текущего пользователя по идентификатору сессии. Нужен
// для /api/auth/me, чтобы вернуть login/email — Session хранит только
// user_id, role, lang.
func (u *AuthUsecase) Me(ctx context.Context, userID string) (*domain.User, error) {
	return u.users.Get(ctx, userID)
}

// ChangePassword — изменяет пароль пользователя (вызывается админом
// или самим пользователем). Все активные сессии этого пользователя
// удаляются (forced re-login, §7.1).
func (u *AuthUsecase) ChangePassword(ctx context.Context, actor Actor, userID, newPassword string, mustChange bool) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := u.users.UpdatePassword(ctx, userID, string(hash), mustChange); err != nil {
		return err
	}
	_, _ = u.sessions.DeleteByUser(ctx, userID)
	u.audit.Log(ctx, actor, domain.ActionUserPassword, "user", userID, nil)
	return nil
}

func randomToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
