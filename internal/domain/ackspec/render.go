package ackspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Reason — причина, по которой рендер не состоялся. Замкнутое множество:
// значение уходит меткой в метрику nexus_async_ack_render_failed_total, и
// свободная строка сделала бы её кардинальность неограниченной.
type Reason string

const (
	// ReasonBodyNotJSON — тело не разобрано как JSON (в том числе multipart §68).
	ReasonBodyNotJSON Reason = "body_not_json"
	// ReasonBodyTooLarge — тело больше MaxParseBodyBytes, разбор не выполнялся.
	ReasonBodyTooLarge Reason = "body_too_large"
	// ReasonPathNotFound — путь не дал ни одного значения.
	ReasonPathNotFound Reason = "path_not_found"
	// ReasonTypeMismatch — значение не того типа (max по строкам, объект в строку).
	ReasonTypeMismatch Reason = "type_mismatch"
	// ReasonAmbiguous — путь дал несколько значений, а функции нет.
	ReasonAmbiguous Reason = "ambiguous"
	// ReasonTooManyValues — путь с [*] выбрал больше MaxSelectedValues значений.
	ReasonTooManyValues Reason = "too_many_values"
	// ReasonOutputTooBig — отрендеренное тело превысило MaxOutputBytes.
	ReasonOutputTooBig Reason = "output_too_big"
	// ReasonOutputNotJSON — шаблон собрал синтаксически невалидный JSON.
	ReasonOutputNotJSON Reason = "output_not_json"
)

// RenderError — отказ рендера на живом запросе. Обрабатывается политикой
// Spec.OnError, а не как ошибка сервиса.
type RenderError struct {
	Reason      Reason
	Placeholder string // исходный ${...}; пусто, если отказ не привязан к подстановке
}

func (e *RenderError) Error() string {
	if e.Placeholder == "" {
		return fmt.Sprintf("ackspec: render failed: %s", e.Reason)
	}
	return fmt.Sprintf("ackspec: render failed: %s at %s", e.Reason, e.Placeholder)
}

// Unwrap связывает отказ с ErrRenderFailed, чтобы вызывающая сторона отличала
// его от ошибок конфигурации через errors.Is.
func (e *RenderError) Unwrap() error { return ErrRenderFailed }

// maxPlaceholderInError — сколько байт исходного ${...} попадает в ошибку.
// Плейсхолдер пишет оператор, но он же уезжает в логи и в ответ предпросмотра.
const maxPlaceholderInError = 120

func renderErr(reason Reason, ph *placeholder) error {
	e := &RenderError{Reason: reason}
	if ph != nil {
		e.Placeholder = ph.raw
		if len(e.Placeholder) > maxPlaceholderInError {
			e.Placeholder = e.Placeholder[:maxPlaceholderInError] + "…"
		}
	}
	return e
}

// Ctx — данные одного запроса для подстановок. Body и Query обязаны быть УЖЕ
// очищенными от кред (§41: динамическая авторизация вырезает своё поле) —
// иначе шаблон вернёт секрет вызывающей стороне.
type Ctx struct {
	Body        []byte
	ContentType string // Content-Type входящего запроса
	Query       url.Values
	PathSuffix  string // §39: хвост пути после пути узла
	ID          string // id сообщения в очереди
	Node        string // путь узла
	Team        string // slug команды
	ReceivedAt  time.Time
	Queued      bool // узел на паузе (§3.6)
}

// Render собирает тело ответа. Ошибка — всегда *RenderError.
func (t *Template) Render(c *Ctx) ([]byte, error) {
	var root any
	if t.uses.Has(SourceBody) {
		v, err := parseBody(c)
		if err != nil {
			return nil, err
		}
		root = v
	}
	buf := make([]byte, 0, 256)
	for _, p := range t.parts {
		if p.ph == nil {
			buf = append(buf, p.text...)
		} else {
			v, err := evalPlaceholder(p.ph, root, c)
			if err != nil {
				return nil, err
			}
			if buf, err = t.appendValue(buf, v, p.ph); err != nil {
				return nil, err
			}
		}
		if len(buf) > MaxOutputBytes {
			return nil, renderErr(ReasonOutputTooBig, nil)
		}
	}
	if t.ct == ContentTypeJSON && !json.Valid(buf) {
		return nil, renderErr(ReasonOutputNotJSON, nil)
	}
	return buf, nil
}

