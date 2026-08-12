package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/mail"
)

// fakeMailSender записывает отправленное и умеет отказывать.
type fakeMailSender struct {
	mu   sync.Mutex
	sent []struct {
		cfg mail.Config
		msg mail.Message
	}
	err error
}

func (f *fakeMailSender) Send(_ context.Context, cfg mail.Config, msg mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, struct {
		cfg mail.Config
		msg mail.Message
	}{cfg, msg})
	return nil
}

func (f *fakeMailSender) last() (mail.Config, mail.Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return mail.Config{}, mail.Message{}, false
	}
	s := f.sent[len(f.sent)-1]
	return s.cfg, s.msg, true
}

func newMailTester(repo *fakeAppSettingsRepo, sender MailSender) *SettingsTester {
	return NewSettingsTester(repo, &config.Config{}, nil, nil, nil, sender, "Test", "v0", logging.NewNoop())
}

func configuredMailRepo() *fakeAppSettingsRepo {
	return &fakeAppSettingsRepo{current: &domain.AppSettings{Mail: domain.MailSettings{
		Enabled:     new(true),
		Host:        new("smtp.example.com"),
		Port:        new(587),
		Username:    new("noreply@example.com"),
		Password:    new("real-smtp-password"),
		FromAddress: new("nexus@example.com"),
		FromName:    new("Nexus"),
	}}}
}

// Маскированный пароль из формы не должен подменять сохранённый: иначе
// «Проверить» после открытия страницы всегда падало бы на аутентификации.
func TestSettingsTester_TestMail_MaskedPasswordUsesStored(t *testing.T) {
	t.Parallel()
	sender := &fakeMailSender{}
	tester := newMailTester(configuredMailRepo(), sender)

	res, err := tester.TestMail(context.Background(),
		&domain.MailSettings{Password: new("***")}, "admin@example.com")

	require.NoError(t, err)
	require.True(t, res.OK)
	assert.GreaterOrEqual(t, res.LatencyMs, int64(0))

	cfg, msg, ok := sender.last()
	require.True(t, ok)
	assert.Equal(t, "real-smtp-password", cfg.Password)
	assert.Equal(t, "admin@example.com", msg.To)
	assert.NotEmpty(t, msg.Subject)
	assert.NotEmpty(t, msg.Text)
	assert.NotEmpty(t, msg.HTML)
}

// Патч формы применяется поверх сохранённого — тест обязан проверять то, что
// оператор видит на экране, а не то, что лежит в БД.
func TestSettingsTester_TestMail_UsesUnsavedPatch(t *testing.T) {
	t.Parallel()
	sender := &fakeMailSender{}
	tester := newMailTester(configuredMailRepo(), sender)

	_, err := tester.TestMail(context.Background(), &domain.MailSettings{
		Host:       new("smtp2.example.com"),
		Port:       new(2525),
		Encryption: new(domain.MailEncryptionTLS),
		AuthType:   new(domain.MailAuthLogin),
		TimeoutSec: new(7),
	}, "admin@example.com")
	require.NoError(t, err)

	cfg, _, ok := sender.last()
	require.True(t, ok)
	assert.Equal(t, "smtp2.example.com", cfg.Host)
	assert.Equal(t, 2525, cfg.Port)
	assert.Equal(t, mail.EncryptionTLS, cfg.Encryption)
	assert.Equal(t, mail.AuthLogin, cfg.Auth)
	assert.Equal(t, 7, int(cfg.Timeout.Seconds()), "таймаут берётся из настроек, а не из pingTimeout")
}

// «Не смогли отправить» — это TestResult{OK:false}, а не Go-ошибка: контракт
// общий с проверками ClickHouse/Sentry/Telegram.
func TestSettingsTester_TestMail_SendFailureIsResultNotError(t *testing.T) {
	t.Parallel()
	sender := &fakeMailSender{err: errors.New("dial tcp: i/o timeout")}
	tester := newMailTester(configuredMailRepo(), sender)

	res, err := tester.TestMail(context.Background(), &domain.MailSettings{}, "admin@example.com")

	require.NoError(t, err, "сбой отправки не должен превращаться в 500")
	require.False(t, res.OK)
	assert.Contains(t, res.Error, "i/o timeout")
}

