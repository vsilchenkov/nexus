package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/getsentry/sentry-go"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TestResult — итог проверки соединения (§7.10 ТЗ): успех/ошибка + latency.
// Возвращается оператору в UI кнопкой «Test connection» на странице
// Settings → Sentry/ClickHouse.
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ClickHouseFactory — фабрика, открывающая driver.Conn по конфигу. Совпадает
// по сигнатуре с clickhouse.New и clickhouse.Factory. Объявлена локально
// в consumer-пакете (CLAUDE.md §3), чтобы SettingsTester не зависел
// от пакета platform/clickhouse.
type ClickHouseFactory func(ctx context.Context, c *config.ClickHouseSection) (chdriver.Conn, error)

// SentryClientFactory — фабрика sentry.Client для теста. Объявлена
// интерфейсом, чтобы в unit-тестах подменить на fake без сети.
type SentryClientFactory func(opts sentry.ClientOptions) (SentryTestClient, error)

// SentryTestClient — узкий интерфейс над *sentry.Client: только то, что
// нужно SettingsTester. Определён в consumer-пакете (CLAUDE.md §3).
type SentryTestClient interface {
	CaptureMessage(message string, hint *sentry.EventHint, scope sentry.EventModifier) *sentry.EventID
	Flush(timeout time.Duration) bool
}

// SettingsTester — usecase «test connection» для секций Sentry и ClickHouse
// (Phase 6.3.2.6 / §7.10 ТЗ). Не сохраняет настройки и не дёргает
// pub/sub: только проверяет, что с заданным патчем коннект возможен.
//
// Merge-семантика: берёт текущие сохранённые настройки из repo и
// накладывает patch (исключая masked-секреты), затем накладывает
// merge на копию cfg.* — оригинал не мутируется.
type SettingsTester struct {
	repo          port.AppSettingsRepo
	cfg           *config.Config
	chFactory     ClickHouseFactory
	sentryFactory SentryClientFactory
	telegram      TelegramSender
	mail          MailSender
	projectName   string
	version       string
	pingTimeout   time.Duration
	flushTimeout  time.Duration
	logger        logging.Logger
}

// NewSettingsTester — конструктор SettingsTester. chFactory и sentryFactory
// инжектятся для тестируемости; в production wiring передают
// clickhouse.New и DefaultSentryClientFactory.
func NewSettingsTester(
	repo port.AppSettingsRepo,
	cfg *config.Config,
	chFactory ClickHouseFactory,
	sentryFactory SentryClientFactory,
	telegram TelegramSender,
	mailSender MailSender,
	projectName, version string,
	logger logging.Logger,
) *SettingsTester {
	return &SettingsTester{
		repo:          repo,
		cfg:           cfg,
		chFactory:     chFactory,
		sentryFactory: sentryFactory,
		telegram:      telegram,
		mail:          mailSender,
		projectName:   projectName,
		version:       version,
		pingTimeout:   5 * time.Second,
		flushTimeout:  5 * time.Second,
		logger:        logger,
	}
}

// DefaultSentryClientFactory оборачивает sentry.NewClient в SentryTestClient.
// Используется в web/app.go при wiring'е production-инстанса.
func DefaultSentryClientFactory(opts sentry.ClientOptions) (SentryTestClient, error) {
	c, err := sentry.NewClient(opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// TestClickHouse открывает временный conn по patch'у поверх current+cfg,
// делает Ping и закрывает. Результат — TestResult с latency или error-string.
// Возвращает (nil, err) только на инфраструктурных ошибках (repo, ctx);
// сам факт «коннект не получился» — это TestResult{OK:false,Error:...}.
func (t *SettingsTester) TestClickHouse(ctx context.Context, patch *domain.ClickHouseSettings) (*TestResult, error) {
	current, err := t.repo.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current settings: %w", err)
	}
	merged := mergeAppSettings(current, &domain.AppSettings{ClickHouse: derefCH(patch)})

	section := t.cfg.ClickHouse
	applyClickHouseSettings(&section, &merged.ClickHouse)

	if t.chFactory == nil {
		return nil, errors.New("clickhouse factory is nil")
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, t.pingTimeout)
	defer cancelOpen()

	start := time.Now()
	conn, err := t.chFactory(openCtx, &section)
	if err != nil {
		return &TestResult{OK: false, Error: err.Error()}, nil
	}
	defer func() { _ = conn.Close() }()

	pingCtx, cancelPing := context.WithTimeout(ctx, t.pingTimeout)
	defer cancelPing()
	if err := conn.Ping(pingCtx); err != nil {
		return &TestResult{OK: false, Error: err.Error()}, nil
	}
	return &TestResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}, nil
}

