package domain_test

import (
	"errors"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestLogMaskPattern_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entry   domain.LogMaskPattern
		wantErr error
	}{
		{
			name:  "valid",
			entry: domain.LogMaskPattern{Pattern: `(bot[0-9]+):\w+`, Replacement: "$1:***", Description: "tg", SortOrder: 10},
		},
		{
			name:    "empty pattern",
			entry:   domain.LogMaskPattern{Pattern: "", Replacement: "***"},
			wantErr: domain.ErrLogMaskPatternLength,
		},
		{
			name:    "pattern too long",
			entry:   domain.LogMaskPattern{Pattern: strings.Repeat("a", 501), Replacement: "***"},
			wantErr: domain.ErrLogMaskPatternLength,
		},
		{
			name:    "invalid regex",
			entry:   domain.LogMaskPattern{Pattern: "broken(", Replacement: "***"},
			wantErr: domain.ErrLogMaskPatternInvalid,
		},
		{
			name:    "replacement too long",
			entry:   domain.LogMaskPattern{Pattern: "x", Replacement: strings.Repeat("*", 201)},
			wantErr: domain.ErrLogMaskReplacementLength,
		},
		{
			name:    "description too long",
			entry:   domain.LogMaskPattern{Pattern: "x", Replacement: "***", Description: strings.Repeat("d", 501)},
			wantErr: domain.ErrLogMaskDescriptionLength,
		},
		{
			name:    "negative sort_order",
			entry:   domain.LogMaskPattern{Pattern: "x", Replacement: "***", SortOrder: -1},
			wantErr: domain.ErrLogMaskSortOrderNegative,
		},
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
