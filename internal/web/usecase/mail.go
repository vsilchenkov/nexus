package usecase

import (
	"context"
	"html"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/mail"
)

// MailSender — отправка одного письма. Интерфейс объявлен на стороне
// потребителя (CLAUDE.md §3); реализует его platform/mail.Sender.
type MailSender interface {
	Send(ctx context.Context, cfg mail.Config, msg mail.Message) error
}

// Назначения писем для метрик (§88.9).
const (
	MailPurposePasswordReset = "password_reset"
	MailPurposeTest          = "test"
)

// MailMetrics — учёт исхода и длительности отправки. Реализует
// metrics.Metrics; nil допустим (unit-тесты).
type MailMetrics interface {
	ObserveMailSend(purpose, result string, seconds float64)
}

// observeMailSend — общая точка учёта для обеих отправок (тестовой и
// восстановления): иначе одна из них рано или поздно окажется без метрики.
func observeMailSend(m MailMetrics, purpose string, started time.Time, err error) {
	if m == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.ObserveMailSend(purpose, result, time.Since(started).Seconds())
}

// mailConfigFrom переводит разрешённые настройки в параметры транспорта.
// Единственная точка перевода: строки настроек и типы платформы совпадают
// по значениям, но не по типам — чтобы platform/mail не зависел от домена.
func mailConfigFrom(m domain.ResolvedMail) mail.Config {
	return mail.Config{
		Host:          m.Host,
		Port:          m.Port,
		Encryption:    mail.Encryption(m.Encryption),
		Auth:          mail.AuthType(m.AuthType),
		Username:      m.Username,
		Password:      m.Password,
		FromAddress:   m.FromAddress,
		FromName:      m.FromName,
		HELOHost:      m.HELOHost,
		Timeout:       time.Duration(m.TimeoutSec) * time.Second,
		SkipTLSVerify: m.SkipTLSVerify,
	}
}

// mailTestMessage — письмо кнопки «Отправить тестовое письмо» (§88.6).
// Короткое и самодостаточное: получатель должен понять, откуда оно и что
// проверялось, не заглядывая в интерфейс.
func mailTestMessage(to string, m domain.ResolvedMail) mail.Message {
	return mail.Message{
		To:      to,
		Subject: "Nexus: проверка почтовых настроек",
		Text: "Это тестовое письмо от Nexus.\n" +
			"Если вы его получили, отправка почты настроена верно.\n\n" +
			"Релей: " + m.Host + "\n" +
			"Отправитель: " + m.FromAddress + "\n\n" +
			"--\nПисьмо отправлено автоматически, отвечать на него не нужно.\n",
		HTML: `<div style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:14px;color:#111">` +
			`<p>Это тестовое письмо от <strong>Nexus</strong>.<br>` +
			`Если вы его получили, отправка почты настроена верно.</p>` +
			// Хост и адрес отправителя задаёт админ, но письмо уходит наружу —
			// в разметку они идут только экранированными.
			`<p style="color:#555">Релей: ` + html.EscapeString(m.Host) + `<br>` +
			`Отправитель: ` + html.EscapeString(m.FromAddress) + `</p>` +
			`<hr style="border:none;border-top:1px solid #e5e5e5">` +
			`<p style="color:#888;font-size:12px">Письмо отправлено автоматически, отвечать на него не нужно.</p>` +
			`</div>`,
	}
}
