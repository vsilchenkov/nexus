package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/mail"
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// maxActivePasswordResets — предел одновременно действующих ссылок на
// пользователя (§88.4.1). Превышение не создаёт токен и не шлёт письмо, но
// ответ клиенту не меняется.
const maxActivePasswordResets = 3

// Исходы запроса восстановления (§88.9). Наружу все они дают ОДИН ответ;
// различие видно только в аудите и служебном логе.
const (
	resetResultSent           = "sent"
	resetResultUserNotFound   = "user_not_found"
	resetResultNoEmail        = "no_email"
	resetResultInactive       = "inactive"
	resetResultEmailAmbiguous = "email_ambiguous"
	resetResultTooManyActive  = "too_many_active"
	resetResultMailDisabled   = "mail_disabled"
	resetResultSendFailed     = "send_failed"
)

// PasswordChanger — смена пароля пользователя. Интерфейс на стороне
// потребителя: реализует его *AuthUsecase, но тянуть в тесты сессии и команды
// ради восстановления пароля не нужно.
type PasswordChanger interface {
	ChangePassword(ctx context.Context, actor Actor, userID, newPassword string, mustChange bool) error
}

// PasswordResetMetrics — учёт исходов (§88.5). Реализует metrics.Metrics;
// nil-реализация допустима (unit-тесты).
type PasswordResetMetrics interface {
	IncPasswordResetRequest(result string)
}

// PasswordResetUsecase — самостоятельное восстановление пароля (§88.4).
//
// Ключевое свойство: наружу все исходы запроса неразличимы. Публичная форма не
// должна быть справочником существующих логинов и почтовых адресов, поэтому
// решение «письмо не уходит» и «письмо ушло» дают один ответ, а причина
// пишется в аудит.
type PasswordResetUsecase struct {
	users     port.UserRepo
	tokens    port.OneTimeTokenRepo
	settings  appSettingsReader
	mail      MailSender
	templates *mail.Templates
	changer   PasswordChanger
	audit     *AuditUsecase
	clock     clock.Clock
	logger    logging.Logger

	appTitle   string
	instanceID string

	metrics PasswordResetMetrics
	// runner запускает фоновую отправку. Опция, а не голый `go`: unit-тесты
	// подменяют её синхронной, иначе они флейкуют и спорят с goleak.
	runner func(func())
}

// PasswordResetOption — функциональная опция конструктора.
type PasswordResetOption func(*PasswordResetUsecase)

// WithPasswordResetClock подменяет часы (тесты).
func WithPasswordResetClock(c clock.Clock) PasswordResetOption {
	return func(u *PasswordResetUsecase) { u.clock = c }
}

// WithPasswordResetRunner подменяет способ запуска фоновой отправки.
func WithPasswordResetRunner(run func(func())) PasswordResetOption {
	return func(u *PasswordResetUsecase) { u.runner = run }
}

// WithPasswordResetMetrics включает учёт исходов.
func WithPasswordResetMetrics(m PasswordResetMetrics) PasswordResetOption {
	return func(u *PasswordResetUsecase) { u.metrics = m }
}

