package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	users    port.UserRepo
	sessions port.SessionRepo
	teams    port.TeamRepo
	audit    *AuditUsecase
	// sessionTTL — провайдер актуальной длительности сессии (§34.2). Не
	// фиксированное значение: возвращает live-TTL, обновляемый из настроек без
	// рестарта (см. SessionTTLProvider). Новые сессии и sliding-Touch берут его.
	sessionTTL func() time.Duration
	logger     logging.Logger

	// Анти-брутфорс логина (Phase AUD.4): rl == nil или limit <= 0 —
	// проверка выключена (unit-тесты, отсутствие Redis).
	rl                 RateLimiter
	loginRateLimitPMin int

	// favorites — избранные команды пользователя (§49). nil — фича выключена
	// (unit-тесты без WithFavoriteTeams): чтение отдаёт пустой список.
	favorites port.FavoriteTeamRepo

	// searchHistory — история поиска узлов пользователя (§62). nil — фича
	// выключена (unit-тесты без WithSearchHistory): чтение отдаёт пустой
	// список, запись — no-op.
	searchHistory port.SearchHistoryRepo
}

func NewAuthUsecase(
	users port.UserRepo,
	sessions port.SessionRepo,
	teams port.TeamRepo,
	audit *AuditUsecase,
	sessionTTL func() time.Duration,
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

// WithFavoriteTeams включает избранные команды (§49). Builder-паттерн (как
// WithLoginRateLimit): существующие вызовы NewAuthUsecase не меняются.
func (u *AuthUsecase) WithFavoriteTeams(repo port.FavoriteTeamRepo) *AuthUsecase {
	u.favorites = repo
	return u
}

// maxFavoriteTeams — верхняя граница списка избранного (§49): защита от
// раздувания payload'а и позиций; членств столько на практике не бывает.
const maxFavoriteTeams = 100

// WithSearchHistory включает историю поиска узлов (§62). Builder-паттерн (как
// WithFavoriteTeams): существующие вызовы NewAuthUsecase не меняются.
func (u *AuthUsecase) WithSearchHistory(repo port.SearchHistoryRepo) *AuthUsecase {
	u.searchHistory = repo
	return u
}

// maxSearchHistory — сколько последних запросов хранить на пользователя (§62).
const maxSearchHistory = 10

// searchHistoryQueryBounds — границы длины сохраняемого запроса (§62, руны):
// короче нижней границы истории не даёт (кросс-командный поиск требует ≥ 2
// рун — см. NodeUsecase.SearchAcrossTeams), длиннее верхней — не сохраняем
// (обрезанная строка дала бы другой набор результатов; совпадает с CHECK
// миграции 0025).
const (
	minSearchQueryRunes = 2
	maxSearchQueryRunes = 200
)

// SearchHistory — последние сохранённые запросы пользователя от свежих к старым
// (§62). Никогда не возвращает ошибку: история — декорация к UI, при сбое (или
// невключённом репозитории) деградирует в пустой список.
func (u *AuthUsecase) SearchHistory(ctx context.Context, userID string) []string {
	if u.searchHistory == nil {
		return []string{}
	}
	items, err := u.searchHistory.ListSearchHistory(ctx, userID, maxSearchHistory)
	if err != nil {
		u.logger.Warn("list search history failed; returning empty",
			u.logger.Str("user_id", userID), u.logger.Err(err))
		return []string{}
	}
	if items == nil {
		items = []string{}
	}
	return items
}

// RecordSearch — сохранить строку поиска в историю пользователя (§62): trim,
// затем upsert с обрезкой до maxSearchHistory свежих. Мусор (пустая строка,
// короче minSearchQueryRunes, длиннее maxSearchQueryRunes) молча игнорируется —
// no-op, а не ошибка: вызывается из высокочастотных UI-триггеров, лишний шум
// клиенту не нужен. Репозиторий не включён → no-op.
func (u *AuthUsecase) RecordSearch(ctx context.Context, userID, query string) error {
	if u.searchHistory == nil {
		return nil
	}
	q := strings.TrimSpace(query)
	if n := utf8.RuneCountInString(q); n < minSearchQueryRunes || n > maxSearchQueryRunes {
		u.logger.Debug("record search: skip (length out of bounds)",
			u.logger.Str("user_id", userID), u.logger.Int("runes", utf8.RuneCountInString(q)))
		return nil
	}
	if err := u.searchHistory.SaveSearchQuery(ctx, userID, q, maxSearchHistory); err != nil {
		return fmt.Errorf("save search query: %w", err)
	}
	return nil
}

// ClearSearchHistory — удалить всю историю поиска пользователя (§62).
// Репозиторий не включён → no-op.
func (u *AuthUsecase) ClearSearchHistory(ctx context.Context, userID string) error {
	if u.searchHistory == nil {
		return nil
	}
	if err := u.searchHistory.ClearSearchHistory(ctx, userID); err != nil {
		return fmt.Errorf("clear search history: %w", err)
	}
	return nil
}

// FavoriteTeamIDs — id избранных команд пользователя в сохранённом порядке
// (§49). Никогда не возвращает ошибку: избранное — декорация к MyTeams, при
// сбое (или невключённом репозитории) деградирует в пустой список, а не
// валит весь ответ со списком членств.
func (u *AuthUsecase) FavoriteTeamIDs(ctx context.Context, userID string) []string {
	if u.favorites == nil {
		return []string{}
	}
	ids, err := u.favorites.ListFavoriteTeamIDs(ctx, userID)
	if err != nil {
		u.logger.Warn("list favorite teams failed; returning empty",
			u.logger.Str("user_id", userID), u.logger.Err(err))
		return []string{}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// SetFavoriteTeams — полная замена списка избранных команд пользователя (§49):
// добавление/удаление/переупорядочивание — один вызов, позиция = индексу.
// Валидация: лимит, отсутствие дубликатов, каждый id входит в членства
// (прецедент §45 SetDefaultTeam) — иначе ErrUserNotTeamMember. Пустой список
// допустим (очистить избранное).
func (u *AuthUsecase) SetFavoriteTeams(ctx context.Context, actor Actor, userID string, teamIDs []string) error {
	if u.favorites == nil {
		return fmt.Errorf("favorite teams: %w", domain.ErrNotFound)
	}
	if len(teamIDs) > maxFavoriteTeams {
		return domain.ErrFavoriteTeamsInvalid
	}
	seen := make(map[string]struct{}, len(teamIDs))
	for _, id := range teamIDs {
		if _, dup := seen[id]; dup {
			return domain.ErrFavoriteTeamsInvalid
		}
		seen[id] = struct{}{}
	}
	memberships, err := u.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return fmt.Errorf("list memberships: %w", err)
	}
	for _, id := range teamIDs {
		if !teamInMemberships(id, memberships) {
			return domain.ErrUserNotTeamMember
		}
	}
	if err := u.favorites.ReplaceFavoriteTeams(ctx, userID, teamIDs); err != nil {
		return fmt.Errorf("replace favorite teams: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionUserFavoriteTeams, "user", userID, map[string]any{
		"team_ids": teamIDs, "count": len(teamIDs),
	})
	return nil
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
		CurrentTeamID:      u.resolveLoginTeam(ctx, user),
		MustChangePassword: user.MustChangePassword,
		CreatedAt:          now,
		LastSeenAt:         now,
	}
	if err := u.sessions.Create(ctx, s, u.sessionTTL()); err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}
	_ = u.users.UpdateLastLogin(ctx, user.ID, now)
	u.audit.Log(ctx, Actor{UserID: user.ID, UserLogin: user.Login, IPAddress: ip},
		domain.ActionUserLogin, "user", user.ID, nil)
	return token, user, nil
}