// parseBody разбирает тело запроса. Числа сохраняются как json.Number: эхо
// идентификатора обязано совпасть с присланным байт в байт, а float64 ломает
// целые больше 2^53 — ровно те, что встречаются в курсорах журналов.
func parseBody(c *Ctx) (any, error) {
	if c == nil || len(c.Body) == 0 {
		return nil, renderErr(ReasonBodyNotJSON, nil)
	}
	if len(c.Body) > MaxParseBodyBytes {
		return nil, renderErr(ReasonBodyTooLarge, nil)
	}
	if !isJSONMediaType(c.ContentType) {
		return nil, renderErr(ReasonBodyNotJSON, nil)
	}
	dec := json.NewDecoder(bytes.NewReader(c.Body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, renderErr(ReasonBodyNotJSON, nil)
	}
	if dec.More() {
		return nil, renderErr(ReasonBodyNotJSON, nil)
	}
	return v, nil
}

// isJSONMediaType — JSON ли тело по объявленному типу. Пустой Content-Type
// допускается к попытке разбора (клиенты его часто не ставят); явный не-JSON —
// нет, и multipart §68 отсекается здесь же, до чтения тела в память парсером.
func isJSONMediaType(ct string) bool {
	mt := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "":
		return true
	case "application/json", "text/json":
		return true
	default:
		return strings.HasSuffix(mt, "+json")
	}
}

func evalPlaceholder(ph *placeholder, root any, c *Ctx) (any, error) {
	vals, err := selectValues(ph, root, c)
	if err != nil {
		return nil, err
	}
	return applyFn(ph, vals)
}

func selectValues(ph *placeholder, root any, c *Ctx) ([]any, error) {
	switch ph.src {
	case srcBody:
		return walk(root, ph.segs)
	case srcQuery:
		return queryValues(ph, c)
	case srcPath:
		return pathValues(ph, c)
	default:
		return nexusValues(ph, c)
	}
}

// walk идёт по пути внутри тела. Значение, не подходящее сегменту по типу
// (ключ у массива, индекс у объекта), просто не попадает в набор: пустой набор
// на выходе честнее сообщает «такого пути нет», чем догадка о намерении.
func walk(root any, segs []segment) ([]any, error) {
	cur := []any{root}
	for _, s := range segs {
		next := make([]any, 0, len(cur))
		for _, v := range cur {
			switch s.kind {
			case segKey:
				if m, ok := v.(map[string]any); ok {
					if x, ok := m[s.key]; ok {
						next = append(next, x)
					}
				}
			case segIndex:
				if a, ok := v.([]any); ok && s.idx < len(a) {
					next = append(next, a[s.idx])
				}
			case segAll:
				if a, ok := v.([]any); ok {
					next = append(next, a...)
				}
			}
		}
		if len(next) > MaxSelectedValues {
			return nil, renderErr(ReasonTooManyValues, nil)
		}
		if cur = next; len(cur) == 0 {
			return nil, nil
		}
	}
	return cur, nil
}

func queryValues(ph *placeholder, c *Ctx) ([]any, error) {
	if c == nil {
		return nil, nil
	}
	raw := c.Query[ph.segs[0].key]
	vals := make([]any, 0, len(raw))
	for _, s := range raw {
		vals = append(vals, s)
	}
	if len(ph.segs) == 1 {
		if len(vals) == 0 {
			return nil, nil
		}
		return vals[:1], nil // query.<name> — первое значение
	}
	if ph.segs[1].kind == segAll {
		return vals, nil
	}
	if idx := ph.segs[1].idx; idx < len(vals) {
		return vals[idx : idx+1], nil
	}
	return nil, nil
}

func pathValues(ph *placeholder, c *Ctx) ([]any, error) {
	if c == nil {
		return nil, nil
	}
	if ph.segs[0].key == "suffix" {
		return []any{c.PathSuffix}, nil
	}
	parts := strings.FieldsFunc(c.PathSuffix, func(r rune) bool { return r == '/' })
	if idx := ph.segs[1].idx; idx < len(parts) {
		return []any{parts[idx]}, nil
	}
	return nil, nil
}

func nexusValues(ph *placeholder, c *Ctx) ([]any, error) {
	if c == nil {
		return nil, nil
	}
	switch ph.segs[0].key {
	case "id":
		return []any{c.ID}, nil
	case "node":
		return []any{c.Node}, nil
	case "team":
		return []any{c.Team}, nil
	case "queued":
		return []any{c.Queued}, nil
	default: // received_at
		return []any{c.ReceivedAt.UTC().Format(time.RFC3339)}, nil
	}
}