func NewPasswordResetUsecase(
	users port.UserRepo,
	tokens port.OneTimeTokenRepo,
	settings appSettingsReader,
	sender MailSender,
	templates *mail.Templates,
	changer PasswordChanger,
	audit *AuditUsecase,
	appTitle, instanceID string,
	logger logging.Logger,
	opts ...PasswordResetOption,
) *PasswordResetUsecase {
	u := &PasswordResetUsecase{
		users:      users,
		tokens:     tokens,
		settings:   settings,
		mail:       sender,
		templates:  templates,
		changer:    changer,
		audit:      audit,
		clock:      clock.System(),
		logger:     logger,
		appTitle:   appTitle,
		instanceID: instanceID,
		runner:     func(fn func()) { go fn() },
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Request принимает логин ИЛИ email и, если всё сложилось, отправляет письмо
// со ссылкой (§88.4.3).
//
// Возвращает срок жизни ссылки в минутах — его показывает интерфейс («ссылка
// действует N минут»); значение одинаково при любом исходе и ничего не
// раскрывает. Ошибка возвращается ТОЛЬКО на инфраструктурном сбое: «письмо не
// ушло» ошибкой не считается.
func (u *PasswordResetUsecase) Request(ctx context.Context, input, ip string) (ttlMinutes int, err error) {
	settings, err := u.settings.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("read mail settings: %w", err)
	}
	m := settings.Mail.Resolve()
	ttlMinutes = m.PasswordResetTTLMin

	input = strings.TrimSpace(input)
	actor := Actor{UserLogin: input, IPAddress: ip}

	if !domain.MailPasswordResetReady(settings) {
		u.finish(ctx, actor, "", resetResultMailDisabled, nil)
		return ttlMinutes, nil
	}

	user, result := u.resolveUser(ctx, input)
	if result != "" {
		u.finish(ctx, actor, "", result, nil)
		return ttlMinutes, nil
	}
	actor.UserID = user.ID

	now := u.clock.Now()
	active, err := u.tokens.CountActive(ctx, domain.TokenPurposePasswordReset, user.ID, now)
	if err != nil {
		return 0, fmt.Errorf("count active reset links: %w", err)
	}
	if active >= maxActivePasswordResets {
		u.finish(ctx, actor, user.ID, resetResultTooManyActive, nil)
		return ttlMinutes, nil
	}

	token, err := randomToken()
	if err != nil {
		return 0, fmt.Errorf("generate reset token: %w", err)
	}
	expiresAt := now.Add(time.Duration(m.PasswordResetTTLMin) * time.Minute)

	// Токен пишется ДО отправки: письмо, доехавшее раньше записи, дало бы
	// мёртвую ссылку.
	if err := u.tokens.Create(ctx, &domain.OneTimeToken{
		UserID:    user.ID,
		Purpose:   domain.TokenPurposePasswordReset,
		TokenHash: hashToken(token),
		ExpiresAt: expiresAt,
		RequestIP: ip,
	}); err != nil {
		return 0, fmt.Errorf("create reset link: %w", err)
	}

	// Ленивая чистка (§88.4.1): отдельный воркер ради этого не заводится,
	// хвост 7 дней оставлен для расследований. Ошибка не важна — операция
	// пользователя от неё не зависит.
	if _, err := u.tokens.DeleteExpiredBefore(ctx, now.Add(-7*24*time.Hour)); err != nil {
		u.logger.Debug("password reset: cleanup failed", u.logger.Err(err))
	}

	// Публичный адрес берём из УЖЕ прочитанных настроек: второе чтение той же
	// строки БД дало бы лишний запрос и окно рассинхрона.
	u.sendAsync(ctx, actor, user, token, m, derefStr(settings.General.PublicBaseURL), expiresAt, ip)
	return ttlMinutes, nil
}

// resolveUser ищет пользователя по логину, затем по email. Возвращает либо
// пользователя, либо код исхода — тот уйдёт в аудит.
func (u *PasswordResetUsecase) resolveUser(ctx context.Context, input string) (*domain.User, string) {
	if input == "" {
		return nil, resetResultUserNotFound
	}
	user, err := u.users.GetByLogin(ctx, input)
	if err != nil {
		if !errors.Is(err, domain.ErrUserNotFound) && !errors.Is(err, domain.ErrNotFound) {
			u.logger.Warn("password reset: lookup by login failed", u.logger.Err(err))
		}
		user, err = u.users.GetByEmail(ctx, input)
		switch {
		case errors.Is(err, domain.ErrUserEmailAmbiguous):
			return nil, resetResultEmailAmbiguous
		case err != nil:
			return nil, resetResultUserNotFound
		}
	}
	if !user.Active {
		return nil, resetResultInactive
	}
	if strings.TrimSpace(user.Email) == "" {
		return nil, resetResultNoEmail
	}
	return user, ""
}

// sendAsync отправляет письмо в фоне (§88.5).
//
// Синхронная отправка сделала бы время ответа оракулом существования учётной
// записи (SMTP-диалог — секунды) и превратила публичный неаутентифицированный
// эндпоинт в усилитель отказа в обслуживании.
func (u *PasswordResetUsecase) sendAsync(
	ctx context.Context, actor Actor, user *domain.User, token string,
	m domain.ResolvedMail, publicBaseURL string, expiresAt time.Time, ip string,
) {
	base := strings.TrimSuffix(publicBaseURL, "/")
	msg, err := u.templates.PasswordReset(string(user.Lang), user.Email, mail.PasswordResetData{
		UserName:         user.DisplayName(),
		ResetURL:         base + "/reset-password?token=" + url.QueryEscape(token),
		ExpiresInMinutes: m.PasswordResetTTLMin,
		ExpiresAtUTC:     expiresAt.UTC().Format("2006-01-02 15:04 UTC"),
		RequestIP:        ip,
		InstanceID:       u.instanceID,
		AppTitle:         u.appTitle,
	})
	if err != nil {
		u.finish(ctx, actor, user.ID, resetResultSendFailed, err)
		return
	}

	cfg := mailConfigFrom(m)
	// context.WithoutCancel, а не Background: сохраняются trace-значения и
	// Sentry-hub запроса (CLAUDE.md §6).
	sendCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), cfg.Timeout+5*time.Second)

	u.runner(func() {
		defer cancel()
		defer safego.Recover(u.logger, "auth.password_reset_send")

		if err := u.mail.Send(sendCtx, cfg, msg); err != nil {
			u.finish(sendCtx, actor, user.ID, resetResultSendFailed, err)
			return
		}
		u.finish(sendCtx, actor, user.ID, resetResultSent, nil)
	})
}

