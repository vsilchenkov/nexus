package ackspec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SourceSet — какие источники данных шаблону реально нужны. Рендер парсит тело
// запроса, только если в наборе есть SourceBody: константный шаблон не должен
// платить разбором JSON на каждом запросе.
type SourceSet uint8

const (
	SourceBody SourceSet = 1 << iota
	SourceQuery
	SourcePath
	SourceNexus
)

// Has сообщает, входит ли источник в набор.
func (s SourceSet) Has(x SourceSet) bool { return s&x != 0 }

type source uint8

const (
	srcBody source = iota
	srcQuery
	srcPath
	srcNexus
)

func (s source) set() SourceSet {
	switch s {
	case srcQuery:
		return SourceQuery
	case srcPath:
		return SourcePath
	case srcNexus:
		return SourceNexus
	default:
		return SourceBody
	}
}

type segKind uint8

const (
	segKey segKind = iota
	segIndex
	segAll
)

type segment struct {
	kind segKind
	key  string
	idx  int
}

type fnKind uint8

const (
	fnNone fnKind = iota
	fnMax
	fnMin
	fnFirst
	fnLast
	fnCount
	fnDefault
)

type placeholder struct {
	raw    string // исходный ${...} — попадает в RenderError для диагностики
	src    source
	segs   []segment
	fn     fnKind
	defVal any  // литерал default(x), разобранный на этапе компиляции
	quoted bool // подставлять JSON-литералом вместо кавычек (см. пакетный godoc)
}

// part — либо кусок литерального текста, либо подстановка.
type part struct {
	text string
	ph   *placeholder
}

// Template — скомпилированный шаблон. Иммутабелен, пригоден для параллельного
// использования: рендер не пишет ни в одно его поле.
type Template struct {
	parts []part
	uses  SourceSet
	ct    ContentType
}

// Uses — набор источников, задействованных шаблоном.
func (t *Template) Uses() SourceSet { return t.uses }

func errSyntax(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTemplateSyntax, fmt.Sprintf(format, args...))
}

// Compile разбирает текст шаблона. Ошибка всегда обёртывает ErrTemplateSyntax
// (кроме неизвестного content type — это ошибка спеки, не текста).
func Compile(tmpl string, ct ContentType) (*Template, error) {
	if !ct.Valid() {
		return nil, fmt.Errorf("%w: unknown content_type %q", ErrSpecInvalid, ct)
	}
	if len(tmpl) > MaxTemplateBytes {
		return nil, errSyntax("template is %d bytes, limit is %d", len(tmpl), MaxTemplateBytes)
	}
	t := &Template{ct: ct}
	var lit strings.Builder
	for i := 0; i < len(tmpl); {
		j := strings.Index(tmpl[i:], "${")
		if j < 0 {
			lit.WriteString(tmpl[i:])
			break
		}
		// $${ — экранированная открывающая последовательность.
		if j > 0 && tmpl[i+j-1] == '$' {
			lit.WriteString(tmpl[i : i+j-1])
			lit.WriteString("${")
			i += j + 2
			continue
		}
		lit.WriteString(tmpl[i : i+j])
		end, err := findClose(tmpl, i+j+2)
		if err != nil {
			return nil, err
		}
		ph, err := parseExpr(tmpl[i+j+2 : end])
		if err != nil {
			return nil, err
		}
		i = t.appendPlaceholder(&lit, ph, tmpl, end+1)
	}
	t.parts = append(t.parts, part{text: lit.String()})
	if err := t.countPlaceholders(); err != nil {
		return nil, err
	}
	return t, nil
}

// appendPlaceholder закрывает накопленный литерал, решает вопрос quoted-режима
// и кладёт подстановку в шаблон. Возвращает позицию, с которой продолжать
// разбор: в quoted-режиме закрывающая кавычка съедается вместе с плейсхолдером.
func (t *Template) appendPlaceholder(lit *strings.Builder, ph *placeholder, tmpl string, next int) int {
	text := lit.String()
	if t.ct == ContentTypeJSON && strings.HasSuffix(text, `"`) && next < len(tmpl) && tmpl[next] == '"' {
		ph.quoted = true
		text = text[:len(text)-1]
		next++
	}
	t.parts = append(t.parts, part{text: text}, part{ph: ph})
	t.uses |= ph.src.set()
	lit.Reset()
	return next
}

