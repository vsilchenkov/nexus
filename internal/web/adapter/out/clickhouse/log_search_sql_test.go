package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/logsearch"
)

// TestExprConds — генерация WHERE из AST мини-языка (§48): И/ИЛИ/НЕ,
// скоуп на конкретное поле, режимы Aa/ab|/.*, порядок позиционных args.
func TestExprConds(t *testing.T) {
	t.Parallel()

	// Сокращения ожидаемых условий.
	const (
		ciURL  = "positionCaseInsensitiveUTF8(url, ?) > 0"
		ciPar  = "positionCaseInsensitiveUTF8(parameters, ?) > 0"
		ciReq  = "positionCaseInsensitiveUTF8(request, ?) > 0"
		ciResp = "positionCaseInsensitiveUTF8(response, ?) > 0"
		all4ci = "(" + ciURL + " OR " + ciPar + " OR " + ciReq + " OR " + ciResp + ")"
	)

	tests := []struct {
		name     string
		q        string
		opts     logsearch.Options
		wantCond string
		wantArgs []any
	}{
		{
			name:     "один plain-терм — 4 колонки, регистронезависимо",
			q:        "foo",
			wantCond: "(" + all4ci + ")",
			wantArgs: []any{"foo", "foo", "foo", "foo"},
		},
		{
			name:     "Aa: регистрозависимый position",
			q:        "url:Foo",
			opts:     logsearch.Options{CaseSensitive: true},
			wantCond: "((positionUTF8(url, ?) > 0))",
			wantArgs: []any{"Foo"},
		},
		{
			name:     "скоуп на конкретное поле — одна колонка",
			q:        "params:id=42",
			wantCond: "((" + ciPar + "))",
			wantArgs: []any{"id=42"},
		},
		{
			name:     "И: два терма в одной группе",
			q:        "a & url:b",
			wantCond: "(" + all4ci + " AND (" + ciURL + "))",
			wantArgs: []any{"a", "a", "a", "a", "b"},
		},
		{
			name:     "ИЛИ: две группы",
			q:        "url:a | resp:b",
			wantCond: "(((" + ciURL + ")) OR ((" + ciResp + ")))",
			wantArgs: []any{"a", "b"},
		},
		{
			name:     "НЕ: негация терма",
			q:        "-req:x",
			wantCond: "(NOT (" + ciReq + "))",
			wantArgs: []any{"x"},
		},
		{
			name:     "НЕ по всем колонкам",
			q:        "-x",
			wantCond: "(NOT " + all4ci + ")",
			wantArgs: []any{"x", "x", "x", "x"},
		},
		{
			name: "комбинация: (a И НЕ b) ИЛИ (url:c)",
			q:    "a & -b | url:c",
			wantCond: "((" + all4ci + " AND NOT " + all4ci + ") OR " +
				"((" + ciURL + ")))",
			wantArgs: []any{"a", "a", "a", "a", "b", "b", "b", "b", "c"},
		},
		{
			name:     "ab|: целое слово → match() с готовым паттерном",
			q:        "url:cat",
			opts:     logsearch.Options{WholeWord: true},
			wantCond: "((match(url, ?)))",
			wantArgs: []any{`(?i)\b(?:cat)\b`},
		},
		{
			name: ".*: regex — одно выражение по 4 колонкам",
			q:    "sta(tus|te)",
			opts: logsearch.Options{Regex: true},
			wantCond: "((match(url, ?) OR match(parameters, ?) OR " +
				"match(request, ?) OR match(response, ?)))",
			wantArgs: []any{"(?i)sta(tus|te)", "(?i)sta(tus|te)", "(?i)sta(tus|te)", "(?i)sta(tus|te)"},
		},
		{
			name:     ".* + Aa + ab|: паттерн без (?i), с \\b",
			q:        "cat",
			opts:     logsearch.Options{Regex: true, CaseSensitive: true, WholeWord: true},
			wantCond: "((match(url, ?) OR match(parameters, ?) OR match(request, ?) OR match(response, ?)))",
			wantArgs: []any{`\b(?:cat)\b`, `\b(?:cat)\b`, `\b(?:cat)\b`, `\b(?:cat)\b`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := logsearch.Parse(tt.q, tt.opts)
			require.NoError(t, err)
			cond, args := exprConds(e)
			assert.Equal(t, tt.wantCond, cond)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}
