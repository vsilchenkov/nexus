package sensitive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSensitive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"exact password", "password", true},
		{"exact token", "token", true},
		{"case insensitive", "PASSWORD", true},
		{"mixed case header", "X-Api-Key", true},
		{"contains substring", "user_password", true},
		{"contains token", "access_token", true},
		{"authorization", "Authorization", true},
		{"set-cookie", "Set-Cookie", true},
		{"plain node", "node", false},
		{"plain status", "status", false},
		{"plain url", "url", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsSensitive(tc.key))
		})
	}
}

func TestKeys_NotEmptyAndCopied(t *testing.T) {
	t.Parallel()
	ks := Keys()
	assert.NotEmpty(t, ks)
	// Возвращается копия: мутация результата не портит источник.
	ks[0] = "mutated"
	assert.NotEqual(t, "mutated", Keys()[0])
}