func (t *Template) countPlaceholders() error {
	n := 0
	for _, p := range t.parts {
		if p.ph != nil {
			n++
		}
	}
	if n > MaxPlaceholders {
		return errSyntax("%d placeholders, limit is %d", n, MaxPlaceholders)
	}
	return nil
}

// findClose ищет закрывающую скобку подстановки, пропуская JSON-строки:
// default("a}b") — законный литерал, и '}' внутри него не завершает выражение.
func findClose(s string, from int) (int, error) {
	inStr := false
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inStr {
				i++
			}
		case '"':
			inStr = !inStr
		case '}':
			if !inStr {
				return i, nil
			}
		}
	}
	return 0, errSyntax("unterminated ${...}")
}

func parseExpr(expr string) (*placeholder, error) {
	raw := "${" + expr + "}"
	pathPart, fnPart := splitPipe(strings.TrimSpace(expr))
	// Имя источника — префикс из «ключевых» байт. Искать первую точку нельзя:
	// в body["a.b"] она принадлежит имени ключа, а не разделяет источник и путь.
	i := 0
	for i < len(pathPart) && isKeyByte(pathPart[i]) {
		i++
	}
	if i == 0 || i >= len(pathPart) || (pathPart[i] != '.' && pathPart[i] != '[') {
		return nil, errSyntax("%s — expected <source>.<path>", raw)
	}
	src, err := parseSource(pathPart[:i])
	if err != nil {
		return nil, err
	}
	rest := pathPart[i:]
	if rest[0] == '.' {
		rest = rest[1:]
	}
	segs, err := parsePath(rest)
	if err != nil {
		return nil, err
	}
	fn, def, err := parseFn(fnPart)
	if err != nil {
		return nil, err
	}
	if err := validateSegments(src, segs); err != nil {
		return nil, err
	}
	return &placeholder{raw: raw, src: src, segs: segs, fn: fn, defVal: def}, nil
}

// splitPipe делит выражение на путь и функцию по '|' вне строкового литерала.
func splitPipe(s string) (path, fn string) {
	inStr := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inStr {
				i++
			}
		case '"':
			inStr = !inStr
		case '|':
			if !inStr {
				return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
			}
		}
	}
	return s, ""
}

func parseSource(s string) (source, error) {
	switch strings.TrimSpace(s) {
	case "body":
		return srcBody, nil
	case "query":
		return srcQuery, nil
	case "path":
		return srcPath, nil
	case "nexus":
		return srcNexus, nil
	default:
		return 0, errSyntax("unknown source %q (allowed: body, query, path, nexus)", s)
	}
}

func parsePath(s string) ([]segment, error) {
	var segs []segment
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			seg, next, err := parseIndex(s, i)
			if err != nil {
				return nil, err
			}
			segs, i = append(segs, seg), next
		case '.':
			i++
			if i >= len(s) || s[i] == '.' || s[i] == '[' {
				return nil, errSyntax("empty path segment in %q", s)
			}
		default:
			j := i
			for j < len(s) && isKeyByte(s[j]) {
				j++
			}
			if j == i {
				return nil, errSyntax("unexpected %q in path %q", s[i], s)
			}
			segs, i = append(segs, segment{kind: segKey, key: s[i:j]}), j
		}
		if len(segs) > MaxPathDepth {
			return nil, errSyntax("path is deeper than %d segments", MaxPathDepth)
		}
	}
	if len(segs) == 0 {
		return nil, errSyntax("empty path")
	}
	return segs, nil
}

// parseIndex разбирает [n] / [*] / ["ключ с точкой"], начиная с '[' в позиции
// start. Возвращает сегмент и позицию сразу за ']'.
func parseIndex(s string, start int) (segment, int, error) {
	inStr := false
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inStr {
				i++
			}
		case '"':
			inStr = !inStr
		case ']':
			if inStr {
				continue
			}
			seg, err := buildIndex(s[start+1 : i])
			return seg, i + 1, err
		}
	}
	return segment{}, 0, errSyntax("unterminated [ in path %q", s)
}

