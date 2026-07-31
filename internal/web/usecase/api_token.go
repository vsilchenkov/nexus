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
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// APITokenPrefix — общий префикс для всех токенов шины (§7.14):
// позволяет middleware отличать API-токен от прокси-Bearer'ов.
const APITokenPrefix = "db_"

const tokenRandomBytes = 32

// teamMembershipLister — членства пользователя. Узкий интерфейс на стороне
// консьюмера (ISP): из TeamRepo здесь нужен ровно один метод, и стабы
// существующих тестов от этого не ломаются (тот же приём, что у §49
// FavoriteTeamRepo).
type teamMembershipLister interface {
	ListUserTeams(ctx context.Context, userID string) ([]*domain.UserTeam, error)
}

// APITokenUsecase — CRUD + проверка токенов.
type APITokenUsecase struct {
	repo   port.APITokenRepo
	audit  *AuditUsecase
	users  port.UserRepo
	teams  teamMembershipLister
	logger logging.Logger
}

func NewAPITokenUsecase(
	repo port.APITokenRepo,
	users port.UserRepo,
	teams teamMembershipLister,
	audit *AuditUsecase,
	logger logging.Logger,
) *APITokenUsecase {
	return &APITokenUsecase{repo: repo, users: users, teams: teams, audit: audit, logger: logger}
}

// CreatedToken — то, что возвращается при создании. Поле PlainToken
// отдаётся клиенту ОДИН РАЗ (§7.14: «показывается ровно один раз»).
type CreatedToken struct {
	Token *domain.APIToken
	Plain string
}

// Create генерирует токен и сохраняет SHA-256(token) в БД.
// teamID — UUID команды, к которой будет привязан токен (multi-tenancy v2,
// миграция 0008); выбирается в форме создания. Пустая строка — fallback на
// default-team через подзапрос в api_token_repo.Create.
//
// Членство в команде проверяется здесь: teamID приходит от клиента, и без
// проверки любой пользователь выписал бы себе токен в чужую команду — обход
// изоляции §18. Образец — AuthUsecase.SwitchTeam.
//
// expiresInDays — срок жизни в днях от «сейчас»; nil = бессрочный. Дни, а не
// готовая дата: считать срок должен сервер по своим часам, иначе кривые часы
// клиента дают кривой срок. Раньше UI слал expires_in_days, а handler принимал
// expires_at — поле молча терялось, и ВСЕ выданные токены были бессрочными
// вопреки выбранному в интерфейсе сроку.
func (u *APITokenUsecase) Create(
	ctx context.Context,
	actor Actor,
	userID, teamID, name string,
	scopes []string,
	expiresInDays *int,
) (*CreatedToken, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := u.ensureMembership(ctx, userID, teamID); err != nil {
		return nil, err
	}
	var expiresAt *time.Time
	if expiresInDays != nil {
		if *expiresInDays <= 0 {
			return nil, errors.New("expires_in_days must be positive")
		}
		exp := time.Now().Add(time.Duration(*expiresInDays) * 24 * time.Hour)
		expiresAt = &exp
	}
	plain, err := generateToken()
	if err != nil {
		return nil, err
	}
	hash := hashToken(plain)
	t := &domain.APIToken{
		UserID:    userID,
		TeamID:    teamID,
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

// ensureMembership — пользователь состоит в команде, для которой выписывается
// токен. Пустой teamID пропускаем: это старый контракт (репозиторий подставит
// default-team), клиент команду не выбирал.
func (u *APITokenUsecase) ensureMembership(ctx context.Context, userID, teamID string) error {
	if teamID == "" || u.teams == nil {
		return nil
	}
	memberships, err := u.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return fmt.Errorf("list memberships: %w", err)
	}
	for _, ut := range memberships {
		if ut.Team.ID == teamID {
			return nil
		}
	}
	return domain.ErrPermissionDenied
}

// ListByUser — все токены пользователя (по всем командам): «Настройки» вне
// скоупа команды; команда каждого токена видна в ответе (TeamID).
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

// Rotate перевыпускает ЗНАЧЕНИЕ существующего токена: генерирует новый секрет,
// обновляет hash/prefix, сохраняя id/name/scopes/team/expires. Старое значение
// сразу перестаёт авторизовывать (hash изменился). Отозванный/просроченный токен
// не ротируется (repo вернёт ErrNotFound) — для него нужно создать новый. Как и
// Create, plaintext возвращается ровно один раз (§7.14).
func (u *APITokenUsecase) Rotate(ctx context.Context, actor Actor, id, userID string) (string, error) {
	plain, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := u.repo.Rotate(ctx, id, userID, hashToken(plain), plain[:min(8, len(plain))]); err != nil {
		return "", err
	}
	u.audit.Log(ctx, actor, domain.ActionAPITokenRotate, "api_token", id, nil)
	return plain, nil
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
	// Best-effort last_used_at; не блокирует ответ. WithoutCancel, а не
	// Background: запись переживает завершение запроса, но сохраняет его
	// значения — request-id в логах и Sentry-hub (§6 CLAUDE.md).
	touchCtx := context.WithoutCancel(ctx)
	go func() {
		defer safego.Recover(u.logger, "web.tokenTouchLastUsed")
		_ = u.repo.TouchLastUsed(touchCtx, t.ID)
	}()
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
