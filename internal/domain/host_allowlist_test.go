package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestHostAllowlistEntry_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entry   HostAllowlistEntry
		wantErr error
	}{
		{"exact ok", HostAllowlistEntry{Pattern: "api.partner.com", Kind: HostKindExact}, nil},
		{"exact single label", HostAllowlistEntry{Pattern: "localhost", Kind: HostKindExact}, nil},
		{"exact uppercase ok", HostAllowlistEntry{Pattern: "API.Partner.COM", Kind: HostKindExact}, nil},
		{"exact with scheme rejected", HostAllowlistEntry{Pattern: "https://api.partner.com", Kind: HostKindExact}, ErrHostExactFormat},
		{"exact with port rejected", HostAllowlistEntry{Pattern: "api.partner.com:443", Kind: HostKindExact}, ErrHostExactFormat},
		{"exact with path rejected", HostAllowlistEntry{Pattern: "api.partner.com/hook", Kind: HostKindExact}, ErrHostExactFormat},
		{"exact consecutive dots rejected", HostAllowlistEntry{Pattern: "api..partner.com", Kind: HostKindExact}, ErrHostExactFormat},
		{"exact trailing dot rejected", HostAllowlistEntry{Pattern: "api.partner.com.", Kind: HostKindExact}, ErrHostExactFormat},
		{"exact leading hyphen rejected", HostAllowlistEntry{Pattern: "-api.partner.com", Kind: HostKindExact}, ErrHostExactFormat},

		{"wildcard ok", HostAllowlistEntry{Pattern: "*.partner.com", Kind: HostKindWildcard}, nil},
		{"wildcard no prefix rejected", HostAllowlistEntry{Pattern: "partner.com", Kind: HostKindWildcard}, ErrHostWildcardFormat},
		{"wildcard bare star rejected", HostAllowlistEntry{Pattern: "*.", Kind: HostKindWildcard}, ErrHostWildcardFormat},
		{"wildcard inner star rejected", HostAllowlistEntry{Pattern: "api.*.com", Kind: HostKindWildcard}, ErrHostWildcardFormat},

		{"regex ok", HostAllowlistEntry{Pattern: `^api-\d+\.legacy\.io$`, Kind: HostKindRegex}, nil},
		{"regex invalid", HostAllowlistEntry{Pattern: `^api-(\d+`, Kind: HostKindRegex}, ErrHostRegexInvalid},

		{"invalid kind", HostAllowlistEntry{Pattern: "api.partner.com", Kind: "glob"}, ErrHostInvalidKind},
		{"empty pattern", HostAllowlistEntry{Pattern: "", Kind: HostKindExact}, ErrHostPatternLength},
		{"pattern too long", HostAllowlistEntry{Pattern: strings.Repeat("a", 513), Kind: HostKindRegex}, ErrHostPatternLength},
		{"description too long", HostAllowlistEntry{Pattern: "api.partner.com", Kind: HostKindExact, Description: strings.Repeat("d", 501)}, ErrHostDescriptionLength},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.entry.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestHostAllowlistEntry_EncodedPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   HostAllowlistEntry
		want string
	}{
		{"exact unchanged", HostAllowlistEntry{Pattern: "api.partner.com", Kind: HostKindExact}, "api.partner.com"},
		{"wildcard unchanged", HostAllowlistEntry{Pattern: "*.partner.com", Kind: HostKindWildcard}, "*.partner.com"},
		{"regex prefixed", HostAllowlistEntry{Pattern: `^x$`, Kind: HostKindRegex}, `re:^x$`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.EncodedPattern(); got != tc.want {
				t.Errorf("EncodedPattern() = %q, want %q", got, tc.want)
			}
		})
	}
}
