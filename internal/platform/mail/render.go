package mail

import (
	"embed"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.txt templates/*.html
var templateFS embed.FS

// Языки писем (§88.6). Язык берётся из карточки ПОЛУЧАТЕЛЯ (users.lang), а не
// из запроса: письмо читает владелец ящика, а запрос мог прийти от постороннего.
const (
	LangRU = "ru"
	LangEN = "en"
)

// PasswordResetData — подстановки письма о восстановлении пароля (§88.6).
type PasswordResetData struct {
	UserName         string
	ResetURL         string
	ExpiresInMinutes int
	ExpiresAtUTC     string
	// RequestIP — пусто, если адрес неизвестен: блок в письме не рендерится.
	RequestIP string
	// InstanceID — код инстанса (§70). Пусто — не рендерится.
	InstanceID string
	AppTitle   string
}

// Templates — разобранные шаблоны писем.
//
// Разбираются один раз при старте, а не через template.Must в package var:
// глобальное изменяемое состояние запрещено (CLAUDE.md §4), а panic вне main —
// §5. Ошибка разбора возвращается вызывающему, который решает, деградировать
// или падать.
type Templates struct {
	text map[string]*texttemplate.Template
	html map[string]*htmltemplate.Template
}

// NewTemplates разбирает встроенные шаблоны.
func NewTemplates() (*Templates, error) {
	t := &Templates{
		text: map[string]*texttemplate.Template{},
		html: map[string]*htmltemplate.Template{},
	}
	for _, lang := range []string{LangRU, LangEN} {
		name := "password_reset." + lang

		txt, err := texttemplate.ParseFS(templateFS, "templates/"+name+".txt")
		if err != nil {
			return nil, fmt.Errorf("mail: parse %s.txt: %w", name, err)
		}
		t.text[lang] = txt

		// html/template — ради контекстного экранирования: имя пользователя
		// подставляется в разметку, а адрес ссылки получает URL-контекст.
		h, err := htmltemplate.ParseFS(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("mail: parse %s.html: %w", name, err)
		}
		t.html[lang] = h
	}
	return t, nil
}

// Тема письма (§88.6). Имя пользователя в тему НЕ подставляется: `\r\n` в нём
// дал бы инъекцию MIME-заголовка.
const (
	subjectPasswordResetRU = "восстановление пароля"
	subjectPasswordResetEN = "password reset"
)

// PasswordReset собирает письмо о восстановлении пароля на языке lang
// (неизвестный язык → английский).
func (t *Templates) PasswordReset(lang string, to string, d PasswordResetData) (Message, error) {
	lang = normalizeLang(lang)

	text, err := renderText(t.text[lang], d)
	if err != nil {
		return Message{}, err
	}
	html, err := renderHTML(t.html[lang], d)
	if err != nil {
		return Message{}, err
	}

	subject := d.AppTitle
	if d.InstanceID != "" {
		subject += " [" + d.InstanceID + "]"
	}
	if lang == LangRU {
		subject += ": " + subjectPasswordResetRU
	} else {
		subject += ": " + subjectPasswordResetEN
	}

	return Message{To: to, Subject: subject, Text: text, HTML: html}, nil
}

func normalizeLang(lang string) string {
	if strings.EqualFold(lang, LangRU) {
		return LangRU
	}
	return LangEN
}

func renderText(tpl *texttemplate.Template, d PasswordResetData) (string, error) {
	var b strings.Builder
	if err := tpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("mail: render text: %w", err)
	}
	return b.String(), nil
}

func renderHTML(tpl *htmltemplate.Template, d PasswordResetData) (string, error) {
	var b strings.Builder
	if err := tpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("mail: render html: %w", err)
	}
	return b.String(), nil
}
