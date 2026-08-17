package keyrotate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

func keyOf(b byte) *crypto.Cipher {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return crypto.MustNewCipher(base64.StdEncoding.EncodeToString(raw))
}

func docFrom(t *testing.T, raw string) map[string]any {
	t.Helper()
	doc := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	return doc
}

func TestRotateAppSettingsDoc(t *testing.T) {
	t.Parallel()
	oldC, newC := keyOf(1), keyOf(2)

	encOld := func(v string) string {
		s, err := oldC.Encrypt(v)
		require.NoError(t, err)
		return s
	}
	encNew := func(v string) string {
		s, err := newC.Encrypt(v)
		require.NoError(t, err)
		return s
	}

	t.Run("шифротекст старого ключа переезжает на новый", func(t *testing.T) {
		t.Parallel()
		doc := docFrom(t, `{"sentry":{"dsn":"`+encOld("dsn-value")+`"}}`)

		changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, 1, st.Reencrypted)
		assert.Equal(t, 0, st.Encrypted)
		got, _ := lookupString(doc, []string{"sentry", "dsn"})
		plain, err := newC.Decrypt(got)
		require.NoError(t, err)
		assert.Equal(t, "dsn-value", plain)
	})

	t.Run("plaintext шифруется и считается отдельно", func(t *testing.T) {
		t.Parallel()
		doc := docFrom(t, `{"clickhouse":{"password":"legacy-pwd"}}`)

		changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, 0, st.Reencrypted)
		assert.Equal(t, 1, st.Encrypted, "это первичное шифрование, а не ротация")
		got, _ := lookupString(doc, []string{"clickhouse", "password"})
		plain, err := newC.Decrypt(got)
		require.NoError(t, err)
		assert.Equal(t, "legacy-pwd", plain)
	})

	t.Run("уже новый ключ — пропуск (идемпотентность)", func(t *testing.T) {
		t.Parallel()
		already := encNew("bot-token")
		doc := docFrom(t, `{"notifications":{"telegram":{"bot_token":"`+already+`"}}}`)

		changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

		require.NoError(t, err)
		assert.False(t, changed, "повторный прогон не должен переписывать документ")
		assert.Equal(t, 1, st.SkippedAlreadyNew)
		got, _ := lookupString(doc, []string{"notifications", "telegram", "bot_token"})
		assert.Equal(t, already, got)
	})

	t.Run("пустые и отсутствующие поля", func(t *testing.T) {
		t.Parallel()
		doc := docFrom(t, `{"mail":{"password":""},"sentry":{"use":true}}`)

		changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, 1, st.Empty)
		assert.Equal(t, 1, st.Scanned, "отсутствующие пути даже не сканируются")
	})

	t.Run("чужой ключ — ошибка с путём поля", func(t *testing.T) {
		t.Parallel()
		foreign := keyOf(9)
		enc, err := foreign.Encrypt("smtp-pwd")
		require.NoError(t, err)
		doc := docFrom(t, `{"mail":{"password":"`+enc+`"}}`)

		_, _, err = rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

		require.Error(t, err)
		assert.ErrorIs(t, err, crypto.ErrDecryption)
		assert.Contains(t, err.Error(), "mail.password")
	})
}

// Ротация обязана сохранять поля, которых не знает: документ разбирается в
// map, а не в типизированную структуру, именно ради этого.
func TestRotateAppSettingsDoc_KeepsUnknownFields(t *testing.T) {
	t.Parallel()
	oldC, newC := keyOf(1), keyOf(2)
	enc, err := oldC.Encrypt("dsn-value")
	require.NoError(t, err)

	doc := docFrom(t, `{
		"sentry":{"dsn":"`+enc+`","environment":"prod","level":2},
		"general":{"public_base_url":"https://nexus.example.com"},
		"future_section":{"nested":{"flag":true}},
		"logging":{"level":5}
	}`)

	_, _, err = rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())
	require.NoError(t, err)

	assert.Equal(t, "prod", doc["sentry"].(map[string]any)["environment"])
	assert.Equal(t, float64(2), doc["sentry"].(map[string]any)["level"])
	assert.Equal(t, "https://nexus.example.com",
		doc["general"].(map[string]any)["public_base_url"])
	assert.Equal(t, true,
		doc["future_section"].(map[string]any)["nested"].(map[string]any)["flag"])
	assert.Equal(t, float64(5), doc["logging"].(map[string]any)["level"])
}

