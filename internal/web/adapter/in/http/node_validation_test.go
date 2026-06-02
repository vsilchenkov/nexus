package http

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
)

// TestNodeValidationCode_AllMapped — каждая запись карты даёт непустые code и
// field, помечается как validation-ошибка и имеет перевод в обоих языках (а не
// возврат самого ключа, что означало бы пропущенный i18n-ключ).
func TestNodeValidationCode_AllMapped(t *testing.T) {
	t.Parallel()
	for _, e := range nodeValidationErrors {
		code, field, ok := nodeValidationCode(e.err)
		assert.True(t, ok, "errors.Is must match its own sentinel: %v", e.err)
		assert.NotEmpty(t, code, "code for %v", e.err)
		assert.NotEmpty(t, field, "field for %v", e.err)
		assert.True(t, isValidationError(e.err), "isValidationError must be true for %v", e.err)
		for _, lang := range []i18n.Lang{i18n.LangEN, i18n.LangRU} {
			assert.NotEqual(t, code, i18n.Translate(lang, code),
				"missing %s translation for %s", lang, code)
		}
	}
}

// TestNodeValidationCode_Unknown — посторонняя ошибка не считается валидацией.
func TestNodeValidationCode_Unknown(t *testing.T) {
	t.Parallel()
	_, _, ok := nodeValidationCode(domain.ErrNodeNotFound)
	assert.False(t, ok)
	assert.False(t, isValidationError(domain.ErrNodeNotFound))
}
