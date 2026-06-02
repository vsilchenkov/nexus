package usecase

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
	"nexus/internal/web/usecase/port"
)

// ReloadPublisher — interface, который реализует reloader.Publisher.
// Объявлен здесь, чтобы usecase не зависел от конкретного pub/sub-механизма
// (см. §17.2 — accept interfaces).
type ReloadPublisher interface {
	Publish(ctx context.Context, section reloader.Section) error
}

// AppSettingsUsecase инкапсулирует CRUD над singleton-конфигом §14.5.
//
// При Update пишет audit-запись с маскированием DSN и пароля
// (в `details` уходят имена изменённых полей, но не их значения, кроме
// безопасных вроде environment/level), и публикует событие reload в
// Redis pub/sub для горячей перезагрузки на всех инстансах.
type AppSettingsUsecase struct {
	repo      port.AppSettingsRepo
	audit     *AuditUsecase
	publisher ReloadPublisher
	logger    logging.Logger
}

func NewAppSettingsUsecase(repo port.AppSettingsRepo, audit *AuditUsecase, publisher ReloadPublisher, logger logging.Logger) *AppSettingsUsecase {
	return &AppSettingsUsecase{repo: repo, audit: audit, publisher: publisher, logger: logger}
}

// Get возвращает текущие настройки + флаги «значение задано».
// DSN и password в Get() возвращаются маскированными: оператор не должен
// видеть полный секрет, даже если у него есть права admin.
func (u *AppSettingsUsecase) Get(ctx context.Context) (*domain.AppSettings, error) {
	s, err := u.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if s.Sentry.DSN != nil && *s.Sentry.DSN != "" {
		masked := maskSecret(*s.Sentry.DSN)
		s.Sentry.DSN = &masked
	}
	if s.ClickHouse.Password != nil && *s.ClickHouse.Password != "" {
		masked := "***"
		s.ClickHouse.Password = &masked
	}
	if s.Notifications.Telegram.BotToken != nil && *s.Notifications.Telegram.BotToken != "" {
		masked := "***"
		s.Notifications.Telegram.BotToken = &masked
	}
	return s, nil
}

// Raw возвращает текущие настройки БЕЗ маскирования — для overlay'я
// поверх env-конфига на старте сервиса. Вызывается только из bootstrap.
func (u *AppSettingsUsecase) Raw(ctx context.Context) (*domain.AppSettings, error) {
	return u.repo.Get(ctx)
}

// Update сохраняет новые настройки и пишет audit.
// Если в патче DSN/password равны "***" — значение НЕ перезаписывается
// (оператор отредактировал только не-секретное поле).
func (u *AppSettingsUsecase) Update(ctx context.Context, actor Actor, patch *domain.AppSettings) error {
	if patch == nil {
		return fmt.Errorf("patch is nil")
	}

	if err := validateTelegramPatch(patch); err != nil {
		return err
	}
	if patch.General.PublicBaseURL != nil {
		if err := domain.ValidatePublicBaseURL(*patch.General.PublicBaseURL); err != nil {
			return err
		}
	}

	current, err := u.repo.Get(ctx)
	if err != nil {
		return err
	}

	merged := mergeAppSettings(current, patch)
	merged.UpdatedBy = actor.UserID

	if err := u.repo.Update(ctx, merged); err != nil {
		return err
	}

	sections := changedSections(patch)
	u.audit.Log(ctx, actor, domain.ActionAppSettingsUpdate, "app_settings", "1", map[string]any{
		"changed_sections": sections,
	})

	// Hot-reload: для каждой изменённой секции публикуем событие в Redis.
	// Подписчики (Receiver/Sender/Web) переинициализируют Sentry и/или CH.
	if u.publisher != nil {
		for _, s := range sections {
			if err := u.publisher.Publish(ctx, reloader.Section(s)); err != nil {
				u.logger.Warn("reload publish failed",
					u.logger.Str("section", s),
					u.logger.Err(err))
			}
		}
	}
	return nil
}

