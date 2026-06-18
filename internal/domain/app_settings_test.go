package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePublicBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty ok", "", false},
		{"https origin", "https://nexus.example.com", false},
		{"http origin with port", "http://localhost:8000", false},
		{"trailing slash", "https://nexus.example.com/", true},
		{"with path", "https://nexus.example.com/api", true},
		{"with query", "https://nexus.example.com?x=1", true},
		{"no scheme", "nexus.example.com", true},
		{"ftp scheme", "ftp://nexus.example.com", true},
		{"scheme only", "https://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePublicBaseURL(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrPublicBaseURLInvalid)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateSessionTTLSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      int
		wantErr bool
	}{
		{"below min", SessionTTLMinSeconds - 1, true},
		{"at min", SessionTTLMinSeconds, false},
		{"middle", 86400, false},
		{"at max", SessionTTLMaxSeconds, false},
		{"above max", SessionTTLMaxSeconds + 1, true},
		{"zero", 0, true},
		{"negative", -10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSessionTTLSeconds(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrSessionTTLInvalid)
				return
			}
			assert.NoError(t, err)
		})
	}
}