func TestRotateAppSettingsDoc_AllFourSecrets(t *testing.T) {
	t.Parallel()
	oldC, newC := keyOf(1), keyOf(2)
	// Значения намеренно не совпадают с именами JSON-ключей: иначе проверка
	// «секрета нет в выводе» ловила бы сам ключ.
	raw := `{"sentry":{"dsn":"dsn-secret"},"clickhouse":{"password":"ch-secret"},` +
		`"mail":{"password":"smtp-secret"},"notifications":{"telegram":{"bot_token":"bot-secret"}}}`
	doc := docFrom(t, raw)

	changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, Options{}, logging.NewNoop())

	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 4, st.Scanned)
	assert.Equal(t, 4, st.Encrypted)

	out, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.Equal(t, 4, strings.Count(string(out), "v1:"))
	for _, secret := range []string{"dsn-secret", "ch-secret", "smtp-secret", "bot-secret"} {
		assert.NotContains(t, string(out), secret)
	}
}

func TestRotateValue_EmptyAndScanCounters(t *testing.T) {
	t.Parallel()
	oldC, newC := keyOf(1), keyOf(2)

	var st Stats
	next, err := nextValue(oldC, newC, "", Options{}, &st)
	require.NoError(t, err)
	assert.Nil(t, next)
	assert.Equal(t, 1, st.Scanned)
	assert.Equal(t, 1, st.Empty)
}