// mergeAppSettings накладывает patch поверх current: nil-поля в patch
// сохраняются из current, не-nil — переопределяют. Маскированные значения
// ("***", "https://***@...") НЕ перезаписывают current.
func mergeAppSettings(current, patch *domain.AppSettings) *domain.AppSettings {
	out := *current

	// General (§28). PublicBaseURL не секрет — перезаписываем как есть.
	if patch.General.PublicBaseURL != nil {
		out.General.PublicBaseURL = patch.General.PublicBaseURL
	}

	// Sentry
	if patch.Sentry.Use != nil {
		out.Sentry.Use = patch.Sentry.Use
	}
	if patch.Sentry.DSN != nil && !isMaskedSecret(*patch.Sentry.DSN) {
		out.Sentry.DSN = patch.Sentry.DSN
	}
	if patch.Sentry.Environment != nil {
		out.Sentry.Environment = patch.Sentry.Environment
	}
	if patch.Sentry.Level != nil {
		out.Sentry.Level = patch.Sentry.Level
	}
	if patch.Sentry.AttachStacktrace != nil {
		out.Sentry.AttachStacktrace = patch.Sentry.AttachStacktrace
	}
	if patch.Sentry.EnableTracing != nil {
		out.Sentry.EnableTracing = patch.Sentry.EnableTracing
	}
	if patch.Sentry.TracesSampleRate != nil {
		out.Sentry.TracesSampleRate = patch.Sentry.TracesSampleRate
	}

	// ClickHouse
	if patch.ClickHouse.Host != nil {
		out.ClickHouse.Host = patch.ClickHouse.Host
	}
	if patch.ClickHouse.Port != nil {
		out.ClickHouse.Port = patch.ClickHouse.Port
	}
	if patch.ClickHouse.Database != nil {
		out.ClickHouse.Database = patch.ClickHouse.Database
	}
	if patch.ClickHouse.User != nil {
		out.ClickHouse.User = patch.ClickHouse.User
	}
	if patch.ClickHouse.Password != nil && *patch.ClickHouse.Password != "***" {
		out.ClickHouse.Password = patch.ClickHouse.Password
	}
	if patch.ClickHouse.BatchSize != nil {
		out.ClickHouse.BatchSize = patch.ClickHouse.BatchSize
	}
	if patch.ClickHouse.FlushIntervalSec != nil {
		out.ClickHouse.FlushIntervalSec = patch.ClickHouse.FlushIntervalSec
	}
	if patch.ClickHouse.BufferMaxSize != nil {
		out.ClickHouse.BufferMaxSize = patch.ClickHouse.BufferMaxSize
	}
	if patch.ClickHouse.Workers != nil {
		out.ClickHouse.Workers = patch.ClickHouse.Workers
	}

	// Notifications → Telegram (§20). bot_token не перезаписываем маской.
	tg := patch.Notifications.Telegram
	if tg.Enabled != nil {
		out.Notifications.Telegram.Enabled = tg.Enabled
	}
	if tg.ChatID != nil {
		out.Notifications.Telegram.ChatID = tg.ChatID
	}
	if tg.BotToken != nil && *tg.BotToken != "***" {
		out.Notifications.Telegram.BotToken = tg.BotToken
	}
	if tg.Cron != nil {
		out.Notifications.Telegram.Cron = tg.Cron
	}
	return &out
}

// validateTelegramPatch проверяет cron-выражение в патче (§20.2). Пустой
// cron или nil — пропускается (валидация на этапе включения уведомлений).
func validateTelegramPatch(p *domain.AppSettings) error {
	c := p.Notifications.Telegram.Cron
	if c == nil || *c == "" {
		return nil
	}
	if _, err := cron.ParseStandard(*c); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrTelegramCronInvalid, err)
	}
	return nil
}

// changedSections — список секций (sentry/clickhouse), в которых патч
// содержит хотя бы одно не-nil поле. Используется в audit details.
func changedSections(p *domain.AppSettings) []string {
	var out []string
	if p.General.PublicBaseURL != nil {
		out = append(out, "general")
	}
	s := p.Sentry
	if s.Use != nil || s.DSN != nil || s.Environment != nil || s.Level != nil ||
		s.AttachStacktrace != nil || s.EnableTracing != nil || s.TracesSampleRate != nil {
		out = append(out, "sentry")
	}
	c := p.ClickHouse
	if c.Host != nil || c.Port != nil || c.Database != nil || c.User != nil || c.Password != nil ||
		c.BatchSize != nil || c.FlushIntervalSec != nil || c.BufferMaxSize != nil || c.Workers != nil {
		out = append(out, "clickhouse")
	}
	tg := p.Notifications.Telegram
	if tg.Enabled != nil || tg.ChatID != nil || tg.BotToken != nil || tg.Cron != nil {
		out = append(out, "notifications")
	}
	return out
}

// maskSecret возвращает строку с серединой, заменённой на ***. Сохраняет
// первые/последние 4 символа, если строка длиннее 8.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

// isMaskedSecret — true если строка похожа на возвращённую maskSecret.
// Используется в merge: маскированный DSN не должен перезаписать настоящий.
func isMaskedSecret(s string) bool {
	if s == "***" {
		return true
	}
	if len(s) >= 11 && s[4:7] == "***" {
		return true
	}
	return false
}
