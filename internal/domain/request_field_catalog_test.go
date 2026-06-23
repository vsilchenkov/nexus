package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestRequestFieldCatalogEntry_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry RequestFieldCatalogEntry
		want  error
	}{
		{"valid simple", RequestFieldCatalogEntry{Name: "token"}, nil},
		{"valid header-like", RequestFieldCatalogEntry{Name: "X-Api-Key"}, nil},
		{"valid Authorization", RequestFieldCatalogEntry{Name: "Authorization"}, nil},
		{"valid underscore", RequestFieldCatalogEntry{Name: "api_key"}, nil},
		{"empty", RequestFieldCatalogEntry{Name: ""}, ErrRequestFieldNameLength},
		{"too long", RequestFieldCatalogEntry{Name: strings.Repeat("a", 65)}, ErrRequestFieldNameLength},
		{"leading digit", RequestFieldCatalogEntry{Name: "1bad"}, ErrRequestFieldNameFormat},
		{"space", RequestFieldCatalogEntry{Name: "bad name"}, ErrRequestFieldNameFormat},
		{"slash", RequestFieldCatalogEntry{Name: "a/b"}, ErrRequestFieldNameFormat},
		{"desc too long", RequestFieldCatalogEntry{Name: "ok", Description: strings.Repeat("d", 501)}, ErrRequestFieldDescriptionLength},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.entry.Validate()
			if c.want == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}
