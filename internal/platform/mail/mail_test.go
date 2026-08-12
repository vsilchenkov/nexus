package mail

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomail "github.com/wneessen/go-mail"

	"nexus/internal/platform/logging"
)

// parsedMessage — письмо, разобранное настоящими парсерами stdlib. Проверять
// сырой текст подстроками нельзя: заголовки сворачиваются, а тела едут в
// quoted-printable — «=D0=97» вместо «З» и «=3D» вместо «=».
type parsedMessage struct{ msg *netmail.Message }

func parseMessage(t *testing.T, raw string) parsedMessage {
	t.Helper()
	m, err := netmail.ReadMessage(strings.NewReader(raw))
	require.NoError(t, err, "письмо должно разбираться net/mail")
	return parsedMessage{msg: m}
}

func (p parsedMessage) header(name string) string { return p.msg.Header.Get(name) }

// decodedBody возвращает тело, снятое с quoted-printable.
func (p parsedMessage) decodedBody(t *testing.T) string {
	t.Helper()
	r := p.msg.Body // net/mail.Message.Body уже io.Reader
	if strings.EqualFold(p.header("Content-Transfer-Encoding"), "quoted-printable") {
		r = quotedprintable.NewReader(r)
	}
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}

// alternativeParts возвращает части multipart/alternative по их media type,
// каждая — уже раскодированная.
func (p parsedMessage) alternativeParts(t *testing.T) map[string]string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(p.header("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/alternative", mediaType)
	require.NotEmpty(t, params["boundary"])

	out := map[string]string{}
	mr := multipart.NewReader(p.msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		partType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		require.NoError(t, err)

		var r io.Reader = part
		if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
			r = quotedprintable.NewReader(r)
		}
		b, err := io.ReadAll(r)
		require.NoError(t, err)
		out[partType] = string(b)
	}
	return out
}

func TestPlanFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		encryption Encryption
		auth       AuthType
		wantSSL    bool
		wantPolicy gomail.TLSPolicy
		wantAuth   gomail.SMTPAuthType
		wantUser   string
	}{
		{"none + none", EncryptionNone, AuthNone, false, gomail.NoTLS, gomail.SMTPAuthNoAuth, ""},
		{"none + plain", EncryptionNone, AuthPlain, false, gomail.NoTLS, gomail.SMTPAuthPlain, "u"},
		{"none + login", EncryptionNone, AuthLogin, false, gomail.NoTLS, gomail.SMTPAuthLogin, "u"},
		{"none + cram", EncryptionNone, AuthCRAMMD5, false, gomail.NoTLS, gomail.SMTPAuthCramMD5, "u"},

		{"starttls + none", EncryptionSTARTTLS, AuthNone, false, gomail.TLSMandatory, gomail.SMTPAuthNoAuth, ""},
		{"starttls + plain", EncryptionSTARTTLS, AuthPlain, false, gomail.TLSMandatory, gomail.SMTPAuthPlain, "u"},
		{"starttls + login", EncryptionSTARTTLS, AuthLogin, false, gomail.TLSMandatory, gomail.SMTPAuthLogin, "u"},
		{"starttls + cram", EncryptionSTARTTLS, AuthCRAMMD5, false, gomail.TLSMandatory, gomail.SMTPAuthCramMD5, "u"},

		{"tls + none", EncryptionTLS, AuthNone, true, 0, gomail.SMTPAuthNoAuth, ""},
		{"tls + plain", EncryptionTLS, AuthPlain, true, 0, gomail.SMTPAuthPlain, "u"},
		{"tls + login", EncryptionTLS, AuthLogin, true, 0, gomail.SMTPAuthLogin, "u"},
		{"tls + cram", EncryptionTLS, AuthCRAMMD5, true, 0, gomail.SMTPAuthCramMD5, "u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := planFor(Config{
				Host: "smtp.example.com", Port: 587,
				Encryption: tt.encryption, Auth: tt.auth,
				Username: "u", Password: "p",
				Timeout: 10 * time.Second,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.wantSSL, got.UseSSL)
			assert.Equal(t, tt.wantAuth, got.Auth)
			assert.Equal(t, tt.wantUser, got.Username,
				"при auth=none имя пользователя не должно уходить в соединение")
			assert.Equal(t, "smtp.example.com", got.ServerName, "ServerName нужен для проверки сертификата")
			if !tt.wantSSL {
				assert.Equal(t, tt.wantPolicy, got.TLSPolicy)
			}
		})
	}
}