func TestSettingsTester_TestMail_Preconditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		repo    *fakeAppSettingsRepo
		to      string
		wantErr string
	}{
		{
			name:    "пустой получатель",
			repo:    configuredMailRepo(),
			to:      "   ",
			wantErr: "recipient is empty",
		},
		{
			name:    "не задан хост",
			repo:    &fakeAppSettingsRepo{current: &domain.AppSettings{}},
			to:      "admin@example.com",
			wantErr: "mail host is empty",
		},
		{
			name: "не задан отправитель",
			repo: &fakeAppSettingsRepo{current: &domain.AppSettings{Mail: domain.MailSettings{
				Host: new("smtp.example.com"),
			}}},
			to:      "admin@example.com",
			wantErr: "from_address is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sender := &fakeMailSender{}
			tester := newMailTester(tt.repo, sender)

			res, err := tester.TestMail(context.Background(), &domain.MailSettings{}, tt.to)

			require.NoError(t, err)
			require.False(t, res.OK)
			assert.Contains(t, res.Error, tt.wantErr)

			_, _, sentSomething := sender.last()
			assert.False(t, sentSomething, "при негодной конфигурации в сеть ходить нельзя")
		})
	}
}

// Отсутствие отправителя — это дефект wiring'а, а не конфигурации оператора:
// такой случай обязан быть Go-ошибкой (500), а не «ok:false».
func TestSettingsTester_TestMail_NilSenderIsInfrastructureError(t *testing.T) {
	t.Parallel()
	tester := newMailTester(configuredMailRepo(), nil)

	res, err := tester.TestMail(context.Background(), &domain.MailSettings{}, "admin@example.com")

	require.Error(t, err)
	assert.Nil(t, res)
}

func TestMailConfigFrom(t *testing.T) {
	t.Parallel()

	got := mailConfigFrom(domain.MailSettings{
		Host:          new("smtp.example.com"),
		Port:          new(465),
		Encryption:    new(domain.MailEncryptionTLS),
		AuthType:      new(domain.MailAuthCRAMMD5),
		Username:      new("u"),
		Password:      new("p"),
		FromAddress:   new("nexus@example.com"),
		FromName:      new("Шина"),
		HELOHost:      new("nexus.example.com"),
		TimeoutSec:    new(30),
		SkipTLSVerify: new(true),
	}.Resolve())

	assert.Equal(t, "smtp.example.com", got.Host)
	assert.Equal(t, 465, got.Port)
	assert.Equal(t, mail.EncryptionTLS, got.Encryption)
	assert.Equal(t, mail.AuthCRAMMD5, got.Auth)
	assert.Equal(t, "u", got.Username)
	assert.Equal(t, "p", got.Password)
	assert.Equal(t, "nexus@example.com", got.FromAddress)
	assert.Equal(t, "Шина", got.FromName)
	assert.Equal(t, "nexus.example.com", got.HELOHost)
	assert.Equal(t, 30, int(got.Timeout.Seconds()))
	assert.True(t, got.SkipTLSVerify)
}

// Значения настроек уходят в письмо — в HTML-часть только экранированными.
func TestMailTestMessage_EscapesSettingsInHTML(t *testing.T) {
	t.Parallel()

	msg := mailTestMessage("admin@example.com", domain.MailSettings{
		Host:        new("smtp.example.com"),
		FromAddress: new("nexus@example.com"),
	}.Resolve())

	assert.Contains(t, msg.Text, "smtp.example.com")
	assert.Contains(t, msg.HTML, "smtp.example.com")

	evil := mailTestMessage("admin@example.com", domain.MailSettings{
		Host:        new("evil"),
		FromAddress: new("a@b.c"),
		FromName:    new("<script>alert(1)</script>"),
	}.Resolve())
	assert.NotContains(t, evil.HTML, "<script>")
}
