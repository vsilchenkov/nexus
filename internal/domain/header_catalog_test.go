package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestHeaderCatalogEntry_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entry   HeaderCatalogEntry
		wantErr error
	}{
		{"simple ok", HeaderCatalogEntry{Name: "X-Request-ID"}, nil},
		{"token symbols ok", HeaderCatalogEntry{Name: "X-Custom!#$%&'*+.^_`|~-"}, nil},
		{"lowercase ok", HeaderCatalogEntry{Name: "content-type"}, nil},
		{"empty", HeaderCatalogEntry{Name: ""}, ErrHeaderNameLength},
		{"too long", HeaderCatalogEntry{Name: strings.Repeat("a", 101)}, ErrHeaderNameLength},
		{"space rejected", HeaderCatalogEntry{Name: "X My Header"}, ErrHeaderNameFormat},
		{"colon rejected", HeaderCatalogEntry{Name: "X-Foo:bar"}, ErrHeaderNameFormat},
		{"description too long", HeaderCatalogEntry{Name: "X-Foo", Description: strings.Repeat("d", 501)}, ErrHeaderDescriptionLength},
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
