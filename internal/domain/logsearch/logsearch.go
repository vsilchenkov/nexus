// Package logsearch — мини-язык поиска по логам узла (ТЗ §48).
//
// Один пакет задаёт семантику поиска и для SQL-генерации ClickHouse-адаптера
// (position*/match по колонкам url/parameters/request/response), и для
// in-memory зеркала live-tail (Expr.Match) — расхождение snapshot↔live
// исключено по построению.
//
// Грамматика plain-режима (§48.1):
//
//	expr   = orPart { "|" orPart }        // ИЛИ (низший приоритет)
//	orPart = term { "&" term }            // И
//	term   = ["-"] [prefix ":"] text      // "-" = НЕ
//	prefix = url | params | req | resp    // регистр не важен
//
// Экранирование: `\x` — литеральный `x` (актуально для & | - : \).
// В regex-режиме (§48.2) весь ввод — одно RE2-выражение, мини-язык отключён.
package logsearch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// ErrBadQuery — синтаксическая ошибка поискового запроса. Handler мапит её
// на HTTP 400 (i18n "error.bad_search_query").
var ErrBadQuery = errors.New("logsearch: invalid query")

// Field — колонка лога, по которой матчится терм.
type Field int

// Колонки поиска. FieldAll — терм без префикса: совпадение хотя бы в одной
// из четырёх колонок.
const (
	FieldAll Field = iota
	FieldURL
	FieldParams
	FieldRequest
	FieldResponse
)

// Options — режимы разбора (§48.2, кнопки Aa / ab| / .*).
type Options struct {
	CaseSensitive bool // Aa: с учётом регистра
	WholeWord     bool // ab|: только целое слово (\b…\b)
	Regex         bool // .*: весь ввод — одно RE2, мини-язык отключён
}

// Term — один терм выражения.
type Term struct {
	Field  Field
	Negate bool
	// Text — plain-игла после снятия экранирования. Используется
	// position*-матчингом (CH) и strings.Contains (Go), только если
	// Pattern пуст.
	Text string
	// Pattern — RE2-паттерн для CH match(); непуст только в word/regex-
	// режимах. Регистронезависимость уже вшита префиксом (?i).
	Pattern string

	re       *regexp.Regexp // скомпилированный Pattern (Go-матчер)
	textFold string         // strings.ToLower(Text) для регистронезависимого Contains
}

// Expr — разобранный запрос: ИЛИ из И-групп термов.
type Expr struct {
	Groups        [][]Term
	CaseSensitive bool
	WholeWord     bool
	Regex         bool
}

// prefixFields — белый список префиксов термов (§48.1). Любой другой текст
// с ':' — литерал.
var prefixFields = map[string]Field{
	"url":    FieldURL,
	"params": FieldParams,
	"req":    FieldRequest,
	"resp":   FieldResponse,
}

// unit — руна ввода с признаком «была экранирована» (экранированные руны
// не участвуют в разборе синтаксиса).
type unit struct {
	r   rune
	esc bool
}

// Parse разбирает поисковый запрос q в Expr. Пустой или синтаксически
// некорректный запрос (пустой терм, незавершённое `\`, невалидный regex) —
// ошибка, оборачивающая ErrBadQuery.
func Parse(q string, opts Options) (*Expr, error) {
	e := &Expr{CaseSensitive: opts.CaseSensitive, WholeWord: opts.WholeWord, Regex: opts.Regex}
	if opts.Regex {
		t, err := regexTerm(q, opts)
		if err != nil {
			return nil, err
		}
		e.Groups = [][]Term{{t}}
		return e, nil
	}
	units, err := tokenize(q)
	if err != nil {
		return nil, err
	}
	var group []Term
	flush := func(buf []unit) error {
		t, err := parseTerm(buf, opts)
		if err != nil {
			return err
		}
		group = append(group, t)
		return nil
	}
	// parseTerm не удерживает buf (текст копируется) — слайс переиспользуется.
	buf := make([]unit, 0, len(units))
	for _, u := range units {
		if u.esc {
			buf = append(buf, u)
			continue
		}
		switch u.r {
		case '&':
			if err := flush(buf); err != nil {
				return nil, err
			}
			buf = buf[:0]
		case '|':
			if err := flush(buf); err != nil {
				return nil, err
			}
			buf = buf[:0]
			e.Groups = append(e.Groups, group)
			group = nil
		default:
			buf = append(buf, u)
		}
	}
	if err := flush(buf); err != nil {
		return nil, err
	}
	e.Groups = append(e.Groups, group)
	return e, nil
}

