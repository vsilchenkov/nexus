// Package mail — отправка одного письма через SMTP-релей (§88.3).
//
// Без доменной логики: пакет знает только как соединиться и как отправить
// сообщение. Кому и по какому поводу шлём — решают потребители (тестовая
// отправка настроек и восстановление пароля), объявляя у себя узкий
// интерфейс-consumer.
//
// Реализован поверх github.com/wneessen/go-mail. Стандартный net/smtp взят не
// был осознанно: implicit TLS он не умеет, PLAIN поверх нешифрованного канала
// запрещает (а внутренние релеи на порту 25 настроены именно так), AUTH LOGIN
// в нём отсутствует вовсе, а MIME пришлось бы собирать руками — и все эти
// места ломаются молча, симптомом у получателя (§88.3).
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	gomail "github.com/wneessen/go-mail"

	"nexus/internal/platform/logging"
)

// Encryption — режим шифрования соединения с релеем. Значения совпадают со
// строками настроек (§88.2), маппинг делает потребитель.
type Encryption string

const (
	EncryptionNone     Encryption = "none"
	EncryptionSTARTTLS Encryption = "starttls"
	EncryptionTLS      Encryption = "tls" // implicit TLS, обычно порт 465
)

// AuthType — способ аутентификации на релее.
type AuthType string

const (
	AuthNone    AuthType = "none"
	AuthPlain   AuthType = "plain"
	AuthLogin   AuthType = "login"
	AuthCRAMMD5 AuthType = "cram-md5"
)

// Config — параметры соединения и отправителя. Без указателей: дефолты
// разворачиваются на стороне потребителя (domain.MailSettings.Resolve).
type Config struct {
	Host          string
	Port          int
	Encryption    Encryption
	Auth          AuthType
	Username      string
	Password      string
	FromAddress   string
	FromName      string
	HELOHost      string
	Timeout       time.Duration
	SkipTLSVerify bool
}

// Message — одно письмо. Пустой HTML означает письмо только с текстовой
// частью (без multipart/alternative).
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender отправляет письма. Соединение живёт одну отправку: пул для одного
// письма на действие пользователя не нужен, а долгоживущее соединение
// пришлось бы пересоздавать при каждой смене настроек.
type Sender struct {
	logger logging.Logger
}

func New(logger logging.Logger) *Sender {
	return &Sender{logger: logger}
}

// Send отправляет msg по параметрам cfg. Возвращает ошибку при негодной
// конфигурации, сбое соединения, отказе релея или отмене ctx.
func (s *Sender) Send(ctx context.Context, cfg Config, msg Message) error {
	m, err := buildMessage(cfg, msg)
	if err != nil {
		return err
	}
	plan, err := planFor(cfg)
	if err != nil {
		return err
	}
	client, err := gomail.NewClient(cfg.Host, optionsFor(plan, cfg.Password)...)
	if err != nil {
		return fmt.Errorf("mail: new client: %w", err)
	}
	defer func() { _ = client.Close() }()

	start := time.Now()
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		// §51.9: исход внешнего вызова. Пароль сюда не попадает — plan его
		// не хранит, а текст ошибки go-mail несёт адрес релея, не креды.
		s.logger.Debug("mail: send failed",
			s.logger.Str("host", cfg.Host),
			s.logger.Int("port", cfg.Port),
			s.logger.Str("encryption", string(cfg.Encryption)),
			s.logger.Str("auth", string(cfg.Auth)),
			s.logger.Str("to", msg.To),
			s.logger.Int("duration_ms", int(time.Since(start).Milliseconds())),
			s.logger.Err(err))
		return fmt.Errorf("mail: send: %w", err)
	}
	s.logger.Debug("mail: sent",
		s.logger.Str("host", cfg.Host),
		s.logger.Int("port", cfg.Port),
		s.logger.Str("encryption", string(cfg.Encryption)),
		s.logger.Str("auth", string(cfg.Auth)),
		s.logger.Str("to", msg.To),
		s.logger.Int("duration_ms", int(time.Since(start).Milliseconds())))
	return nil
}