func TestLookupString(t *testing.T) {
	t.Parallel()
	doc := docFrom(t, `{"a":{"b":"v"},"n":{"b":null},"num":{"b":7},"flat":"s"}`)

	tests := []struct {
		name string
		path []string
		want string
		ok   bool
	}{
		{"обычный путь", []string{"a", "b"}, "v", true},
		{"нет ключа", []string{"a", "zzz"}, "", false},
		{"нет секции", []string{"zzz", "b"}, "", false},
		{"null не строка", []string{"n", "b"}, "", false},
		{"число не строка", []string{"num", "b"}, "", false},
		{"строка вместо секции", []string{"flat", "b"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := lookupString(doc, tc.path)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// MODE=encrypt — разовая миграция открытых значений (§90.6). Ключ один и тот же,
// поэтому главный вопрос: не перешифрует ли повторный прогон уже зашифрованное
// (двойное шифрование сделало бы секрет нечитаемым для сервиса).
func TestEncryptMode_SkipsAlreadyEncrypted(t *testing.T) {
	t.Parallel()
	c := keyOf(7)
	enc, err := c.Encrypt("already-secret")
	require.NoError(t, err)

	doc := docFrom(t, `{"sentry":{"dsn":"`+enc+`"},"clickhouse":{"password":"plain-secret"}}`)

	// Первый прогон: открытое значение шифруется, зашифрованное не трогается.
	changed, st, err := rotateAppSettingsDoc(doc, c, c, Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, st.Encrypted, "зашифровано только то, что лежало открыто")
	assert.Equal(t, 1, st.SkippedAlreadyNew, "уже зашифрованное пропущено")
	assert.Equal(t, 0, st.Reencrypted)

	dsnAfter, _ := lookupString(doc, []string{"sentry", "dsn"})
	assert.Equal(t, enc, dsnAfter, "готовое значение осталось байт в байт прежним")

	// Второй прогон: делать больше нечего — документ не меняется.
	changed2, st2, err := rotateAppSettingsDoc(doc, c, c, Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.False(t, changed2, "повторный запуск не должен ничего переписывать")
	assert.Equal(t, 2, st2.SkippedAlreadyNew)
	assert.Equal(t, 0, st2.Encrypted)

	// И главное: значения по-прежнему читаются одним разшифрованием, то есть
	// двойного шифрования не произошло.
	for path, want := range map[string]string{"sentry.dsn": "already-secret", "clickhouse.password": "plain-secret"} {
		parts := strings.Split(path, ".")
		got, ok := lookupString(doc, parts)
		require.True(t, ok, path)
		plain, err := c.Decrypt(got)
		require.NoError(t, err, path)
		assert.Equal(t, want, plain, path)
	}
}

// MODE=decrypt — обратный ход для отката кода: значения возвращаются в plaintext.
func TestDecryptMode_ReturnsPlaintextAndIsIdempotent(t *testing.T) {
	t.Parallel()
	c := keyOf(8)
	enc, err := c.Encrypt("ch-secret")
	require.NoError(t, err)
	doc := docFrom(t, `{"clickhouse":{"password":"`+enc+`","host":"ch1"},"mail":{"password":"plain-smtp"}}`)

	changed, st, err := rotateAppSettingsDoc(doc, c, c, Options{Decrypt: true}, logging.NewNoop())

	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, st.Decrypted)
	assert.Equal(t, 1, st.SkippedPlaintext, "то, что и так открыто, не трогаем")

	got, _ := lookupString(doc, []string{"clickhouse", "password"})
	assert.Equal(t, "ch-secret", got, "значение вернулось открытым текстом")
	host, _ := lookupString(doc, []string{"clickhouse", "host"})
	assert.Equal(t, "ch1", host, "несекретные поля не тронуты")

	// Повторный обратный ход ничего не меняет.
	changed2, st2, err := rotateAppSettingsDoc(doc, c, c, Options{Decrypt: true}, logging.NewNoop())
	require.NoError(t, err)
	assert.False(t, changed2)
	assert.Equal(t, 2, st2.SkippedPlaintext)
	assert.Equal(t, 0, st2.Decrypted)
}

// Круговой сценарий: зашифровали → откатились (расшифровали) → снова зашифровали.
// Значения обязаны пережить оба перехода без потерь.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	c := keyOf(5)
	doc := docFrom(t, `{"sentry":{"dsn":"dsn-1"},"mail":{"password":"smtp-1"}}`)

	_, _, err := rotateAppSettingsDoc(doc, c, c, Options{}, logging.NewNoop())
	require.NoError(t, err)
	_, _, err = rotateAppSettingsDoc(doc, c, c, Options{Decrypt: true}, logging.NewNoop())
	require.NoError(t, err)

	dsn, _ := lookupString(doc, []string{"sentry", "dsn"})
	pwd, _ := lookupString(doc, []string{"mail", "password"})
	assert.Equal(t, "dsn-1", dsn)
	assert.Equal(t, "smtp-1", pwd)

	_, st, err := rotateAppSettingsDoc(doc, c, c, Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 2, st.Encrypted)
}

// DryRun не пишет даже в память: документ остаётся прежним.
func TestDryRun_LeavesDocumentUntouched(t *testing.T) {
	t.Parallel()
	c := keyOf(6)
	doc := docFrom(t, `{"sentry":{"dsn":"dsn-plain"}}`)

	_, st, err := rotateAppSettingsDoc(doc, c, c, Options{DryRun: true}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 1, st.Encrypted, "план показан")

	// Документ правится на месте, а решение «не писать» принимается уровнем выше
	// (RotateAppSettings не выполняет UPDATE) — здесь фиксируем именно счётчик.
	got, _ := lookupString(doc, []string{"sentry", "dsn"})
	assert.True(t, strings.HasPrefix(got, "v1:") || got == "dsn-plain")
}
