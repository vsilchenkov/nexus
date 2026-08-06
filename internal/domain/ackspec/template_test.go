package ackspec_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/ackspec"
)

func TestCompile_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tmpl string
		ct   ackspec.ContentType
	}{
		{"unterminated placeholder", `{"a": "${ body.x"}`, ackspec.ContentTypeJSON},
		{"no source separator", `{"a": "${ body }"}`, ackspec.ContentTypeJSON},
		{"unknown source", `{"a": "${ header.authorization }"}`, ackspec.ContentTypeJSON},
		{"unknown function", `{"a": "${ body.x | sum }"}`, ackspec.ContentTypeJSON},
		{"empty path", `{"a": "${ body. }"}`, ackspec.ContentTypeJSON},
		{"double dot", `{"a": "${ body.a..b }"}`, ackspec.ContentTypeJSON},
		{"negative index", `{"a": "${ body.logs[-1].id }"}`, ackspec.ContentTypeJSON},
		{"bad index", `{"a": "${ body.logs[x] }"}`, ackspec.ContentTypeJSON},
		{"unterminated index", `{"a": "${ body.logs[0 }"}`, ackspec.ContentTypeJSON},
		{"default without literal", `{"a": "${ body.x | default() }"}`, ackspec.ContentTypeJSON},
		{"default with junk literal", `{"a": "${ body.x | default(zz) }"}`, ackspec.ContentTypeJSON},
		{"unknown nexus field", `{"a": "${ nexus.secret }"}`, ackspec.ContentTypeJSON},
		{"path.suffix with index", `{"a": "${ path.suffix[0] }"}`, ackspec.ContentTypeJSON},
		{"path.segment without index", `{"a": "${ path.segment }"}`, ackspec.ContentTypeJSON},
		{"query with nested key", `{"a": "${ query.a.b }"}`, ackspec.ContentTypeJSON},
		{"unquoted placeholder breaks json", `{"a": ${ body.x }}`, ackspec.ContentTypeJSON},
		{"unbalanced braces", `{"a": "${ body.x }"`, ackspec.ContentTypeJSON},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpl, err := ackspec.Compile(tc.tmpl, tc.ct)
			if err == nil {
				// Часть ошибок ловится не компиляцией, а скелетной проверкой
				// внутри Spec.Validate — она обязана их поймать.
				s := &ackspec.Spec{Version: ackspec.Version, ContentType: tc.ct, Body: tc.tmpl, OnError: ackspec.OnErrorDefault}
				err = s.Validate()
				require.Error(t, err, "neither Compile nor Validate rejected %q", tc.tmpl)
			}
			assert.True(t, errors.Is(err, ackspec.ErrTemplateSyntax),
				"want ErrTemplateSyntax, got %v (tmpl=%q)", err, tc.tmpl)
			_ = tmpl
		})
	}
}

func TestCompile_Limits(t *testing.T) {
	t.Parallel()

	t.Run("template too large", func(t *testing.T) {
		t.Parallel()
		_, err := ackspec.Compile(strings.Repeat("x", ackspec.MaxTemplateBytes+1), ackspec.ContentTypeText)
		require.ErrorIs(t, err, ackspec.ErrTemplateSyntax)
	})

	t.Run("too many placeholders", func(t *testing.T) {
		t.Parallel()
		one := `${ nexus.id }`
		_, err := ackspec.Compile(strings.Repeat(one, ackspec.MaxPlaceholders+1), ackspec.ContentTypeText)
		require.ErrorIs(t, err, ackspec.ErrTemplateSyntax)
	})

	t.Run("path too deep", func(t *testing.T) {
		t.Parallel()
		deep := "${ body." + strings.Repeat("a.", ackspec.MaxPathDepth) + "b }"
		_, err := ackspec.Compile(deep, ackspec.ContentTypeText)
		require.ErrorIs(t, err, ackspec.ErrTemplateSyntax)
	})

	t.Run("unknown content type", func(t *testing.T) {
		t.Parallel()
		_, err := ackspec.Compile("ok", ackspec.ContentType("application/xml"))
		require.ErrorIs(t, err, ackspec.ErrSpecInvalid)
	})
}

func TestCompile_Uses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tmpl string
		want ackspec.SourceSet
	}{
		{"constant template needs nothing", `{"ok": true}`, 0},
		{"body only", `{"a": "${ body.x }"}`, ackspec.SourceBody},
		{"nexus only", `{"a": "${ nexus.id }"}`, ackspec.SourceNexus},
		{"body and query", `{"a": "${ body.x }", "b": "${ query.c }"}`, ackspec.SourceBody | ackspec.SourceQuery},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpl, err := ackspec.Compile(tc.tmpl, ackspec.ContentTypeJSON)
			require.NoError(t, err)
			assert.Equal(t, tc.want, tmpl.Uses())
			// Uses — не украшение: по нему рендер решает, парсить ли тело.
			assert.Equal(t, tc.want.Has(ackspec.SourceBody), tmpl.Uses().Has(ackspec.SourceBody))
		})
	}
}
