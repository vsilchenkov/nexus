package usecase

import (
	"context"
	"fmt"

	"nexus/internal/domain"
)

// settingsFieldCheck — проверка одного поля (или цельной секции) патча
// глобальных настроек. Проверки собраны таблицей, чтобы у двух потребителей
// был один список:
//   - Update (форма «Настройки») — первая ошибка отклоняет весь патч;
//   - PrepareImport (восстановление §110) — поле с ошибкой сбрасывается с
//     предупреждением, а остальное переносится.
//
// Разойдись эти списки — импорт записал бы значение, которое форма не дала бы
// сохранить (например, лимит тела выше потолка конфига цели, §97).
type settingsFieldCheck struct {
	// field — имя поля в предупреждении импорта ("general.max_body_bytes";
	// для секционных проверок — имя секции).
	field string
	check func(p *domain.AppSettings) error
	// drop сбрасывает поле/секцию в nil — «не переносить, оставить значение цели».
	drop func(p *domain.AppSettings)
}

// fieldChecks — проверки патча по полям. Непустота поля проверяется внутри
// check: nil-поле («не прислано») валидно всегда.
func (u *AppSettingsUsecase) fieldChecks() []settingsFieldCheck {
	return append(u.generalChecks(),
		settingsFieldCheck{"notifications.telegram", validateTelegramPatch,
			func(p *domain.AppSettings) { p.Notifications = domain.NotificationsSettings{} }},
		settingsFieldCheck{"security.session_ttl_seconds", func(p *domain.AppSettings) error {
			return checkPtr(p.Security.SessionTTLSeconds, domain.ValidateSessionTTLSeconds)
		}, func(p *domain.AppSettings) { p.Security.SessionTTLSeconds = nil }},
		settingsFieldCheck{"logging.level", func(p *domain.AppSettings) error {
			return checkPtr(p.Logging.Level, domain.ValidateLogLevel)
		}, func(p *domain.AppSettings) { p.Logging.Level = nil }},
		settingsFieldCheck{"mail", func(p *domain.AppSettings) error {
			return domain.ValidateMailSettings(p.Mail)
		}, func(p *domain.AppSettings) { p.Mail = domain.MailSettings{} }},
	)
}

// generalChecks — проверки секции general. version_override (§34.3) — тоже
// здесь: на боевой ноде поле запрещено, и импорт его просто не переносит.
func (u *AppSettingsUsecase) generalChecks() []settingsFieldCheck {
	return []settingsFieldCheck{
		{"general.client_host_backfill_cron", func(p *domain.AppSettings) error {
			return validateClientHostBackfillCron(p.General.ClientHostBackfillCron)
		}, func(p *domain.AppSettings) { p.General.ClientHostBackfillCron = nil }},
		{"general.public_base_url", func(p *domain.AppSettings) error {
			return checkPtr(p.General.PublicBaseURL, domain.ValidatePublicBaseURL)
		}, func(p *domain.AppSettings) { p.General.PublicBaseURL = nil }},
		{"general.metrics_refetch_ms", func(p *domain.AppSettings) error {
			return checkPtr(p.General.MetricsRefetchMs, domain.ValidateMetricsRefetchMs)
		}, func(p *domain.AppSettings) { p.General.MetricsRefetchMs = nil }},
		{"general.rejected_retention_days", func(p *domain.AppSettings) error {
			return checkPtr(p.General.RejectedRetentionDays, domain.ValidateRejectedRetentionDays)
		}, func(p *domain.AppSettings) { p.General.RejectedRetentionDays = nil }},
		{"general.max_body_bytes", func(p *domain.AppSettings) error {
			return checkPtr(p.General.MaxBodyBytes, func(v int) error {
				return domain.ValidateMaxBodyBytes(v, u.maxBodyBytesCap)
			})
		}, func(p *domain.AppSettings) { p.General.MaxBodyBytes = nil }},
		{"general.max_async_body_bytes", func(p *domain.AppSettings) error {
			return checkPtr(p.General.MaxAsyncBodyBytes, func(v int) error {
				return domain.ValidateMaxAsyncBodyBytes(v, u.maxAsyncBodyBytesCap)
			})
		}, func(p *domain.AppSettings) { p.General.MaxAsyncBodyBytes = nil }},
		{"general.circuit_breaker_threshold", func(p *domain.AppSettings) error {
			return checkPtr(p.General.CircuitBreakerThreshold, domain.ValidateBreakerThreshold)
		}, func(p *domain.AppSettings) { p.General.CircuitBreakerThreshold = nil }},
		{"general.circuit_breaker_cooldown_sec", func(p *domain.AppSettings) error {
			return checkPtr(p.General.CircuitBreakerCooldownSec, domain.ValidateBreakerCooldownSec)
		}, func(p *domain.AppSettings) { p.General.CircuitBreakerCooldownSec = nil }},
		{"general.version_override", func(p *domain.AppSettings) error {
			if p.General.VersionOverride != nil && !u.allowVersionOverride {
				return domain.ErrVersionOverrideForbidden
			}
			return nil
		}, func(p *domain.AppSettings) { p.General.VersionOverride = nil }},
	}
}