func buildIndex(inner string) (segment, error) {
	inner = strings.TrimSpace(inner)
	switch {
	case inner == "*":
		return segment{kind: segAll}, nil
	case strings.HasPrefix(inner, `"`):
		var key string
		if err := json.Unmarshal([]byte(inner), &key); err != nil {
			return segment{}, errSyntax("bad quoted key %s", inner)
		}
		return segment{kind: segKey, key: key}, nil
	default:
		n, err := strconv.Atoi(inner)
		if err != nil || n < 0 {
			// Отрицательный индекс не поддержан намеренно: «последний элемент»
			// выражается функцией last, а две записи одного смысла в языке
			// настройки — лишний способ ошибиться.
			return segment{}, errSyntax("bad index [%s] (expected non-negative number, * or \"key\")", inner)
		}
		return segment{kind: segIndex, idx: n}, nil
	}
}

func parseFn(s string) (fnKind, any, error) {
	switch {
	case s == "":
		return fnNone, nil, nil
	case s == "max":
		return fnMax, nil, nil
	case s == "min":
		return fnMin, nil, nil
	case s == "first":
		return fnFirst, nil, nil
	case s == "last":
		return fnLast, nil, nil
	case s == "count":
		return fnCount, nil, nil
	case strings.HasPrefix(s, "default(") && strings.HasSuffix(s, ")"):
		lit := strings.TrimSpace(s[len("default(") : len(s)-1])
		v, err := decodeLiteral(lit)
		if err != nil {
			return fnNone, nil, err
		}
		return fnDefault, v, nil
	default:
		return fnNone, nil, errSyntax("unknown function %q (allowed: max, min, first, last, count, default(x))", s)
	}
}

// decodeLiteral разбирает аргумент default(x) как JSON-литерал, сохраняя числа
// в json.Number: default(9007199254740993) обязан вернуться без потери точности,
// как и значение, взятое из тела.
func decodeLiteral(lit string) (any, error) {
	if lit == "" {
		return nil, errSyntax("default() needs a literal argument")
	}
	dec := json.NewDecoder(strings.NewReader(lit))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errSyntax("bad default(%s) literal", lit)
	}
	if dec.More() {
		return nil, errSyntax("bad default(%s) literal", lit)
	}
	return v, nil
}

func isKeyByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

var nexusFields = map[string]bool{
	"id": true, "node": true, "team": true, "received_at": true, "queued": true,
}

// validateSegments отбраковывает пути, бессмысленные для своего источника, на
// этапе компиляции: у query, path и nexus форма фиксирована, и ошибку в ней
// оператор должен увидеть в форме узла, а не в проде через ReasonPathNotFound.
func validateSegments(src source, segs []segment) error {
	switch src {
	case srcNexus:
		if len(segs) != 1 || segs[0].kind != segKey || !nexusFields[segs[0].key] {
			return errSyntax("nexus.* allows one of: id, node, team, received_at, queued")
		}
	case srcPath:
		if segs[0].kind != segKey || (segs[0].key != "suffix" && segs[0].key != "segment") {
			return errSyntax("path.* allows path.suffix or path.segment[n]")
		}
		if segs[0].key == "suffix" && len(segs) != 1 {
			return errSyntax("path.suffix takes no index")
		}
		if segs[0].key == "segment" && (len(segs) != 2 || segs[1].kind != segIndex) {
			return errSyntax("path.segment requires a numeric index, e.g. path.segment[0]")
		}
	case srcQuery:
		if segs[0].kind != segKey {
			return errSyntax("query.* requires a parameter name")
		}
		if len(segs) > 2 || (len(segs) == 2 && segs[1].kind == segKey) {
			return errSyntax("query.<name> takes at most one index: query.<name>[*] or query.<name>[n]")
		}
	}
	return nil
}
