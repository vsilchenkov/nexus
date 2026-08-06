package ackspec_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"nexus/internal/domain/ackspec"
)

// FuzzCompile — компилятор разбирает текст, который пишет человек в форме узла,
// поэтому обязан переживать любой ввод: паника уронила бы сохранение узла, а
// ошибка не того класса — не подсветила бы поле формы (§8 CLAUDE.md: fuzz для
// парсеров обязателен).
func FuzzCompile(f *testing.F) {
	seeds := []string{
		`{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		`${ query.code }`,
		`{"a": "${ body["a.b"].c | default("x") }"}`,
		`$${ body.x }`,
		`${`,
		`${ }`,
		`${ body.a[`,
		`${ body.a | default(} }`,
		`"${ nexus.queued }"`,
		``,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, tmpl string) {
		for _, ct := range []ackspec.ContentType{ackspec.ContentTypeJSON, ackspec.ContentTypeText} {
			compiled, err := ackspec.Compile(tmpl, ct)
			switch {
			case err != nil:
				if !errors.Is(err, ackspec.ErrTemplateSyntax) {
					t.Fatalf("Compile(%q, %s) = %v; want ErrTemplateSyntax", tmpl, ct, err)
				}
			case compiled == nil:
				t.Fatalf("Compile(%q, %s) returned nil template without error", tmpl, ct)
			}
		}
	})
}

// FuzzRender — тело приходит из интернета. Рендер обязан не паниковать, не
// превышать потолок ответа и не отдавать битый JSON под заголовком JSON: иначе
// шина сама сломает клиента, который до §83 работал.
func FuzzRender(f *testing.F) {
	seeds := []string{
		sigurBody,
		`{"logs":[]}`,
		`{"a":[1,2,3]}`,
		`{"a":{"b":{"c":1}}}`,
		`[1,2,3]`,
		`"just a string"`,
		`null`,
		`{`,
		``,
		`{"id":9007199254740993}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	templates := []string{
		`{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		`{"n": "${ body.a[*] | count }", "first": "${ body.a[*] | first }"}`,
		`{"echo": "${ body.a }", "msg": "got ${ body.a }"}`,
		`{"x": "${ body.missing | default(0) }"}`,
		`{"deep": "${ body.a.b.c }"}`,
	}
	compiled := make([]*ackspec.Template, 0, len(templates))
	for _, tmpl := range templates {
		t, err := ackspec.Compile(tmpl, ackspec.ContentTypeJSON)
		if err != nil {
			f.Fatalf("seed template %q does not compile: %v", tmpl, err)
		}
		compiled = append(compiled, t)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		ctx := &ackspec.Ctx{
			Body:        body,
			ContentType: "application/json",
			Query:       url.Values{"code": {"a", "b"}},
			PathSuffix:  "/x/y",
			ID:          "id",
			Node:        "node",
			Team:        "team",
		}
		for _, tmpl := range compiled {
			out, err := tmpl.Render(ctx)
			if err != nil {
				var re *ackspec.RenderError
				if !errors.As(err, &re) {
					t.Fatalf("Render returned %T (%v); want *RenderError", err, err)
				}
				if !errors.Is(err, ackspec.ErrRenderFailed) {
					t.Fatalf("RenderError does not unwrap to ErrRenderFailed: %v", err)
				}
				continue
			}
			if len(out) > ackspec.MaxOutputBytes {
				t.Fatalf("rendered %d bytes, limit is %d", len(out), ackspec.MaxOutputBytes)
			}
			if !json.Valid(out) {
				t.Fatalf("rendered invalid JSON under application/json: %q", out)
			}
		}
	})
}
