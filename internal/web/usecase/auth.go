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

// ErrLoginRateLimited — превышен лимит попыток логина (анти-брутфорс,
// Phase AUD.4). Handler отвечает 429.
var ErrLoginRateLimited = errors.New("login rate limit exceeded")

// AuthUsecase — login/logout/check; password operations; team-switcher.
type AuthUsecase struct {
	users      port.UserRepo
	sessions   port.SessionRepo
	teams      port.TeamRepo
	audit      *AuditUsecase
	sessionTTL time.Duration
	logger     logging.Logger

	// Анти-брутфорс логина (Phase AUD.4): rl == nil или limit <= 0 —
	// проверка выключена (unit-тесты, отсутствие Redis).
	rl                 RateLimiter
	loginRateLimitPMin int
}

func NewAuthUsecase(
	users port.UserRepo,
	sessions port.SessionRepo,
	teams port.TeamRepo,
	audit *AuditUsecase,
	sessionTTL time.Duration,
	logger logging.Logger,
) *AuthUsecase {
	return &AuthUsecase{
		users:      users,
		sessions:   sessions,
		teams:      teams,
		audit:      audit,
		sessionTTL: sessionTTL,
		logger:     logger,
	}
}

// WithLoginRateLimit включает лимит попыток логина: limitPerMin попыток в
// минуту на IP и столько же на конкретный login (две независимые квоты —
// distributed-брутфорс одного аккаунта ловится по login-ключу, перебор
// аккаунтов с одного адреса — по IP-ключу).
func (u *AuthUsecase) WithLoginRateLimit(rl RateLimiter, limitPerMin int) *AuthUsecase {
	u.rl = rl
	u.loginRateLimitPMin = limitPerMin
	return u
}

// checkLoginRateLimit — true, если попытку можно пропустить. Fail-open при
// ошибке Redis (§9.4 ТЗ — лимиты при сбое Redis временно отключаются).
func (u *AuthUsecase) checkLoginRateLimit(ctx context.Context, login, ip string) bool {
	if u.rl == nil || u.loginRateLimitPMin <= 0 {
		return true
	}
	for _, key := range []string{"login:ip:" + ip, "login:user:" + login} {
		ok, err := u.rl.Allow(ctx, key, u.loginRateLimitPMin)
		if err != nil {
			u.logger.Warn("login rate limit check failed; allowing",
				u.logger.Str("key", key), u.logger.Err(err))
			continue
		}
		if !ok {
			return false
		}
	}
	return true
}

// Login проверяет пару login/password и создаёт сессию.
// Возвращает session-token (значение cookie) и пользователя.
func (u *AuthUsecase) Login(ctx context.Context, login, password, ip string) (string, *domain.User, error) {
	if !u.checkLoginRateLimit(ctx, login, ip) {
		u.audit.Log(ctx, Actor{UserLogin: login, IPAddress: ip},
			domain.ActionUserLoginFailed, "user", "", map[string]any{"reason": "rate_limited"})
		return "", nil, ErrLoginRateLimited
	}
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
		Token:              token,
		UserID:             user.ID,
		Login:              user.Login,
		Role:               user.Role,
		Lang:               user.Lang,
		CurrentTeamID:      user.DefaultTeamID,
		MustChangePassword: user.MustChangePassword,
		CreatedAt:          now,
		LastSeenAt:         now,
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

// MyTeams — список команд, в которых состоит пользователь (multi-tenancy
// v2). Используется UI для team-switcher'а.
func (u *AuthUsecase) MyTeams(ctx context.Context, userID string) ([]*domain.UserTeam, error) {
	return u.teams.ListUserTeams(ctx, userID)
}

// SwitchTeam меняет current_team_id в активной сессии. Проверяет, что
// пользователь является членом запрашиваемой команды (через user_teams).
// При успехе обновляет сессию в Redis (TTL не меняется — Touch отдельно).
//
// Возвращает обновлённую *domain.Session, чтобы handler мог сразу
// положить её в context для последующих request-action'ов.
func (u *AuthUsecase) SwitchTeam(ctx context.Context, actor Actor, token, teamID string) (*domain.Session, error) {
	s, err := u.sessions.Get(ctx, token)
	if err != nil {
		return nil, err
	}
	if s.UserID != actor.UserID {
		return nil, domain.ErrPermissionDenied
	}

	memberships, err := u.teams.ListUserTeams(ctx, actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	found := false
	for _, ut := range memberships {
		if ut.Team.ID == teamID {
			found = true
			break
		}
	}
	if !found {
		return nil, domain.ErrPermissionDenied
	}

	s.CurrentTeamID = teamID
	if err := u.sessions.Create(ctx, s, u.sessionTTL); err != nil {
		return nil, fmt.Errorf("update session: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionTeamSwitch, "team", teamID, nil)
	return s, nil
}

// ChangePassword — изменяет пароль пользователя (вызывается админом).
// Все активные сессии этого пользователя удаляются (forced re-login, §7.1).
func (u *AuthUsecase) ChangePassword(ctx context.Context, actor Actor, userID, newPassword string, mustChange bool) error {
	if err := validatePassword(newPassword); err != nil {
		return err
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

// ChangeOwnPassword — self-service смена собственного пароля (§26). В
// отличие от ChangePassword (admin-only), требует подтверждения текущего
// пароля и сбрасывает флаг must_change_password. Доступна любой роли;
// menedzheru это единственный способ сменить пароль. Все активные сессии
// пользователя инвалидируются (forced re-login, §7.1).
func (u *AuthUsecase) ChangeOwnPassword(ctx context.Context, actor Actor, userID, currentPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	user, err := u.users.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return domain.ErrUnauthorized
	}
	return u.ChangePassword(ctx, actor, userID, newPassword, false)
}

func randomToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
