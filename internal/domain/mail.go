package domain

import (
	netmail "net/mail"
	"strings"
)

// Режимы шифрования SMTP-соединения (§88.2).
//
// MailEncryptionSTARTTLS требует шифрования ОБЯЗАТЕЛЬНО: если сервер не
// предлагает STARTTLS, отправка завершается ошибкой. Молчаливый откат в
// открытый канал для письма со сбросом пароля недопустим (§88.3).
const (
	MailEncryptionNone     = "none"
	MailEncryptionSTARTTLS = "starttls"
	MailEncryptionTLS      = "tls" // implicit TLS, обычно порт 465
)

// Способы аутентификации на SMTP-релее (§88.2).
const (
	MailAuthNone    = "none"
	MailAuthPlain   = "plain"
	MailAuthLogin   = "login"
	MailAuthCRAMMD5 = "cram-md5"
)

// Дефолты и границы секции mail (§88.2).
const (
	MailDefaultPort       = 587
	MailDefaultTimeoutSec = 10
	MailTimeoutMinSec     = 1
	MailTimeoutMaxSec     = 120
	MailDefaultFromName   = "Nexus"

	MailHostMaxLen     = 255
	MailUsernameMaxLen = 255
	MailPasswordMaxLen = 255
	MailFromNameMaxLen = 128

	PasswordResetTTLDefaultMin = 60
	PasswordResetTTLMinMin     = 5
	PasswordResetTTLMaxMin     = 1440
)

// MailSettings — исходящая почта и восстановление пароля (§88.2).
//
// Все поля — указатели: nil означает «не задано, взять дефолт» (инвариант
// AppSettings). Password маскируется в AppSettingsUsecase.Get и не
// перезаписывается маской при merge.
//
// Секция названа mail, а не smtp: SMTP здесь деталь транспорта, а внутрь
// входят и параметры самого восстановления пароля.
type MailSettings struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	Host          *string `json:"host,omitempty"`
	Port          *int    `json:"port,omitempty"`
	Encryption    *string `json:"encryption,omitempty"`
	AuthType      *string `json:"auth_type,omitempty"`
	Username      *string `json:"username,omitempty"`
	Password      *string `json:"password,omitempty"`
	FromAddress   *string `json:"from_address,omitempty"`
	FromName      *string `json:"from_name,omitempty"`
	HELOHost      *string `json:"helo_host,omitempty"`
	TimeoutSec    *int    `json:"timeout_sec,omitempty"`
	SkipTLSVerify *bool   `json:"skip_tls_verify,omitempty"`

	// Восстановление пароля живёт в этой же секции: без работающей почты оно
	// бессмысленно, а оператор настраивает то и другое одним экраном (§88.2).
	PasswordResetEnabled *bool `json:"password_reset_enabled,omitempty"`
	PasswordResetTTLMin  *int  `json:"password_reset_ttl_min,omitempty"`
}

// ResolvedMail — та же секция с развёрнутыми дефолтами, без указателей.
// Её потребляют тестовая отправка и usecase восстановления, чтобы проверки
// «поле задано?» не расползались по слоям.
type ResolvedMail struct {
	Enabled       bool
	Host          string
	Port          int
	Encryption    string
	AuthType      string
	Username      string
	Password      string
	FromAddress   string
	FromName      string
	HELOHost      string
	TimeoutSec    int
	SkipTLSVerify bool

	PasswordResetEnabled bool
	PasswordResetTTLMin  int
}

// Resolve разворачивает nil-поля в дефолты. Возвращает значение и не мутирует
// приёмник: настройки читаются конкурентно.
func (m MailSettings) Resolve() ResolvedMail {
	out := ResolvedMail{
		Port:                MailDefaultPort,
		Encryption:          MailEncryptionSTARTTLS,
		AuthType:            MailAuthPlain,
		FromName:            MailDefaultFromName,
		TimeoutSec:          MailDefaultTimeoutSec,
		PasswordResetTTLMin: PasswordResetTTLDefaultMin,
	}
	if m.Enabled != nil {
		out.Enabled = *m.Enabled
	}
	if m.Host != nil {
		out.Host = strings.TrimSpace(*m.Host)
	}
	if m.Port != nil {
		out.Port = *m.Port
	}
	if m.Encryption != nil && *m.Encryption != "" {
		out.Encryption = *m.Encryption
	}
	if m.AuthType != nil && *m.AuthType != "" {
		out.AuthType = *m.AuthType
	}
	if m.Username != nil {
		out.Username = *m.Username
	}
	if m.Password != nil {
		out.Password = *m.Password
	}
	if m.FromAddress != nil {
		out.FromAddress = strings.TrimSpace(*m.FromAddress)
	}
	if m.FromName != nil && *m.FromName != "" {
		out.FromName = *m.FromName
	}
	if m.HELOHost != nil {
		out.HELOHost = strings.TrimSpace(*m.HELOHost)
	}
	if m.TimeoutSec != nil {
		out.TimeoutSec = *m.TimeoutSec
	}
	if m.SkipTLSVerify != nil {
		out.SkipTLSVerify = *m.SkipTLSVerify
	}
	if m.PasswordResetEnabled != nil {
		out.PasswordResetEnabled = *m.PasswordResetEnabled
	}
	if m.PasswordResetTTLMin != nil {
		out.PasswordResetTTLMin = *m.PasswordResetTTLMin
	}
	return out
}

