package ackspec_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/ackspec"
)

// sigurBody — реальное тело из боевого разбора §83: СКУД отдаёт пакет журнала и
// ждёт в ответ максимальный logId пакета.
const sigurBody = `{"logs":[{"logId":79154,"time":1786034092,"empId":"000004627","internalEmpId":640,"accessPoint":9,"direction":2,"keyHex":"7577E3"}]}`

func render(t *testing.T, tmpl string, ct ackspec.ContentType, c *ackspec.Ctx) (string, error) {
	t.Helper()
	compiled, err := ackspec.Compile(tmpl, ct)
	require.NoError(t, err)
	out, err := compiled.Render(c)
	return string(out), err
}

func jsonCtx(body string) *ackspec.Ctx {
	return &ackspec.Ctx{Body: []byte(body), ContentType: "application/json"}
}

// TestRender_Sigur — контрольный кейс раздела: ответ обязан совпасть байт в
// байт, включая пробел после двоеточия, а число обязано остаться числом.
func TestRender_Sigur(t *testing.T) {
	t.Parallel()

	got, err := render(t,
		`{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		ackspec.ContentTypeJSON, jsonCtx(sigurBody))

	require.NoError(t, err)
	assert.Equal(t, `{"confirmedLogId": 79154}`, got)
}

func TestRender_Values(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tmpl string
		body string
		want string
	}{
		{
			name: "max over batch",
			tmpl: `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
			body: `{"logs":[{"logId":10},{"logId":79154},{"logId":42}]}`,
			want: `{"confirmedLogId": 79154}`,
		},
		{
			name: "int64 beyond float53 survives",
			tmpl: `{"id": "${ body.id }"}`,
			body: `{"id":9007199254740993}`,
			want: `{"id": 9007199254740993}`,
		},
		{
			name: "count on empty batch is zero",
			tmpl: `{"accepted": "${ body.logs[*].logId | count }"}`,
			body: `{"logs":[]}`,
			want: `{"accepted": 0}`,
		},
		{
			name: "default fills missing path",
			tmpl: `{"cursor": "${ body.cursor | default(0) }"}`,
			body: `{"logs":[]}`,
			want: `{"cursor": 0}`,
		},
		{
			name: "default fills explicit null",
			tmpl: `{"cursor": "${ body.cursor | default(-1) }"}`,
			body: `{"cursor":null}`,
			want: `{"cursor": -1}`,
		},
		{
			name: "string value keeps quotes in typed mode",
			tmpl: `{"key": "${ body.keyHex }"}`,
			body: `{"keyHex":"7577E3"}`,
			want: `{"key": "7577E3"}`,
		},
		{
			name: "interpolation into a string",
			tmpl: `{"msg": "accepted ${ body.n } records"}`,
			body: `{"n":12}`,
			want: `{"msg": "accepted 12 records"}`,
		},
		{
			name: "interpolated value is escaped",
			tmpl: `{"msg": "got ${ body.s }"}`,
			body: `{"s":"a\"b\nc"}`,
			want: `{"msg": "got a\"b\nc"}`,
		},
		{
			name: "html characters are not escaped",
			tmpl: `{"s": "${ body.s }"}`,
			body: `{"s":"<a>&b"}`,
			want: `{"s": "<a>&b"}`,
		},
		{
			name: "explicit index",
			tmpl: `{"first": "${ body.logs[0].logId }"}`,
			body: `{"logs":[{"logId":7},{"logId":8}]}`,
			want: `{"first": 7}`,
		},
		{
			name: "quoted key with a dot",
			tmpl: `{"v": "${ body["a.b"].c }"}`,
			body: `{"a.b":{"c":5}}`,
			want: `{"v": 5}`,
		},
		{
			name: "first and last",
			tmpl: `{"f": "${ body.a[*] | first }", "l": "${ body.a[*] | last }"}`,
			body: `{"a":[1,2,3]}`,
			want: `{"f": 1, "l": 3}`,
		},
		{
			name: "min over mixed numbers",
			tmpl: `{"m": "${ body.a[*] | min }"}`,
			body: `{"a":[3,1.5,2]}`,
			want: `{"m": 1.5}`,
		},
		{
			name: "object substituted whole",
			tmpl: `{"echo": "${ body.meta }"}`,
			body: `{"meta":{"k":1}}`,
			want: `{"echo": {"k":1}}`,
		},
		{
			name: "escaped placeholder stays literal",
			tmpl: `{"tpl": "$${ body.x }"}`,
			body: `{}`,
			want: `{"tpl": "${ body.x }"}`,
		},
		{
			name: "constant template without body access",
			tmpl: `{"ok": true}`,
			body: `not json at all`,
			want: `{"ok": true}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := render(t, tc.tmpl, ackspec.ContentTypeJSON, jsonCtx(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRender_NonBodySources(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 6, 10, 30, 0, 0, time.UTC)
	ctx := &ackspec.Ctx{
		ContentType: "application/json",
		Body:        []byte(`{}`),
		Query:       url.Values{"code": {"abc", "def"}},
		PathSuffix:  "/2026/08",
		ID:          "11111111-2222-3333-4444-555555555555",
		Node:        "acs_sigur",
		Team:        "webhook",
		ReceivedAt:  at,
		Queued:      true,
	}

	cases := []struct{ name, tmpl, want string }{
		{"query first value", `{"c": "${ query.code }"}`, `{"c": "abc"}`},
		{"query indexed", `{"c": "${ query.code[1] }"}`, `{"c": "def"}`},
		{"query count", `{"n": "${ query.code[*] | count }"}`, `{"n": 2}`},
		{"query missing with default", `{"c": "${ query.none | default("x") }"}`, `{"c": "x"}`},
		{"path suffix", `{"p": "${ path.suffix }"}`, `{"p": "/2026/08"}`},
		{"path segment", `{"p": "${ path.segment[1] }"}`, `{"p": "08"}`},
		{"nexus id", `{"i": "${ nexus.id }"}`, `{"i": "11111111-2222-3333-4444-555555555555"}`},
		{"nexus node and team", `{"n": "${ nexus.node }/${ nexus.team }"}`, `{"n": "acs_sigur/webhook"}`},
		{"nexus queued is a bool", `{"q": "${ nexus.queued }"}`, `{"q": true}`},
		{"nexus received_at", `{"t": "${ nexus.received_at }"}`, `{"t": "2026-08-06T10:30:00Z"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := render(t, tc.tmpl, ackspec.ContentTypeJSON, ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRender_TextPlain(t *testing.T) {
	t.Parallel()

	t.Run("bare confirmation string", func(t *testing.T) {
		t.Parallel()
		got, err := render(t, `${ query.code }`, ackspec.ContentTypeText,
			&ackspec.Ctx{Query: url.Values{"code": {"vk-confirm-42"}}})
		require.NoError(t, err)
		assert.Equal(t, "vk-confirm-42", got)
	})

	t.Run("quotes stay literal in text mode", func(t *testing.T) {
		t.Parallel()
		got, err := render(t, `"${ body.id }"`, ackspec.ContentTypeText, jsonCtx(`{"id":7}`))
		require.NoError(t, err)
		assert.Equal(t, `"7"`, got)
	})
}

func TestRender_Failures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		tmpl   string
		ctx    *ackspec.Ctx
		reason ackspec.Reason
	}{
		{
			name:   "path not found",
			tmpl:   `{"a": "${ body.missing }"}`,
			ctx:    jsonCtx(`{"present":1}`),
			reason: ackspec.ReasonPathNotFound,
		},
		{
			name:   "ambiguous without function",
			tmpl:   `{"a": "${ body.logs[*].logId }"}`,
			ctx:    jsonCtx(`{"logs":[{"logId":1},{"logId":2}]}`),
			reason: ackspec.ReasonAmbiguous,
		},
		{
			name:   "max over strings",
			tmpl:   `{"a": "${ body.logs[*].k | max }"}`,
			ctx:    jsonCtx(`{"logs":[{"k":"a"},{"k":"b"}]}`),
			reason: ackspec.ReasonTypeMismatch,
		},
		{
			name:   "object into a string",
			tmpl:   `{"a": "x${ body.meta }"}`,
			ctx:    jsonCtx(`{"meta":{"k":1}}`),
			reason: ackspec.ReasonTypeMismatch,
		},
		{
			name:   "multipart body",
			tmpl:   `{"a": "${ body.x }"}`,
			ctx:    &ackspec.Ctx{Body: []byte("--b\r\n"), ContentType: "multipart/form-data; boundary=b"},
			reason: ackspec.ReasonBodyNotJSON,
		},
		{
			name:   "broken json body",
			tmpl:   `{"a": "${ body.x }"}`,
			ctx:    jsonCtx(`{"x":`),
			reason: ackspec.ReasonBodyNotJSON,
		},
		{
			name:   "trailing garbage after json",
			tmpl:   `{"a": "${ body.x }"}`,
			ctx:    jsonCtx(`{"x":1} trailing`),
			reason: ackspec.ReasonBodyNotJSON,
		},
		{
			name:   "empty body",
			tmpl:   `{"a": "${ body.x }"}`,
			ctx:    jsonCtx(``),
			reason: ackspec.ReasonBodyNotJSON,
		},
		{
			name:   "body over parse limit",
			tmpl:   `{"a": "${ body.x }"}`,
			ctx:    jsonCtx(`{"x":"` + strings.Repeat("y", ackspec.MaxParseBodyBytes) + `"}`),
			reason: ackspec.ReasonBodyTooLarge,
		},
		{
			name:   "too many selected values",
			tmpl:   `{"a": "${ body.a[*] | count }"}`,
			ctx:    jsonCtx(`{"a":[` + strings.TrimSuffix(strings.Repeat("1,", ackspec.MaxSelectedValues+1), ",") + `]}`),
			reason: ackspec.ReasonTooManyValues,
		},
		{
			name:   "output over limit",
			tmpl:   `{"a": "${ body.big }"}`,
			ctx:    jsonCtx(`{"big":"` + strings.Repeat("z", ackspec.MaxOutputBytes+10) + `"}`),
			reason: ackspec.ReasonOutputTooBig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := render(t, tc.tmpl, ackspec.ContentTypeJSON, tc.ctx)
			require.Error(t, err)
			require.ErrorIs(t, err, ackspec.ErrRenderFailed)
			var re *ackspec.RenderError
			require.ErrorAs(t, err, &re)
			assert.Equal(t, tc.reason, re.Reason)
		})
	}
}

// TestRender_NoCredentialLeak фиксирует границу безопасности словами теста:
// в Ctx кладут ТОЛЬКО очищенные тело и query, поэтому вырезанного поля креды
// в них уже нет и шаблон достать его не может.
func TestRender_NoCredentialLeak(t *testing.T) {
	t.Parallel()

	// Тело после §41-очистки: поля token в нём больше нет.
	_, err := render(t, `{"t": "${ body.token }"}`, ackspec.ContentTypeJSON, jsonCtx(`{"payload":1}`))
	require.Error(t, err)
	var re *ackspec.RenderError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, ackspec.ReasonPathNotFound, re.Reason)
}