// finish пишет аудит, метрику и служебный лог одного исхода.
func (u *PasswordResetUsecase) finish(ctx context.Context, actor Actor, userID, result string, cause error) {
	details := map[string]any{"input": actor.UserLogin, "result": result}
	if cause != nil {
		details["error"] = cause.Error()
	}
	u.audit.Log(ctx, actor, domain.ActionUserPasswordResetRequest, "user", userID, details)

	if u.metrics != nil {
		u.metrics.IncPasswordResetRequest(result)
	}

	switch result {
	case resetResultSent:
		u.logger.Debug("password reset: link sent", u.logger.Str("user_id", userID))
	case resetResultSendFailed:
		// Уровень error намеренно: пользователь о сбое не узнает (ответ
		// одинаков), и это единственный сигнал администратору — плюс порог
		// Sentry начинается с error.
		u.logger.ErrorWithOp("password reset: send failed", cause, "auth.password_reset_send",
			u.logger.Str("user_id", userID))
	default:
		// §51.9: тихая ветка. Без этой строки «почему письмо не пришло»
		// выясняется только через аудит.
		u.logger.Debug("password reset: skipped",
			u.logger.Str("result", result),
			u.logger.Str("ip", actor.IPAddress))
	}
}

// Validate проверяет ссылку, НЕ расходуя её (§88.4.6).
func (u *PasswordResetUsecase) Validate(ctx context.Context, token string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	ok, err := u.tokens.Peek(ctx, domain.TokenPurposePasswordReset, hashToken(token), u.clock.Now())
	if err != nil {
		return false, fmt.Errorf("validate reset link: %w", err)
	}
	return ok, nil
}

// Confirm гасит ссылку и меняет пароль (§88.7).
//
// Порядок операций значим: политика пароля проверяется ДО гашения токена —
// иначе опечатка в длине убивала бы ссылку безвозвратно.
func (u *PasswordResetUsecase) Confirm(ctx context.Context, token, newPassword, ip string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return domain.ErrOneTimeTokenInvalid
	}

	rec, err := u.tokens.Consume(ctx, domain.TokenPurposePasswordReset, hashToken(token), u.clock.Now())
	if err != nil {
		return err
	}

	user, err := u.users.Get(ctx, rec.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	// Проверка обязана быть И здесь, а не только при запросе: между письмом и
	// переходом проходит до суток, за которые администратор мог отключить
	// учётную запись. Токен уже погашен — валидной ссылки не остаётся.
	if !user.Active {
		return domain.ErrUserInactive
	}

	actor := Actor{UserID: user.ID, UserLogin: user.DisplayName(), IPAddress: ip}
	// mustChange=false: человек только что задал пароль сам. Прочие выданные
	// ссылки гасит сам ChangePassword (§88.7) — отдельного шага не нужно.
	if err := u.changer.ChangePassword(ctx, actor, user.ID, newPassword, false); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionUserPasswordResetConfirm, "user", user.ID,
		map[string]any{"via": "email"})
	return nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