// resolveLoginTeam определяет current_team_id для новой сессии (§44.H):
// команда из user.DefaultTeamID, ЕСЛИ пользователь в ней реально состоит;
// иначе — первая из его членств (детерминированно: ListUserTeams отдаёт
// ORDER BY slug); если членств нет вовсе — оставляем DefaultTeamID как было.
//
// Чинит ситуацию, когда default_team_id указывает на команду, из которой
// пользователя убрали (членство удалили, колонку не переписали): без этой
// проверки сессия скоупилась на чужую команду, а переключатель был заблокирован
// (одна доступная команда ≠ current_team), и до своих узлов было не добраться.
// Ошибку доступа к членствам НЕ эскалируем — логин не блокируем.
func (u *AuthUsecase) resolveLoginTeam(ctx context.Context, user *domain.User) string {
	memberships, err := u.teams.ListUserTeams(ctx, user.ID)
	if err != nil {
		u.logger.Warn("resolve login team: list memberships failed; using default_team_id",
			u.logger.Str("user_id", user.ID), u.logger.Err(err))
		return user.DefaultTeamID
	}
	if len(memberships) == 0 || teamInMemberships(user.DefaultTeamID, memberships) {
		return user.DefaultTeamID
	}
	u.logger.Warn("login: default_team_id is not among memberships; using first membership",
		u.logger.Str("user_id", user.ID),
		u.logger.Str("default_team_id", user.DefaultTeamID),
		u.logger.Str("resolved_team_id", memberships[0].Team.ID))
	return memberships[0].Team.ID
}