// TestSentry создаёт изолированный sentry.Client с merge'нутым DSN и
// отправляет одно тестовое событие. Глобальный hub не затрагивается.
// Если use=false — возвращает TestResult{OK:false,Error:"sentry disabled"}.
func (t *SettingsTester) TestSentry(ctx context.Context, patch *domain.SentrySettings) (*TestResult, error) {
	current, err := t.repo.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current settings: %w", err)
	}
	merged := mergeAppSettings(current, &domain.AppSettings{Sentry: derefSentry(patch)})

	section := t.cfg.Sentry
	applySentrySettings(&section, &merged.Sentry)

	if !section.Use {
		return &TestResult{OK: false, Error: "sentry disabled (use=false)"}, nil
	}
	if section.Dsn == "" {
		return &TestResult{OK: false, Error: "sentry DSN is empty"}, nil
	}
	if t.sentryFactory == nil {
		return nil, errors.New("sentry factory is nil")
	}

	start := time.Now()
	client, err := t.sentryFactory(sentry.ClientOptions{
		Dsn:              section.Dsn,
		Environment:      section.Environment,
		AttachStacktrace: section.AttachStacktrace,
		EnableTracing:    section.EnableTracing,
		TracesSampleRate: section.TracesSampleRate,
		Release:          t.version,
		ServerName:       t.projectName,
	})
	if err != nil {
		return &TestResult{OK: false, Error: err.Error()}, nil //nolint:nilerr // ошибка теста инкапсулирована в TestResult
	}
	_ = client.CaptureMessage("Nexus settings test event", nil, nil)
	if ok := client.Flush(t.flushTimeout); !ok {
		return &TestResult{OK: false, Error: "sentry flush timed out"}, nil
	}
	return &TestResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}, nil
}

// TestTelegram шлёт тестовое сообщение в Telegram с merge'нутыми настройками
// (§20.7). Маскированный bot_token в patch не используется — merge оставит
// сохранённый реальный токен. Не сохраняет настройки.
func (t *SettingsTester) TestTelegram(ctx context.Context, patch *domain.TelegramSettings) (*TestResult, error) {
	current, err := t.repo.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current settings: %w", err)
	}
	merged := mergeAppSettings(current, &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: derefTelegram(patch)},
	})
	tg := merged.Notifications.Telegram
	if tg.BotToken == nil || *tg.BotToken == "" {
		return &TestResult{OK: false, Error: "telegram bot_token is empty"}, nil
	}
	if tg.ChatID == nil || *tg.ChatID == "" {
		return &TestResult{OK: false, Error: "telegram chat_id is empty"}, nil
	}
	if t.telegram == nil {
		return nil, errors.New("telegram sender is nil")
	}
	sendCtx, cancel := context.WithTimeout(ctx, t.pingTimeout)
	defer cancel()
	start := time.Now()
	if err := t.telegram.Send(sendCtx, *tg.BotToken, *tg.ChatID, "✅ Nexus: test notification"); err != nil {
		return &TestResult{OK: false, Error: err.Error()}, nil
	}
	return &TestResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}, nil
}

func derefTelegram(p *domain.TelegramSettings) domain.TelegramSettings {
	if p == nil {
		return domain.TelegramSettings{}
	}
	return *p
}