// tokenize переводит q в последовательность unit, снимая `\`-экранирование.
func tokenize(q string) ([]unit, error) {
	units := make([]unit, 0, len(q))
	esc := false
	for _, r := range q {
		if esc {
			units = append(units, unit{r: r, esc: true})
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		units = append(units, unit{r: r})
	}
	if esc {
		return nil, fmt.Errorf("%w: trailing backslash", ErrBadQuery)
	}
	return units, nil
}

// parseTerm разбирает один терм: [-] [prefix:] text.
func parseTerm(buf []unit, opts Options) (Term, error) {
	buf = trimSpace(buf)
	var t Term
	if len(buf) > 0 && !buf[0].esc && buf[0].r == '-' {
		t.Negate = true
		buf = trimSpace(buf[1:])
	}
	if f, rest, ok := splitPrefix(buf); ok {
		t.Field = f
		buf = trimSpace(rest)
	}
	if len(buf) == 0 {
		return Term{}, fmt.Errorf("%w: empty term", ErrBadQuery)
	}
	runes := make([]rune, len(buf))
	for i, u := range buf {
		runes[i] = u.r
	}
	t.Text = string(runes)
	t.textFold = strings.ToLower(t.Text)
	if opts.WholeWord {
		pat := `\b(?:` + regexp.QuoteMeta(t.Text) + `)\b`
		if !opts.CaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return Term{}, fmt.Errorf("%w: compile word pattern: %v", ErrBadQuery, err)
		}
		t.Pattern, t.re = pat, re
	}
	return t, nil
}

// splitPrefix распознаёт префикс `name:` из белого списка: неэкранированные
// ASCII-буквы до первого неэкранированного ':'. Всё прочее — литерал.
func splitPrefix(buf []unit) (Field, []unit, bool) {
	for i, u := range buf {
		if u.esc {
			return 0, nil, false
		}
		if u.r == ':' {
			if i == 0 {
				return 0, nil, false
			}
			name := make([]rune, i)
			for j := range i {
				name[j] = buf[j].r
			}
			f, ok := prefixFields[strings.ToLower(string(name))]
			if !ok {
				return 0, nil, false
			}
			return f, buf[i+1:], true
		}
		if u.r > unicode.MaxASCII || !unicode.IsLetter(u.r) {
			return 0, nil, false
		}
	}
	return 0, nil, false
}

// trimSpace срезает неэкранированные пробельные руны по краям терма.
func trimSpace(buf []unit) []unit {
	for len(buf) > 0 && !buf[0].esc && unicode.IsSpace(buf[0].r) {
		buf = buf[1:]
	}
	for len(buf) > 0 && !buf[len(buf)-1].esc && unicode.IsSpace(buf[len(buf)-1].r) {
		buf = buf[:len(buf)-1]
	}
	return buf
}

// regexTerm строит единственный терм regex-режима: весь ввод — одно RE2.
func regexTerm(q string, opts Options) (Term, error) {
	if strings.TrimSpace(q) == "" {
		return Term{}, fmt.Errorf("%w: empty regex", ErrBadQuery)
	}
	pat := q
	if opts.WholeWord {
		pat = `\b(?:` + pat + `)\b`
	}
	if !opts.CaseSensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return Term{}, fmt.Errorf("%w: compile regex: %v", ErrBadQuery, err)
	}
	return Term{Field: FieldAll, Text: q, Pattern: pat, re: re}, nil
}

// Match — in-memory эквивалент SQL-условия, генерируемого CH-адаптером
// (зеркало для live-tail). Порядок аргументов фиксирован: url, parameters,
// request, response.
func (e *Expr) Match(url, params, req, resp string) bool {
	cols := [4]string{url, params, req, resp}
	var folded [4]string
	var haveFold [4]bool
	fold := func(i int) string {
		if !haveFold[i] {
			folded[i], haveFold[i] = strings.ToLower(cols[i]), true
		}
		return folded[i]
	}
	one := func(t *Term, i int) bool {
		if t.re != nil {
			return t.re.MatchString(cols[i])
		}
		if e.CaseSensitive {
			return strings.Contains(cols[i], t.Text)
		}
		return strings.Contains(fold(i), t.textFold)
	}
	term := func(t *Term) bool {
		var hit bool
		switch t.Field {
		case FieldURL:
			hit = one(t, 0)
		case FieldParams:
			hit = one(t, 1)
		case FieldRequest:
			hit = one(t, 2)
		case FieldResponse:
			hit = one(t, 3)
		default: // FieldAll
			hit = one(t, 0) || one(t, 1) || one(t, 2) || one(t, 3)
		}
		return hit != t.Negate
	}
	for gi := range e.Groups {
		all := true
		for ti := range e.Groups[gi] {
			if !term(&e.Groups[gi][ti]) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