// ValidateMailSettings проверяет ЗАДАННЫЕ поля патча по отдельности (§88.2).
// nil-поля пропускаются: патч частичный по контракту AppSettings.
//
// Межполевые правила («host обязателен при enabled» и т.п.) проверяет
// ValidateMailConsistency уже по смерженному результату — на патче их
// проверить нельзя, там половины значений просто нет.
func ValidateMailSettings(m MailSettings) error {
	if m.Host != nil {
		if err := validateMailHost(*m.Host); err != nil {
			return err
		}
	}
	if m.Port != nil && (*m.Port < 1 || *m.Port > 65535) {
		return ErrMailPortInvalid
	}
	if m.Encryption != nil && *m.Encryption != "" {
		switch *m.Encryption {
		case MailEncryptionNone, MailEncryptionSTARTTLS, MailEncryptionTLS:
		default:
			return ErrMailEncryptionInvalid
		}
	}
	if m.AuthType != nil && *m.AuthType != "" {
		switch *m.AuthType {
		case MailAuthNone, MailAuthPlain, MailAuthLogin, MailAuthCRAMMD5:
		default:
			return ErrMailAuthTypeInvalid
		}
	}
	if m.Username != nil && len(*m.Username) > MailUsernameMaxLen {
		return ErrMailUsernameLength
	}
	if m.Password != nil && len(*m.Password) > MailPasswordMaxLen {
		return ErrMailPasswordLength
	}
	if m.FromAddress != nil && strings.TrimSpace(*m.FromAddress) != "" {
		if _, err := netmail.ParseAddress(strings.TrimSpace(*m.FromAddress)); err != nil {
			return ErrMailFromInvalid
		}
	}
	if m.FromName != nil {
		if err := validateMailFromName(*m.FromName); err != nil {
			return err
		}
	}
	if m.HELOHost != nil {
		if err := validateMailHELO(*m.HELOHost); err != nil {
			return err
		}
	}
	if m.TimeoutSec != nil && (*m.TimeoutSec < MailTimeoutMinSec || *m.TimeoutSec > MailTimeoutMaxSec) {
		return ErrMailTimeoutInvalid
	}
	if m.PasswordResetTTLMin != nil &&
		(*m.PasswordResetTTLMin < PasswordResetTTLMinMin || *m.PasswordResetTTLMin > PasswordResetTTLMaxMin) {
		return ErrPasswordResetTTLInvalid
	}
	return nil
}

// ValidateMailConsistency проверяет межполевые правила по СМЕРЖЕННОЙ секции
// (§88.2): включённая отправка требует хоста и адреса отправителя, а
// восстановление пароля — включённой отправки.
func ValidateMailConsistency(m MailSettings) error {
	r := m.Resolve()
	if r.PasswordResetEnabled && !r.Enabled {
		return ErrMailResetRequiresMail
	}
	if !r.Enabled {
		return nil
	}
	if r.Host == "" {
		return ErrMailHostInvalid
	}
	if r.FromAddress == "" {
		return ErrMailFromInvalid
	}
	return nil
}

// MailPasswordResetReady — восстановление пароля действительно работоспособно
// (§88.4.5). Публичный адрес приложения входит в условие намеренно: без него
// ссылка в письме получится без хоста — письмо уйдёт, а перейти по нему будет
// некуда.
//
// Значение отдаётся форме входа через GET /api/version и решает, показывать
// ли ссылку «Забыли пароль?».
func MailPasswordResetReady(s *AppSettings) bool {
	if s == nil {
		return false
	}
	m := s.Mail.Resolve()
	if !m.Enabled || !m.PasswordResetEnabled || m.Host == "" || m.FromAddress == "" {
		return false
	}
	return s.General.PublicBaseURL != nil && *s.General.PublicBaseURL != ""
}

// validateMailHost — имя хоста релея без схемы, пробелов и пути: сюда
// регулярно вставляют «smtp://host» или «host:587», и оба варианта дают
// нерабочее соединение с невнятной ошибкой резолвера.
func validateMailHost(v string) error {
	h := strings.TrimSpace(v)
	if h == "" {
		return nil // пустой хост допустим, пока отправка выключена
	}
	if len(h) > MailHostMaxLen ||
		strings.ContainsAny(h, " \t\r\n/\\") ||
		strings.Contains(h, "://") ||
		strings.Contains(h, ":") {
		return ErrMailHostInvalid
	}
	return nil
}

// validateMailFromName запрещает перевод строки: он позволил бы дописать
// произвольные MIME-заголовки в письмо (§88.2). По этой же причине имя
// пользователя не подставляется в тему.
func validateMailFromName(v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return ErrMailFromNameInvalid
	}
	if len([]rune(v)) > MailFromNameMaxLen {
		return ErrMailFromNameInvalid
	}
	return nil
}

func validateMailHELO(v string) error {
	h := strings.TrimSpace(v)
	if h == "" {
		return nil
	}
	if len(h) > MailHostMaxLen || strings.ContainsAny(h, " \t\r\n") {
		return ErrMailHELOInvalid
	}
	return nil
}
