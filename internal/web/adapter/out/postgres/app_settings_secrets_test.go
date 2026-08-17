package postgres

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

// §90.1: секреты app_settings шифруются в адаптере, domain снаружи остаётся
// plaintext. Проверяется контракт хелперов — БД здесь не нужна.

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	return crypto.MustNewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
}

func settingsWithSecrets(dsn, chPwd, mailPwd, botToken string) *domain.AppSettings {
	s := &domain.AppSettings{}
	s.Sentry.DSN = &dsn
	s.ClickHouse.Password = &chPwd
	s.Mail.Password = &mailPwd
	s.Notifications.Telegram.BotToken = &botToken
	return s
}

func TestAppSettingsSecrets_RoundTrip(t *testing.T) {
	t.Parallel()
	c := testCipher(t)
	in := settingsWithSecrets("https://key@sentry.example.com/52", "ch-pwd", "smtp-pwd", "123:ABC")
	// Соседнее несекретное поле — оно обязано пережить оба преобразования.
	host := "ch1"
	in.ClickHouse.Host = &host

	enc, err := encryptAppSettingsSecrets(c, in)
	require.NoError(t, err)

	for _, got := range []*string{
		enc.Sentry.DSN, enc.ClickHouse.Password, enc.Mail.Password,
		enc.Notifications.Telegram.BotToken,
	} {
		require.NotNil(t, got)
		assert.True(t, strings.HasPrefix(*got, "v1:"), "секрет обязан уходить в БД шифротекстом, got %q", *got)
	}
	require.NotNil(t, enc.ClickHouse.Host)
	assert.Equal(t, "ch1", *enc.ClickHouse.Host, "несекретные поля не трогаются")

	require.NoError(t, decryptAppSettingsSecrets(c, enc, logging.NewNoop()))
	assert.Equal(t, "https://key@sentry.example.com/52", *enc.Sentry.DSN)
	assert.Equal(t, "ch-pwd", *enc.ClickHouse.Password)
	assert.Equal(t, "smtp-pwd", *enc.Mail.Password)
	assert.Equal(t, "123:ABC", *enc.Notifications.Telegram.BotToken)
}

// Вызыватель (usecase.Update) продолжает работать с тем же объектом после
// сохранения — шифроваться обязана копия, иначе у него на руках останется
// шифротекст вместо секрета.
func TestEncryptAppSettingsSecrets_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	c := testCipher(t)
	in := settingsWithSecrets("dsn-value", "ch-pwd", "smtp-pwd", "bot-token")

	enc, err := encryptAppSettingsSecrets(c, in)
	require.NoError(t, err)

	assert.Equal(t, "dsn-value", *in.Sentry.DSN)
	assert.Equal(t, "ch-pwd", *in.ClickHouse.Password)
	assert.Equal(t, "smtp-pwd", *in.Mail.Password)
	assert.Equal(t, "bot-token", *in.Notifications.Telegram.BotToken)
	assert.NotEqual(t, *in.Sentry.DSN, *enc.Sentry.DSN)
}

func TestAppSettingsSecrets_NilAndEmptyUntouched(t *testing.T) {
	t.Parallel()
	c := testCipher(t)

	t.Run("nil остаётся nil", func(t *testing.T) {
		t.Parallel()
		enc, err := encryptAppSettingsSecrets(c, &domain.AppSettings{})
		require.NoError(t, err)
		assert.Nil(t, enc.Sentry.DSN)
		assert.Nil(t, enc.ClickHouse.Password)
		assert.Nil(t, enc.Mail.Password)
		assert.Nil(t, enc.Notifications.Telegram.BotToken)
		require.NoError(t, decryptAppSettingsSecrets(c, enc, logging.NewNoop()))
		assert.Nil(t, enc.Sentry.DSN)
	})

	t.Run("пустая строка проходит насквозь", func(t *testing.T) {
		t.Parallel()
		in := settingsWithSecrets("", "", "", "")
		enc, err := encryptAppSettingsSecrets(c, in)
		require.NoError(t, err)
		assert.Equal(t, "", *enc.Sentry.DSN)
		require.NoError(t, decryptAppSettingsSecrets(c, enc, logging.NewNoop()))
		assert.Equal(t, "", *enc.Sentry.DSN)
	})
}

// Исторические значения лежат открытым текстом: читаются как есть и дозревают
// при следующем сохранении.
func TestDecryptAppSettingsSecrets_LegacyPlaintext(t *testing.T) {
	t.Parallel()
	c := testCipher(t)
	s := settingsWithSecrets("https://key@sentry.example.com/52", "ch-pwd", "smtp-pwd", "123:ABC")

	require.NoError(t, decryptAppSettingsSecrets(c, s, logging.NewNoop()))

	assert.Equal(t, "https://key@sentry.example.com/52", *s.Sentry.DSN)
	assert.Equal(t, "ch-pwd", *s.ClickHouse.Password)
	assert.Equal(t, "123:ABC", *s.Notifications.Telegram.BotToken)
}

// Чужой ключ — ошибка с путём поля, а не тихая подмена: иначе следующий
// read-modify-write в usecase затёр бы рабочий секрет.
func TestDecryptAppSettingsSecrets_ForeignKeyIsError(t *testing.T) {
	t.Parallel()
	keyA := testCipher(t)
	keyB := crypto.MustNewCipher(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))

	s := settingsWithSecrets("dsn", "ch-pwd", "smtp-pwd", "bot")
	enc, err := encryptAppSettingsSecrets(keyA, s)
	require.NoError(t, err)

	err = decryptAppSettingsSecrets(keyB, enc, logging.NewNoop())

	require.Error(t, err)
	assert.ErrorIs(t, err, crypto.ErrDecryption)
	assert.Contains(t, err.Error(), "sentry.dsn", "в ошибке обязан быть путь поля")
}
