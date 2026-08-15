package mail

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleData() PasswordResetData {
	return PasswordResetData{
		UserName:         "Иван Иванов",
		ResetURL:         "https://nexus.example.com/reset-password?token=abc123",
		ExpiresInMinutes: 60,
		ExpiresAtUTC:     "2026-08-13 11:00 UTC",
		RequestIP:        "10.1.2.3",
		AppTitle:         "Nexus",
	}
}

func TestTemplates_PasswordReset_RU(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	msg, err := tpl.PasswordReset(LangRU, "ivanov@example.com", sampleData())
	require.NoError(t, err)

	assert.Equal(t, "ivanov@example.com", msg.To)
	assert.Equal(t, "Nexus: восстановление пароля", msg.Subject)

	for _, part := range []string{msg.Text, msg.HTML} {
		assert.Contains(t, part, "Иван Иванов")
		assert.Contains(t, part, "https://nexus.example.com/reset-password?token=abc123")
		assert.Contains(t, part, "60")
		assert.Contains(t, part, "2026-08-13 11:00 UTC")
		assert.Contains(t, part, "10.1.2.3")
	}
}

func TestTemplates_PasswordReset_EN(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	msg, err := tpl.PasswordReset(LangEN, "ivanov@example.com", sampleData())
	require.NoError(t, err)

	assert.Equal(t, "Nexus: password reset", msg.Subject)
	assert.Contains(t, msg.Text, "Hello")
	assert.NotContains(t, msg.Text, "Здравствуйте")
}

// Неизвестный язык не должен ронять отправку — откатываемся на английский.
func TestTemplates_PasswordReset_UnknownLangFallsBackToEN(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	msg, err := tpl.PasswordReset("de", "ivanov@example.com", sampleData())
	require.NoError(t, err)
	assert.Equal(t, "Nexus: password reset", msg.Subject)
}

// Код инстанса (§70) попадает в тему и в письмо, а пустой — не оставляет
// висящих скобок.
func TestTemplates_PasswordReset_InstanceID(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	d := sampleData()
	d.InstanceID = "kz"
	msg, err := tpl.PasswordReset(LangRU, "a@b.c", d)
	require.NoError(t, err)
	assert.Equal(t, "Nexus [kz]: восстановление пароля", msg.Subject)
	assert.Contains(t, msg.Text, "[kz]")

	msg, err = tpl.PasswordReset(LangRU, "a@b.c", sampleData())
	require.NoError(t, err)
	assert.Equal(t, "Nexus: восстановление пароля", msg.Subject)
	assert.NotContains(t, msg.Text, "[]")
	assert.NotContains(t, msg.HTML, "[]")
}

// Пустой IP — блок «запрос с адреса» не рендерится вовсе.
func TestTemplates_PasswordReset_EmptyIPOmitsBlock(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	d := sampleData()
	d.RequestIP = ""
	msg, err := tpl.PasswordReset(LangRU, "a@b.c", d)
	require.NoError(t, err)

	assert.NotContains(t, msg.Text, "Запрос отправлен с адреса")
	assert.NotContains(t, msg.HTML, "Запрос отправлен с адреса")
}

// Имя пользователя приходит из БД и попадает в разметку письма: html/template
// обязан его экранировать. Проверка на реальном payload, а не на «содержит <».
func TestTemplates_PasswordReset_EscapesUserNameInHTML(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	d := sampleData()
	d.UserName = `<script>alert(1)</script>`
	msg, err := tpl.PasswordReset(LangRU, "a@b.c", d)
	require.NoError(t, err)

	assert.NotContains(t, msg.HTML, "<script>", "имя обязано экранироваться в HTML-части")
	assert.Contains(t, msg.HTML, "&lt;script&gt;")
	// В plain-text экранирование не нужно и не должно уродовать текст.
	assert.Contains(t, msg.Text, `<script>alert(1)</script>`)
}

// Адрес ссылки получает URL-контекст: javascript:-схема не должна доехать до
// атрибута href как рабочая ссылка.
func TestTemplates_PasswordReset_FiltersDangerousURL(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	d := sampleData()
	d.ResetURL = "javascript:alert(1)"
	msg, err := tpl.PasswordReset(LangRU, "a@b.c", d)
	require.NoError(t, err)

	assert.NotContains(t, msg.HTML, `href="javascript:alert(1)"`)
	assert.Contains(t, msg.HTML, "ZgotmplZ", "html/template обязан отфильтровать небезопасную схему")
}

// Тема не должна нести имя пользователя: `\r\n` в нём дал бы инъекцию
// MIME-заголовка (§88.6).
func TestTemplates_PasswordReset_SubjectHasNoUserInput(t *testing.T) {
	t.Parallel()
	tpl, err := NewTemplates()
	require.NoError(t, err)

	d := sampleData()
	d.UserName = "Evil\r\nBcc: attacker@example.com"
	msg, err := tpl.PasswordReset(LangRU, "a@b.c", d)
	require.NoError(t, err)

	assert.NotContains(t, msg.Subject, "Evil")
	assert.NotContains(t, msg.Subject, "Bcc")
	assert.False(t, strings.ContainsAny(msg.Subject, "\r\n"))
}