// teamInMemberships — true, если teamID присутствует среди членств.
func teamInMemberships(teamID string, memberships []*domain.UserTeam) bool {
	for _, m := range memberships {
		if m.Team.ID == teamID {
			return true
		}
	}
	return false
}

// Logout удаляет сессию.
func (u *AuthUsecase) Logout(ctx context.Context, token string) error {
	return u.sessions.Delete(ctx, token)
}

// Check валидирует session-token; продлевает TTL и обновляет LastSeenAt
// (Phase AUD.5 — иначе время последней активности замораживалось на логине).
func (u *AuthUsecase) Check(ctx context.Context, token string) (*domain.Session, error) {
	s, err := u.sessions.Get(ctx, token)
	if err != nil {
		return nil, err
	}
	s.LastSeenAt = time.Now().UTC()
	_ = u.sessions.Touch(ctx, s, u.sessionTTL())
	return s, nil
}

// Me возвращает текущего пользователя по идентификатору сессии. Нужен
// для /api/auth/me, чтобы вернуть login/email — Session хранит только
// user_id, role, lang.
func (u *AuthUsecase) Me(ctx context.Context, userID string) (*domain.User, error) {
	return u.users.Get(ctx, userID)
}

// MyTeamsAndCurrent — членства пользователя + актуальный current_team_id с
// САМОЛЕЧЕНИЕМ сессии (§44.H). Если current_team из сессии не входит в членства
// (сессия выдана до фикса, либо пользователя убрали из команды), а членства
// есть — переключаем на первую доступную команду и (для cookie-сессий,
// token != "") персистим: «битая» сессия чинится без ре-логина. Псевдо-сессии
// API-токена (token == "") имеют фиксированную команду — не трогаем. Возвращает
// (членства, актуальный current_team_id).
// Возвращает (членства, current_team_id, healed) — healed=true, если сессия
// была переключена (UI по нему инвалидирует team-scoped кеш, чтобы дашборд
// сразу показал ноды верной команды).
func (u *AuthUsecase) MyTeamsAndCurrent(ctx context.Context, s *domain.Session) ([]*domain.UserTeam, string, bool, error) {
	memberships, err := u.teams.ListUserTeams(ctx, s.UserID)
	if err != nil {
		return nil, "", false, fmt.Errorf("list memberships: %w", err)
	}
	current := s.CurrentTeamID
	healed := false
	if s.Token != "" && len(memberships) > 0 && !teamInMemberships(current, memberships) {
		current = memberships[0].Team.ID
		s.CurrentTeamID = current
		healed = true
		if err := u.sessions.Create(ctx, s, u.sessionTTL()); err != nil {
			// Не валим запрос: вернём исправленный current — UI покажет верную
			// команду, сессия дочинится при следующем заходе.
			u.logger.Warn("heal session current_team failed",
				u.logger.Str("user_id", s.UserID), u.logger.Err(err))
		}
	}
	return memberships, current, healed, nil
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
	if err := u.sessions.Create(ctx, s, u.sessionTTL()); err != nil {
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