// TestMail шлёт тестовое письмо по merge'нутым настройкам (§88.8.4). Патч —
// текущее несохранённое состояние формы; маскированный пароль в нём не
// используется, merge оставит сохранённый реальный. Настройки не сохраняются.
//
// Получатель приходит отдельным аргументом: у почты, в отличие от Telegram,
// адрес назначения не является частью настроек.
//
// Таймаут берётся из САМИХ настроек, а не из t.pingTimeout (5 с): для ping'а
// ClickHouse пяти секунд достаточно, а полный SMTP-диалог через корпоративный
// релей в них не укладывается — рабочая конфигурация показывала бы
// «context deadline exceeded».
func (t *SettingsTester) TestMail(ctx context.Context, patch *domain.MailSettings, to string) (*TestResult, error) {
	current, err := t.repo.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current settings: %w", err)
	}
	merged := mergeAppSettings(current, &domain.AppSettings{Mail: derefMail(patch)})
	m := merged.Mail.Resolve()

	if to = strings.TrimSpace(to); to == "" {
		return &TestResult{OK: false, Error: "recipient is empty"}, nil
	}
	if m.Host == "" {
		return &TestResult{OK: false, Error: "mail host is empty"}, nil
	}
	if m.FromAddress == "" {
		return &TestResult{OK: false, Error: "mail from_address is empty"}, nil
	}
	if t.mail == nil {
		return nil, errors.New("mail sender is nil")
	}

	sendCtx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()

	start := time.Now()
	if err := t.mail.Send(sendCtx, mailConfigFrom(m), mailTestMessage(to, m)); err != nil {
		return &TestResult{OK: false, Error: err.Error()}, nil
	}
	return &TestResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}, nil
}

func derefMail(p *domain.MailSettings) domain.MailSettings {
	if p == nil {
		return domain.MailSettings{}
	}
	return *p
}

// applyClickHouseSettings — overlay не-nil полей из domain.ClickHouseSettings
// на config.ClickHouseSection. Дублирует bootstrap.overlayClickHouse, но
// работает с domain-типом, а не bootstrap-internal'ом — это сознательный
// trade-off, чтобы usecase не зависел от bootstrap.
func applyClickHouseSettings(s *config.ClickHouseSection, p *domain.ClickHouseSettings) {
	if p == nil {
		return
	}
	if p.Host != nil {
		s.Host = *p.Host
	}
	if p.Port != nil {
		s.Port = *p.Port
	}
	if p.Database != nil {
		s.Database = *p.Database
	}
	if p.User != nil {
		s.User = *p.User
	}
	if p.Password != nil {
		s.Password = *p.Password
	}
	if p.BatchSize != nil {
		s.BatchSize = *p.BatchSize
	}
	if p.FlushIntervalSec != nil {
		s.FlushIntervalSec = *p.FlushIntervalSec
	}
	if p.BufferMaxSize != nil {
		s.BufferMaxSize = *p.BufferMaxSize
	}
	if p.Workers != nil {
		s.Workers = *p.Workers
	}
}

func applySentrySettings(s *config.SentrySection, p *domain.SentrySettings) {
	if p == nil {
		return
	}
	if p.Use != nil {
		s.Use = *p.Use
	}
	if p.DSN != nil {
		s.Dsn = *p.DSN
	}
	if p.Environment != nil {
		s.Environment = *p.Environment
	}
	if p.Level != nil {
		s.Level = *p.Level
	}
	if p.AttachStacktrace != nil {
		s.AttachStacktrace = *p.AttachStacktrace
	}
	if p.EnableTracing != nil {
		s.EnableTracing = *p.EnableTracing
	}
	if p.TracesSampleRate != nil {
		s.TracesSampleRate = *p.TracesSampleRate
	}
}

func derefCH(p *domain.ClickHouseSettings) domain.ClickHouseSettings {
	if p == nil {
		return domain.ClickHouseSettings{}
	}
	return *p
}

func derefSentry(p *domain.SentrySettings) domain.SentrySettings {
	if p == nil {
		return domain.SentrySettings{}
	}
	return *p
}