// STARTTLS обязан быть mandatory: opportunistic молча откатился бы в
// открытый канал, а там едет ссылка на смену пароля (§88.3).
func TestPlanFor_StartTLSIsMandatoryNotOpportunistic(t *testing.T) {
	t.Parallel()
	got, err := planFor(Config{Encryption: EncryptionSTARTTLS, Auth: AuthNone})
	require.NoError(t, err)
	assert.Equal(t, gomail.TLSMandatory, got.TLSPolicy)
	assert.NotEqual(t, gomail.TLSOpportunistic, got.TLSPolicy)
}

func TestPlanFor_RejectsUnknownValues(t *testing.T) {
	t.Parallel()

	_, err := planFor(Config{Encryption: "ssl", Auth: AuthPlain})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown encryption")

	_, err = planFor(Config{Encryption: EncryptionNone, Auth: "oauth2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown auth type")
}

func TestPlanFor_CarriesHELOAndTimeout(t *testing.T) {
	t.Parallel()
	got, err := planFor(Config{
		Encryption: EncryptionNone, Auth: AuthNone,
		HELOHost: "nexus.example.com", Timeout: 42 * time.Second,
		SkipTLSVerify: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "nexus.example.com", got.HELO)
	assert.Equal(t, 42*time.Second, got.Timeout)
	assert.True(t, got.SkipVerify)
}

func testConfig(host string, port int) Config {
	return Config{
		Host: host, Port: port,
		Encryption: EncryptionNone, Auth: AuthPlain,
		Username: "noreply@example.com", Password: "s3cret",
		FromAddress: "nexus@example.com", FromName: "Nexus",
		Timeout: 10 * time.Second,
	}
}

func TestSend_DeliversMessage(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	host, port := srv.addr()

	err := New(logging.NewNoop()).Send(context.Background(), testConfig(host, port), Message{
		To:      "ivanov@example.com",
		Subject: "Nexus: восстановление пароля",
		Text:    "Здравствуйте! Ссылка: https://nexus.example.com/reset-password?token=abc",
		HTML:    "<p>Здравствуйте! <a href=\"https://nexus.example.com/reset-password?token=abc\">Задать пароль</a></p>",
	})
	require.NoError(t, err)

	commands, data, authArg := srv.received()
	joined := strings.Join(commands, "\n")

	assert.Contains(t, joined, "MAIL FROM:<nexus@example.com>")
	assert.Contains(t, joined, "RCPT TO:<ivanov@example.com>")
	assert.Contains(t, joined, "DATA")

	// Пароль ушёл через AUTH PLAIN — проверяем содержимое, а не сам факт.
	require.NotEmpty(t, authArg, "AUTH PLAIN должен нести полезную нагрузку")
	raw, decErr := base64.StdEncoding.DecodeString(authArg)
	require.NoError(t, decErr)
	assert.Equal(t, "\x00noreply@example.com\x00s3cret", string(raw))

	msg := parseMessage(t, data)
	assert.Equal(t, `"Nexus" <nexus@example.com>`, msg.header("From"))
	assert.Equal(t, "<ivanov@example.com>", msg.header("To"))

	parts := msg.alternativeParts(t)
	require.Len(t, parts, 2, "текст и HTML складываются в multipart/alternative")
	assert.Contains(t, parts["text/plain"], "Ссылка: https://nexus.example.com/reset-password?token=abc",
		"адрес обязан пережить quoted-printable без искажений")
	assert.Contains(t, parts["text/html"], `href="https://nexus.example.com/reset-password?token=abc"`)
	assert.Contains(t, parts["text/plain"], "Здравствуйте!")
}

// Кириллица в теме обязана уехать закодированной по RFC 2047 и раскодироваться
// обратно без потерь — иначе получатель видит кракозябры, а ни компилятор, ни
// линтер этого не заметят.
func TestSend_SubjectIsRFC2047Encoded(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	host, port := srv.addr()

	const subject = "Nexus: восстановление пароля"
	err := New(logging.NewNoop()).Send(context.Background(), testConfig(host, port), Message{
		To: "ivanov@example.com", Subject: subject, Text: "тело",
	})
	require.NoError(t, err)

	_, data, _ := srv.received()

	assert.Contains(t, data, "=?UTF-8?", "тема должна быть encoded-word, а не сырой кириллицей")
	assert.NotContains(t, data, "восстановление", "сырая кириллица в заголовке недопустима")

	msg := parseMessage(t, data)
	decoded, err := new(mime.WordDecoder).DecodeHeader(msg.header("Subject"))
	require.NoError(t, err)
	assert.Equal(t, subject, decoded, "тема обязана раскодироваться в исходную строку")
}

// Пустой HTML — письмо только с текстовой частью, без multipart-обёртки.
func TestSend_TextOnlyHasNoMultipart(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	host, port := srv.addr()

	err := New(logging.NewNoop()).Send(context.Background(), testConfig(host, port), Message{
		To: "ivanov@example.com", Subject: "check", Text: "plain only",
	})
	require.NoError(t, err)

	_, data, _ := srv.received()
	msg := parseMessage(t, data)

	mediaType, _, err := mime.ParseMediaType(msg.header("Content-Type"))
	require.NoError(t, err)
	assert.Equal(t, "text/plain", mediaType)
	assert.Equal(t, "plain only", strings.TrimSpace(msg.decodedBody(t)))
}

// При auth=none креды не уходят вовсе: команды AUTH в диалоге быть не должно.
func TestSend_AuthNoneSendsNoCredentials(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	host, port := srv.addr()

	cfg := testConfig(host, port)
	cfg.Auth = AuthNone
	err := New(logging.NewNoop()).Send(context.Background(), cfg, Message{
		To: "ivanov@example.com", Subject: "check", Text: "body",
	})
	require.NoError(t, err)

	commands, _, authArg := srv.received()
	assert.Empty(t, authArg)
	for _, c := range commands {
		assert.False(t, strings.HasPrefix(strings.ToUpper(c), "AUTH"),
			"при auth=none команда AUTH недопустима, получено: %q", c)
	}
}

func TestSend_ServerRejection(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	srv.failAt = "RCPT"
	host, port := srv.addr()

	err := New(logging.NewNoop()).Send(context.Background(), testConfig(host, port), Message{
		To: "ivanov@example.com", Subject: "check", Text: "body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mail: send")
}

func TestSend_UnreachableHost(t *testing.T) {
	t.Parallel()
	// Порт 1 на loopback гарантированно никем не слушается.
	err := New(logging.NewNoop()).Send(context.Background(), testConfig("127.0.0.1", 1), Message{
		To: "ivanov@example.com", Subject: "check", Text: "body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mail: send")
}

// Отмена контекста обязана прерывать отправку и не оставлять горутин
// (стережёт goleak в TestMain).
func TestSend_ContextCancelled(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	srv.silentGreeting = true
	host, port := srv.addr()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	cfg := testConfig(host, port)
	cfg.Timeout = 200 * time.Millisecond

	err := New(logging.NewNoop()).Send(ctx, cfg, Message{
		To: "ivanov@example.com", Subject: "check", Text: "body",
	})
	require.Error(t, err)
}

func TestSend_RejectsBadConfig(t *testing.T) {
	t.Parallel()

	t.Run("негодный адрес отправителя", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig("127.0.0.1", 25)
		cfg.FromAddress = "not-an-email"
		err := New(logging.NewNoop()).Send(context.Background(), cfg, Message{To: "a@b.com", Text: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mail: from")
	})

	t.Run("негодный адрес получателя", func(t *testing.T) {
		t.Parallel()
		err := New(logging.NewNoop()).Send(context.Background(), testConfig("127.0.0.1", 25),
			Message{To: "not-an-email", Text: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mail: to")
	})

	t.Run("неизвестный режим шифрования", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig("127.0.0.1", 25)
		cfg.Encryption = "ssl"
		err := New(logging.NewNoop()).Send(context.Background(), cfg, Message{To: "a@b.com", Text: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown encryption")
	})
}