// applyFn схлопывает набор в одно значение. count и default не падают на пустом
// наборе намеренно: «сколько принято» на пустом пакете — это 0, а default(x)
// для того и существует, чтобы закрыть отсутствующий путь.
func applyFn(ph *placeholder, vals []any) (any, error) {
	switch ph.fn {
	case fnCount:
		return json.Number(strconv.Itoa(len(vals))), nil
	case fnDefault:
		if len(vals) == 0 || (len(vals) == 1 && vals[0] == nil) {
			return ph.defVal, nil
		}
		if len(vals) > 1 {
			return nil, renderErr(ReasonAmbiguous, ph)
		}
		return vals[0], nil
	}
	if len(vals) == 0 {
		return nil, renderErr(ReasonPathNotFound, ph)
	}
	switch ph.fn {
	case fnFirst:
		return vals[0], nil
	case fnLast:
		return vals[len(vals)-1], nil
	case fnMax, fnMin:
		return extremum(ph, vals)
	default:
		if len(vals) > 1 {
			return nil, renderErr(ReasonAmbiguous, ph)
		}
		return vals[0], nil
	}
}

func extremum(ph *placeholder, vals []any) (any, error) {
	best := 0
	for i, v := range vals {
		n, ok := v.(json.Number)
		if !ok {
			return nil, renderErr(ReasonTypeMismatch, ph)
		}
		if i == 0 {
			continue
		}
		cmp, err := compareNumbers(n, vals[best].(json.Number))
		if err != nil {
			return nil, renderErr(ReasonTypeMismatch, ph)
		}
		if (ph.fn == fnMax && cmp > 0) || (ph.fn == fnMin && cmp < 0) {
			best = i
		}
	}
	return vals[best], nil
}

// compareNumbers сравнивает точно, пока обе стороны — целые: курсоры журналов
// доходят до значений, которые float64 уже не различает.
func compareNumbers(a, b json.Number) (int, error) {
	ai, aerr := a.Int64()
	bi, berr := b.Int64()
	if aerr == nil && berr == nil {
		switch {
		case ai > bi:
			return 1, nil
		case ai < bi:
			return -1, nil
		default:
			return 0, nil
		}
	}
	af, aerr := a.Float64()
	bf, berr := b.Float64()
	if aerr != nil || berr != nil {
		return 0, fmt.Errorf("%w: not a number", ErrRenderFailed)
	}
	switch {
	case af > bf:
		return 1, nil
	case af < bf:
		return -1, nil
	default:
		return 0, nil
	}
}

func (t *Template) appendValue(buf []byte, v any, ph *placeholder) ([]byte, error) {
	if ph.quoted {
		lit, err := jsonLiteral(v)
		if err != nil {
			return nil, renderErr(ReasonTypeMismatch, ph)
		}
		return append(buf, lit...), nil
	}
	s, ok := stringForm(v)
	if !ok {
		return nil, renderErr(ReasonTypeMismatch, ph)
	}
	if t.ct != ContentTypeJSON {
		return append(buf, s...), nil
	}
	// Интерполяция внутрь JSON-строки: значение обязано быть экранировано, иначе
	// кавычка или перевод строки из тела запроса развалит ответ.
	esc, err := jsonLiteral(s)
	if err != nil {
		return nil, renderErr(ReasonTypeMismatch, ph)
	}
	return append(buf, esc[1:len(esc)-1]...), nil
}

// jsonLiteral кодирует значение без HTML-экранирования: <, > и & в эхо должны
// вернуться такими же, какими пришли.
func jsonLiteral(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// stringForm — представление значения для интерполяции в строку. Массивы и
// объекты не поддержаны: их место — quoted-подстановка целым JSON-значением.
func stringForm(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		return "", false
	}
}

// validateSkeleton проверяет, что шаблон в принципе способен собрать валидный
// JSON: каждая типизированная подстановка заменяется на null, строковая — на
// пустоту. Ловит `{"a": ${x}}` (подстановка без кавычек в JSON) и незакрытые
// скобки на сохранении узла, а не на боевом трафике.
func (t *Template) validateSkeleton() error {
	if t.ct != ContentTypeJSON {
		return nil
	}
	var b []byte
	for _, p := range t.parts {
		if p.ph == nil {
			b = append(b, p.text...)
			continue
		}
		if p.ph.quoted {
			b = append(b, "null"...)
		}
	}
	if !json.Valid(b) {
		return errSyntax("template does not produce valid JSON " +
			"(a placeholder outside quotes, or unbalanced brackets)")
	}
	return nil
}
