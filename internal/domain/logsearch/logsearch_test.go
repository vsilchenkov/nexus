package logsearch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/logsearch"
)

// flatTerm — сравнимая проекция Term для table-driven проверок разбора.
type flatTerm struct {
	field  logsearch.Field
	negate bool
	text   string
}

func flatten(e *logsearch.Expr) [][]flatTerm {
	out := make([][]flatTerm, 0, len(e.Groups))
	for _, g := range e.Groups {
		fg := make([]flatTerm, 0, len(g))
		for _, t := range g {
			fg = append(fg, flatTerm{field: t.Field, negate: t.Negate, text: t.Text})
		}
		out = append(out, fg)
	}
	return out
}

func TestParse_Grammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		q    string
		want [][]flatTerm
	}{
		{
			name: "single term",
			q:    "foo",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "foo"}}},
		},
		{
			name: "and",
			q:    "a & b",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "a"}, {logsearch.FieldAll, false, "b"}}},
		},
		{
			name: "or",
			q:    "a | b",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "a"}}, {{logsearch.FieldAll, false, "b"}}},
		},
		{
			name: "precedence: and binds tighter",
			q:    "a & b | c",
			want: [][]flatTerm{
				{{logsearch.FieldAll, false, "a"}, {logsearch.FieldAll, false, "b"}},
				{{logsearch.FieldAll, false, "c"}},
			},
		},
		{
			name: "negation",
			q:    "error & -timeout",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "error"}, {logsearch.FieldAll, true, "timeout"}}},
		},
		{
			name: "prefix url",
			q:    "url:/v1/orders",
			want: [][]flatTerm{{{logsearch.FieldURL, false, "/v1/orders"}}},
		},
		{
			name: "prefix params",
			q:    "params:id=42",
			want: [][]flatTerm{{{logsearch.FieldParams, false, "id=42"}}},
		},
		{
			name: "prefix req",
			q:    "req:hello",
			want: [][]flatTerm{{{logsearch.FieldRequest, false, "hello"}}},
		},
		{
			name: "prefix resp",
			q:    "resp:fail",
			want: [][]flatTerm{{{logsearch.FieldResponse, false, "fail"}}},
		},
		{
			name: "prefix case-insensitive",
			q:    "URL:foo",
			want: [][]flatTerm{{{logsearch.FieldURL, false, "foo"}}},
		},
		{
			name: "negated prefixed term",
			q:    "-resp:foo",
			want: [][]flatTerm{{{logsearch.FieldResponse, true, "foo"}}},
		},
		{
			name: "unknown prefix is literal",
			q:    "status:500",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "status:500"}}},
		},
		{
			name: "url scheme colon is literal",
			q:    "http://host/path",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "http://host/path"}}},
		},
		{
			name: "inner dash is literal",
			q:    "x-request-id",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "x-request-id"}}},
		},
		{
			name: "inner spaces significant, edges trimmed",
			q:    "  foo bar  ",
			want: [][]flatTerm{{{logsearch.FieldAll, false, "foo bar"}}},
		},
		{
			name: "combined: (url:a AND NOT b) OR params:c OR NOT resp:d",
			q:    "url:a & -b | params:c | -resp:d",
			want: [][]flatTerm{
				{{logsearch.FieldURL, false, "a"}, {logsearch.FieldAll, true, "b"}},
				{{logsearch.FieldParams, false, "c"}},
				{{logsearch.FieldResponse, true, "d"}},
			},
		},
		{
			name: "three AND terms in one group",
			q:    "a & b & c",
			want: [][]flatTerm{{
				{logsearch.FieldAll, false, "a"},
				{logsearch.FieldAll, false, "b"},
				{logsearch.FieldAll, false, "c"},
			}},
		},
		{
			name: "negated prefixed with escape inside text",
			q:    `-url:a\&b`,
			want: [][]flatTerm{{{logsearch.FieldURL, true, "a&b"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := logsearch.Parse(tt.q, logsearch.Options{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, flatten(e))
		})
	}
}

func TestParse_Escaping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		q    string
		want [][]flatTerm
	}{
		{
			name: "escaped ampersand",
			q:    `a\&b`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, "a&b"}}},
		},
		{
			name: "escaped pipe",
			q:    `a\|b`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, "a|b"}}},
		},
		{
			name: "escaped leading dash",
			q:    `\-term`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, "-term"}}},
		},
		{
			name: "escaped colon disables prefix",
			q:    `url\:foo`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, "url:foo"}}},
		},
		{
			name: "escaped backslash",
			q:    `a\\b`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, `a\b`}}},
		},
		{
			name: "escaped ordinary char is literal",
			q:    `\x`,
			want: [][]flatTerm{{{logsearch.FieldAll, false, "x"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := logsearch.Parse(tt.q, logsearch.Options{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, flatten(e))
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		q    string
		opts logsearch.Options
	}{
		{name: "empty and term", q: "a & & b"},
		{name: "leading or", q: "| a"},
		{name: "trailing and", q: "a &"},
		{name: "prefix without text", q: "url:"},
		{name: "only spaces", q: "   "},
		{name: "bare negation", q: "-"},
		{name: "trailing backslash", q: `foo\`},
		{name: "invalid regex", q: "(", opts: logsearch.Options{Regex: true}},
		{name: "empty regex", q: "  ", opts: logsearch.Options{Regex: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := logsearch.Parse(tt.q, tt.opts)
			require.Error(t, err)
			assert.ErrorIs(t, err, logsearch.ErrBadQuery)
		})
	}
}

func TestExprMatch(t *testing.T) {
	t.Parallel()
	// Одна «запись лога» на все кейсы: матчинг проверяется против неё.
	const (
		urlV    = "/v1/GetParcelsInfo"
		paramsV = "id=42&token=Secret"
		reqV    = `{"query":"Привет, мир"}`
		respV   = `{"status":"concatenated OK"}`
	)
	tests := []struct {
		name string
		q    string
		opts logsearch.Options
		want bool
	}{
		{name: "hit in url", q: "parcels", want: true},
		{name: "hit in params only", q: "token=", want: true},
		{name: "hit in request body", q: "мир", want: true},
		{name: "miss everywhere", q: "nothing-here", want: false},
		{name: "and both present", q: "parcels & token", want: true},
		{name: "and one missing", q: "parcels & nope", want: false},
		{name: "or one present", q: "nope | parcels", want: true},
		{name: "precedence", q: "nope & parcels | token", want: true},
		{name: "negate present term", q: "-parcels", want: false},
		{name: "negate absent term", q: "-nothing", want: true},
		{name: "negate scoped: absent in url", q: "-url:token", want: true},
		{name: "negate scoped: present in params", q: "-params:token", want: false},
		{name: "field scope hit", q: "url:parcels", want: true},
		{name: "field scope miss (body-only text)", q: "url:мир", want: false},
		{name: "resp scope", q: "resp:concatenated", want: true},
		{name: "case-insensitive default (cyrillic)", q: "ПРИВЕТ", want: true},
		{name: "case-sensitive miss", q: "PARCELS", opts: logsearch.Options{CaseSensitive: true}, want: false},
		{name: "case-sensitive hit", q: "GetParcels", opts: logsearch.Options{CaseSensitive: true}, want: true},
		{name: "whole word miss inside word", q: "concat", opts: logsearch.Options{WholeWord: true}, want: false},
		{name: "whole word hit", q: "concatenated", opts: logsearch.Options{WholeWord: true}, want: true},
		{name: "whole word case-insensitive", q: "CONCATENATED", opts: logsearch.Options{WholeWord: true}, want: true},
		{name: "regex alternation", q: "parcels|nothing", opts: logsearch.Options{Regex: true}, want: true},
		{name: "regex anchored miss", q: "^token", opts: logsearch.Options{Regex: true}, want: false},
		{name: "regex case-insensitive cyrillic", q: "привет.*мир", opts: logsearch.Options{Regex: true}, want: true},
		{name: "regex case-sensitive miss", q: "PARCELS", opts: logsearch.Options{Regex: true, CaseSensitive: true}, want: false},
		{name: "regex whole word", q: "concat", opts: logsearch.Options{Regex: true, WholeWord: true}, want: false},
		{name: "regex minilanguage disabled", q: `id=42\&token`, opts: logsearch.Options{Regex: true}, want: true},
		{name: "combined field AND negation: both hold", q: "url:parcels & -resp:failure", want: true},
		{name: "combined field AND negation: negated present", q: "url:parcels & -resp:ok", want: false},
		{name: "three OR groups, last hits", q: "nope1 | nope2 | params:token", want: true},
		{name: "AND of three terms", q: "parcels & token & мир", want: true},
		{name: "AND of three, one missing", q: "parcels & token & nope", want: false},
		{name: "word mode with field scope", q: "resp:OK", opts: logsearch.Options{WholeWord: true}, want: true},
		{name: "word mode with field scope, substring only", q: "resp:conca", opts: logsearch.Options{WholeWord: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := logsearch.Parse(tt.q, tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, e.Match(urlV, paramsV, reqV, respV))
		})
	}
}
