package domain

import (
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestAPIToken_IsActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tok  APIToken
		want bool
	}{
		{
			name: "fresh, never expires",
			tok:  APIToken{},
			want: true,
		},
		{
			name: "future expiry",
			tok:  APIToken{ExpiresAt: ptrTime(now.Add(1 * time.Hour))},
			want: true,
		},
		{
			name: "expired exactly now is still valid (After, not !Before)",
			tok:  APIToken{ExpiresAt: ptrTime(now)},
			want: true,
		},
		{
			name: "expired in the past",
			tok:  APIToken{ExpiresAt: ptrTime(now.Add(-1 * time.Second))},
			want: false,
		},
		{
			name: "revoked",
			tok:  APIToken{RevokedAt: ptrTime(now.Add(-1 * time.Hour))},
			want: false,
		},
		{
			name: "revoked beats not-yet-expired",
			tok: APIToken{
				RevokedAt: ptrTime(now.Add(-1 * time.Minute)),
				ExpiresAt: ptrTime(now.Add(1 * time.Hour)),
			},
			want: false,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.tok.IsActive(now); got != c.want {
				t.Errorf("IsActive() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAPIToken_HasScope(t *testing.T) {
	t.Parallel()
	tok := APIToken{Scopes: []string{ScopeLogsRead, ScopeNodesRead}}

	cases := []struct {
		name  string
		scope string
		want  bool
	}{
		{"granted logs", ScopeLogsRead, true},
		{"granted nodes", ScopeNodesRead, true},
		{"not granted metrics", ScopeMetricsRead, false},
		{"not granted audit", ScopeAuditRead, false},
		{"unknown scope", "admin:write", false},
		{"empty scope", "", false},
		{"case-sensitive", "LOGS:READ", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := tok.HasScope(c.scope); got != c.want {
				t.Errorf("HasScope(%q) = %v, want %v", c.scope, got, c.want)
			}
		})
	}
}

func TestAPIToken_HasScope_EmptyScopes(t *testing.T) {
	t.Parallel()
	tok := APIToken{}
	if tok.HasScope(ScopeLogsRead) {
		t.Errorf("empty scopes must reject any check")
	}
}

func TestScopeConstants_Distinct(t *testing.T) {
	t.Parallel()
	scopes := []string{ScopeLogsRead, ScopeNodesRead, ScopeMetricsRead, ScopeAuditRead}
	seen := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		if s == "" {
			t.Errorf("empty scope constant")
		}
		if _, dup := seen[s]; dup {
			t.Errorf("duplicate scope constant %q", s)
		}
		seen[s] = struct{}{}
	}
}
