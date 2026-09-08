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
	// allowVersionOverride — гейт §34.3: при false запись general.version_override
	// отклоняется (прод). Значение из cfg.Web.AllowVersionOverride.
	allowVersionOverride bool
	// maxBodyBytesCap / maxAsyncBodyBytesCap — ПОТОЛКИ рабочих лимитов тела
	// (§97) из конфига (receiver.max_body_bytes / max_async_body_bytes). Выше
	// потолка администратор задать лимит не может: под потолок настроены
	// инфраструктура (nginx, gRPC, Kafka) и память сервисов. 0 = потолок не
	// задан, проверяется только нижняя граница.
	maxBodyBytesCap      int
	maxAsyncBodyBytesCap int
	logger               logging.Logger
}

// NewAppSettingsUsecase создаёт usecase настроек. maxBodyBytesCap и
// maxAsyncBodyBytesCap — потолки рабочих лимитов тела из конфига (§97);
// 0 означает «потолок не задан».
func NewAppSettingsUsecase(repo port.AppSettingsRepo, audit *AuditUsecase, publisher ReloadPublisher, allowVersionOverride bool, maxBodyBytesCap, maxAsyncBodyBytesCap int, logger logging.Logger) *AppSettingsUsecase {
	return &AppSettingsUsecase{
		repo:                 repo,
		audit:                audit,
		publisher:            publisher,
		allowVersionOverride: allowVersionOverride,
		maxBodyBytesCap:      maxBodyBytesCap,
		maxAsyncBodyBytesCap: maxAsyncBodyBytesCap,
		logger:               logger,
	}
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
	// §88: пароль SMTP наружу не выходит даже админу. В БД он, как DSN Sentry,
	// пароль ClickHouse и токен бота, лежит зашифрованным (§90.1) — репозиторий
	// отдаёт сюда уже расшифрованное значение.
	if s.Mail.Password != nil && *s.Mail.Password != "" {
		masked := "***"
		s.Mail.Password = &masked
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
	// §44.C: интервал автообновления метрик в допустимом диапазоне.
	if patch.General.MetricsRefetchMs != nil {
		if err := domain.ValidateMetricsRefetchMs(*patch.General.MetricsRefetchMs); err != nil {
			return err
		}
	}
	// §94.5: срок хранения журнала отказов (0 = сбор выключен).
	if patch.General.RejectedRetentionDays != nil {
		if err := domain.ValidateRejectedRetentionDays(*patch.General.RejectedRetentionDays); err != nil {
			return err
		}
	}
	// §97: рабочие лимиты тела — в пределах потолка из конфига.
	if patch.General.MaxBodyBytes != nil {
		if err := domain.ValidateMaxBodyBytes(*patch.General.MaxBodyBytes, u.maxBodyBytesCap); err != nil {
			return err
		}
	}
	if patch.General.MaxAsyncBodyBytes != nil {
		if err := domain.ValidateMaxAsyncBodyBytes(*patch.General.MaxAsyncBodyBytes, u.maxAsyncBodyBytesCap); err != nil {
			return err
		}
	}
	// §98.5: глобальная политика защиты узла — те же границы, что у полей узла.
	if patch.General.CircuitBreakerThreshold != nil {
		if err := domain.ValidateBreakerThreshold(*patch.General.CircuitBreakerThreshold); err != nil {
			return err
		}
	}
	if patch.General.CircuitBreakerCooldownSec != nil {
		if err := domain.ValidateBreakerCooldownSec(*patch.General.CircuitBreakerCooldownSec); err != nil {
			return err
		}
	}
	// §34.3: override версии разрешён только в dev (web.allow_version_override).
	if patch.General.VersionOverride != nil && !u.allowVersionOverride {
		return domain.ErrVersionOverrideForbidden
	}
	// §34.2: длительность сессии в допустимом диапазоне.
	if patch.Security.SessionTTLSeconds != nil {
		if err := domain.ValidateSessionTTLSeconds(*patch.Security.SessionTTLSeconds); err != nil {
			return err
		}
	}
	// §51: уровень логирования сервисов в допустимом диапазоне (2..5).
	if patch.Logging.Level != nil {
		if err := domain.ValidateLogLevel(*patch.Logging.Level); err != nil {
			return err
		}
	}
	// §88: заданные поля секции mail — по отдельности.
	if err := domain.ValidateMailSettings(patch.Mail); err != nil {
		return err
	}

	current, err := u.repo.Get(ctx)
	if err != nil {
		return err
	}

	merged := mergeAppSettings(current, patch)
	// §88: межполевые правила почты проверяются по СМЕРЖЕННОЙ секции — на
	// патче их проверить нельзя, там половины значений просто нет (например,
	// приходит один флаг enabled, а хост уже сохранён ранее).
	if err := domain.ValidateMailConsistency(merged.Mail); err != nil {
		return err
	}
	// §97: async-лимит не может быть выше sync — по той же причине, что и у
	// почты, проверяем по СМЕРЖЕННОМУ объекту: патч может нести только одно из
	// двух полей, а второе уже сохранено ранее.
	if domain.BodyLimitOrDefault(merged.General.MaxAsyncBodyBytes) > domain.BodyLimitOrDefault(merged.General.MaxBodyBytes) {
		return domain.ErrAsyncBodyLimitOverSync
	}
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
				continue
			}
			// §51.9: успешный publish раньше молчал — «настройка сохранилась,
			// но сервисы не перечитали» было видно только по audit-записи.
			u.logger.Debug("app_settings: reload published",
				u.logger.Str("section", s),
				u.logger.Str("actor", actor.UserID))
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
	// §34.3: version_override (гейт проверен в Update до merge).
	if patch.General.VersionOverride != nil {
		out.General.VersionOverride = patch.General.VersionOverride
	}
	// §44.C: интервал автообновления метрик (не секрет).
	if patch.General.MetricsRefetchMs != nil {
		out.General.MetricsRefetchMs = patch.General.MetricsRefetchMs
	}
	// §44-perf: режим подсчёта уникальных (точно/приблизительно), не секрет.
	if patch.General.MetricsApproxCounts != nil {
		out.General.MetricsApproxCounts = patch.General.MetricsApproxCounts
	}
	// §94.5: срок хранения журнала отказов, не секрет.
	if patch.General.MaxBodyBytes != nil {
		out.General.MaxBodyBytes = patch.General.MaxBodyBytes
	}
	if patch.General.MaxAsyncBodyBytes != nil {
		out.General.MaxAsyncBodyBytes = patch.General.MaxAsyncBodyBytes
	}
	if patch.General.RejectedRetentionDays != nil {
		out.General.RejectedRetentionDays = patch.General.RejectedRetentionDays
	}
	// §98.5: глобальная политика защиты узла.
	if patch.General.CircuitBreakerThreshold != nil {
		out.General.CircuitBreakerThreshold = patch.General.CircuitBreakerThreshold
	}
	if patch.General.CircuitBreakerCooldownSec != nil {
		out.General.CircuitBreakerCooldownSec = patch.General.CircuitBreakerCooldownSec
	}

	// §34.2: Security — длительность сессии (не секрет).
	if patch.Security.SessionTTLSeconds != nil {
		out.Security.SessionTTLSeconds = patch.Security.SessionTTLSeconds
	}

	// §51: уровень логирования сервисов (не секрет).
	if patch.Logging.Level != nil {
		out.Logging.Level = patch.Logging.Level
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

	// Mail (§88). password не перезаписываем маской — иначе правка любого
	// соседнего поля стирала бы сохранённый пароль релея.
	mailPatch := patch.Mail
	if mailPatch.Enabled != nil {
		out.Mail.Enabled = mailPatch.Enabled
	}
	if mailPatch.Host != nil {
		out.Mail.Host = mailPatch.Host
	}
	if mailPatch.Port != nil {
		out.Mail.Port = mailPatch.Port
	}
	if mailPatch.Encryption != nil {
		out.Mail.Encryption = mailPatch.Encryption
	}
	if mailPatch.AuthType != nil {
		out.Mail.AuthType = mailPatch.AuthType
	}
	if mailPatch.Username != nil {
		out.Mail.Username = mailPatch.Username
	}
	if mailPatch.Password != nil && *mailPatch.Password != "***" {
		out.Mail.Password = mailPatch.Password
	}
	if mailPatch.FromAddress != nil {
		out.Mail.FromAddress = mailPatch.FromAddress
	}
	if mailPatch.FromName != nil {
		out.Mail.FromName = mailPatch.FromName
	}
	if mailPatch.HELOHost != nil {
		out.Mail.HELOHost = mailPatch.HELOHost
	}
	if mailPatch.TimeoutSec != nil {
		out.Mail.TimeoutSec = mailPatch.TimeoutSec
	}
	if mailPatch.SkipTLSVerify != nil {
		out.Mail.SkipTLSVerify = mailPatch.SkipTLSVerify
	}
	if mailPatch.PasswordResetEnabled != nil {
		out.Mail.PasswordResetEnabled = mailPatch.PasswordResetEnabled
	}
	if mailPatch.PasswordResetTTLMin != nil {
		out.Mail.PasswordResetTTLMin = mailPatch.PasswordResetTTLMin
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
	// §94.5: имя обязано совпадать с reloader.SectionGeneral — publish кастует
	// строку в Section без маппинга, а подписчики (срок хранения журнала
	// отказов в Web и Receiver) слушают именно её.
	if p.General.PublicBaseURL != nil || p.General.VersionOverride != nil ||
		p.General.MetricsRefetchMs != nil || p.General.MetricsApproxCounts != nil ||
		p.General.RejectedRetentionDays != nil ||
		p.General.MaxBodyBytes != nil || p.General.MaxAsyncBodyBytes != nil ||
		p.General.CircuitBreakerThreshold != nil || p.General.CircuitBreakerCooldownSec != nil {
		out = append(out, "general")
	}
	if p.Security.SessionTTLSeconds != nil {
		out = append(out, "security")
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
	// §51: имя обязано совпадать с reloader.SectionLogging — publish кастует
	// строку в reloader.Section без маппинга.
	if p.Logging.Level != nil {
		out = append(out, "logging")
	}
	// §88: имя обязано совпадать с reloader.SectionMail. Подписчик у секции
	// один — провайдер признака «восстановление доступно» (сам SMTP-транспорт
	// перезагружать нечего, соединение живёт одну отправку).
	if mailChanged(p.Mail) {
		out = append(out, "mail")
	}
	return out
}

// mailChanged — в патче задано хотя бы одно поле секции mail (§88).
func mailChanged(m domain.MailSettings) bool {
	return m.Enabled != nil || m.Host != nil || m.Port != nil || m.Encryption != nil ||
		m.AuthType != nil || m.Username != nil || m.Password != nil ||
		m.FromAddress != nil || m.FromName != nil || m.HELOHost != nil ||
		m.TimeoutSec != nil || m.SkipTLSVerify != nil ||
		m.PasswordResetEnabled != nil || m.PasswordResetTTLMin != nil
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