// buildMessage собирает письмо. Тема кодируется по RFC 2047, а текстовая и
// HTML-части складываются в multipart/alternative силами go-mail.
func buildMessage(cfg Config, msg Message) (*gomail.Msg, error) {
	m := gomail.NewMsg()
	if err := m.FromFormat(cfg.FromName, cfg.FromAddress); err != nil {
		return nil, fmt.Errorf("mail: from %q: %w", cfg.FromAddress, err)
	}
	if err := m.To(msg.To); err != nil {
		return nil, fmt.Errorf("mail: to %q: %w", msg.To, err)
	}
	m.Subject(msg.Subject)
	m.SetBodyString(gomail.TypeTextPlain, msg.Text)
	if msg.HTML != "" {
		m.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	}
	return m, nil
}

// transportPlan — что именно поедет в go-mail. Чистая структура: маппинг
// настроек проверяется таблично, без сети и без доступа к неэкспортируемым
// полям gomail.Client. Пароль сюда намеренно не кладётся — чтобы структуру
// можно было печатать в тестах и в отладке целиком.
type transportPlan struct {
	Port       int
	UseSSL     bool             // implicit TLS
	TLSPolicy  gomail.TLSPolicy // применяется только при !UseSSL
	Auth       gomail.SMTPAuthType
	Username   string
	HELO       string
	Timeout    time.Duration
	SkipVerify bool
	ServerName string
}

func planFor(cfg Config) (transportPlan, error) {
	p := transportPlan{
		Port:       cfg.Port,
		Username:   cfg.Username,
		HELO:       cfg.HELOHost,
		Timeout:    cfg.Timeout,
		SkipVerify: cfg.SkipTLSVerify,
		ServerName: cfg.Host,
	}
	switch cfg.Encryption {
	case EncryptionNone:
		p.TLSPolicy = gomail.NoTLS
	case EncryptionSTARTTLS:
		// Именно TLSMandatory, а не TLSOpportunistic: молчаливый откат в
		// открытый канал для письма со сбросом пароля недопустим (§88.3).
		p.TLSPolicy = gomail.TLSMandatory
	case EncryptionTLS:
		p.UseSSL = true
	default:
		return transportPlan{}, fmt.Errorf("mail: unknown encryption %q", cfg.Encryption)
	}
	switch cfg.Auth {
	case AuthNone:
		p.Auth = gomail.SMTPAuthNoAuth
		p.Username = "" // креды в соединение не уходят вовсе
	case AuthPlain:
		p.Auth = gomail.SMTPAuthPlain
	case AuthLogin:
		p.Auth = gomail.SMTPAuthLogin
	case AuthCRAMMD5:
		p.Auth = gomail.SMTPAuthCramMD5
	default:
		return transportPlan{}, fmt.Errorf("mail: unknown auth type %q", cfg.Auth)
	}
	return p, nil
}

// optionsFor превращает план в опции go-mail. Пароль передаётся отдельным
// аргументом, а не полем плана (см. комментарий у transportPlan): при
// AuthNone он не уходит в соединение вовсе — там пусто и имя пользователя.
func optionsFor(p transportPlan, password string) []gomail.Option {
	opts := []gomail.Option{
		gomail.WithPort(p.Port),
		gomail.WithTimeout(p.Timeout),
		gomail.WithTLSConfig(&tls.Config{
			ServerName: p.ServerName,
			MinVersion: tls.VersionTLS12,
			// §88.10: осознанный режим для самоподписанных внутренних релеев,
			// включается только оператором и помечен в интерфейсе как небезопасный.
			InsecureSkipVerify: p.SkipVerify, //nolint:gosec // G402: см. выше
		}),
		gomail.WithSMTPAuth(p.Auth),
	}
	if p.UseSSL {
		opts = append(opts, gomail.WithSSL())
	} else {
		opts = append(opts, gomail.WithTLSPolicy(p.TLSPolicy))
	}
	if p.Username != "" {
		opts = append(opts, gomail.WithUsername(p.Username), gomail.WithPassword(password))
	}
	if p.HELO != "" {
		opts = append(opts, gomail.WithHELO(p.HELO))
	}
	return opts
}
