package usecase

import "testing"

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"valid", "Sw0rdfish!", false},
		{"valid with inner space", "two words pass", false},
		{"exactly 8", "abcd1234", false},
		{"empty", "", true},
		{"single space", " ", true},
		{"eight spaces (П19)", "        ", true},
		{"too short", "abc123", true},
		{"seven chars plus trailing space", "abcdef1 ", true},
		{"whitespace tabs", "\t\t\t\t", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePassword(tt.pw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validatePassword(%q) err=%v, wantErr=%v", tt.pw, err, tt.wantErr)
			}
		})
	}
}