// checkPtr — проверка необязательного поля: nil («не прислано») валидно.
func checkPtr[T any](v *T, validate func(T) error) error {
	if v == nil {
		return nil
	}
	return validate(*v)
}

// validatePatch — все проверки патча по полям; первая ошибка отклоняет патч.
func (u *AppSettingsUsecase) validatePatch(p *domain.AppSettings) error {
	for _, c := range u.fieldChecks() {
		if err := c.check(p); err != nil {
			return err
		}
	}
	return nil
}

// validateMerged — межполевые правила, проверяемые по СМЕРЖЕННОМУ объекту: в
// патче их не проверить — там половины значений может не быть (приходит один
// флаг mail.enabled, а хост сохранён ранее; одно из двух полей лимита тела).
func validateMerged(merged *domain.AppSettings) error {
	// §88: межполевые правила почты.
	if err := domain.ValidateMailConsistency(merged.Mail); err != nil {
		return err
	}
	// §97: async-лимит не может быть выше sync.
	if domain.BodyLimitOrDefault(merged.General.MaxAsyncBodyBytes) > domain.BodyLimitOrDefault(merged.General.MaxBodyBytes) {
		return domain.ErrAsyncBodyLimitOverSync
	}
	return nil
}

// Prepare проверяет патч и накладывает его на текущие настройки, ничего не
// записывая: merged — то, что сохранит Update, sections — изменённые секции
// (для audit и hot-reload). Отдельно от Update, чтобы запись можно было
// выполнить в чужой транзакции (восстановление §110).
func (u *AppSettingsUsecase) Prepare(ctx context.Context, patch *domain.AppSettings) (*domain.AppSettings, []string, error) {
	if patch == nil {
		return nil, nil, fmt.Errorf("patch is nil")
	}
	if err := u.validatePatch(patch); err != nil {
		return nil, nil, err
	}
	current, err := u.repo.Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	merged := mergeAppSettings(current, patch)
	if err := validateMerged(merged); err != nil {
		return nil, nil, err
	}
	return merged, changedSections(patch), nil
}

// SettingsFieldSkip — поле настроек, которое импорт не перенёс (§110.7).
type SettingsFieldSkip struct {
	Field string
	Err   error
}

// PrepareImport — Prepare для восстановления конфигурации (§110.7): вместо
// отказа поле, не прошедшее проверку на ЭТОЙ ноде, сбрасывается (остаётся
// значение цели) и попадает в skipped. Не переносятся никогда и в skipped не
// попадают: секция clickhouse (импорт создаёт базы на текущем соединении, и
// адрес из файла увёл бы сервисы туда, где этих баз нет) и
// general.version_override (подмена версии для проверки баннера §34.3 — даже
// там, где она разрешена, восстановленная нода не должна врать о версии).
// Патч изменяется на месте.
func (u *AppSettingsUsecase) PrepareImport(ctx context.Context, patch *domain.AppSettings) (*domain.AppSettings, []string, []SettingsFieldSkip, error) {
	if patch == nil {
		return nil, nil, nil, fmt.Errorf("patch is nil")
	}
	patch.ClickHouse = domain.ClickHouseSettings{}
	patch.General.VersionOverride = nil
	var skipped []SettingsFieldSkip
	for _, c := range u.fieldChecks() {
		if err := c.check(patch); err != nil {
			c.drop(patch)
			skipped = append(skipped, SettingsFieldSkip{Field: c.field, Err: err})
		}
	}
	current, err := u.repo.Get(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	// Межполевые правила — по смерженному объекту, как в Prepare. Нарушившая
	// их часть патча сбрасывается целиком: почта — секцией, лимиты тела — парой.
	merged := mergeAppSettings(current, patch)
	if err := domain.ValidateMailConsistency(merged.Mail); err != nil {
		patch.Mail = domain.MailSettings{}
		skipped = append(skipped, SettingsFieldSkip{Field: "mail", Err: err})
	}
	if domain.BodyLimitOrDefault(merged.General.MaxAsyncBodyBytes) > domain.BodyLimitOrDefault(merged.General.MaxBodyBytes) {
		patch.General.MaxBodyBytes, patch.General.MaxAsyncBodyBytes = nil, nil
		skipped = append(skipped, SettingsFieldSkip{Field: "general.max_body_bytes", Err: domain.ErrAsyncBodyLimitOverSync})
	}
	return mergeAppSettings(current, patch), changedSections(patch), skipped, nil
}
