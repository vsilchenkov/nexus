package domain

import "testing"

func TestNodeStatus_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		s    NodeStatus
		want bool
	}{
		{"enabled", NodeStatusEnabled, true},
		{"disabled", NodeStatusDisabled, true},
		{"paused", NodeStatusPaused, true},
		{"empty", NodeStatus(""), false},
		{"unknown", NodeStatus("draining"), false},
		{"case-sensitive", NodeStatus("Enabled"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.s.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestRootMethod_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		m    RootMethod
		want bool
	}{
		{"request", RootMethodRequest, true},
		{"requestAsync", RootMethodRequestAsync, true},
		{"empty", RootMethod(""), false},
		{"callback-not-a-root-method", RootMethod("callback"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.m.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.m, got, c.want)
			}
		})
	}
}

func TestURLMode_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		u    URLMode
		want bool
	}{
		{"static", URLModeStatic, true},
		{"from_request", URLModeFromRequest, true},
		{"empty", URLMode(""), false},
		{"unknown", URLMode("dynamic"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.u.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.u, got, c.want)
			}
		})
	}
}

func TestAuthType_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    AuthType
		want bool
	}{
		{"none", AuthTypeNone, true},
		{"basic", AuthTypeBasic, true},
		{"token", AuthTypeToken, true},
		{"token_from_request", AuthTypeTokenFromRequest, true},
		{"basic_from_request", AuthTypeBasicFromRequest, true},
		{"empty", AuthType(""), false},
		{"oauth2-not-supported", AuthType("oauth2"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.a.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.a, got, c.want)
			}
		})
	}
}

func TestAuthDynSource_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		s    AuthDynSource
		want bool
	}{
		{"query", AuthDynSourceQuery, true},
		{"header", AuthDynSourceHeader, true},
		{"body", AuthDynSourceBody, true},
		{"empty", AuthDynSource(""), false},
		{"cookie-not-supported", AuthDynSource("cookie"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.s.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestIncomingAuthType_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    IncomingAuthType
		want bool
	}{
		{"none", IncomingAuthTypeNone, true},
		{"basic", IncomingAuthTypeBasic, true},
		{"token", IncomingAuthTypeToken, true},
		{"webhook_signature", IncomingAuthTypeWebhookSignature, true},
		{"empty", IncomingAuthType(""), false},
		{"unknown", IncomingAuthType("mtls"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.a.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.a, got, c.want)
			}
		})
	}
}

func TestUserRole_Valid_And_IsAdmin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		r           UserRole
		wantValid   bool
		wantIsAdmin bool
	}{
		{"admin", UserRoleAdmin, true, true},
		{"manager", UserRoleManager, true, false},
		{"viewer", UserRoleViewer, true, false},
		{"empty", UserRole(""), false, false},
		{"editor-not-supported", UserRole("editor"), false, false},
		{"case-sensitive", UserRole("Admin"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.r.Valid(); got != c.wantValid {
				t.Errorf("%q.Valid() = %v, want %v", c.r, got, c.wantValid)
			}
			if got := c.r.IsAdmin(); got != c.wantIsAdmin {
				t.Errorf("%q.IsAdmin() = %v, want %v", c.r, got, c.wantIsAdmin)
			}
		})
	}
}

func TestUserRole_Rank_And_AtLeast(t *testing.T) {
	t.Parallel()
	// Иерархия: viewer < manager < admin (§26).
	if !(UserRoleViewer.Rank() < UserRoleManager.Rank() &&
		UserRoleManager.Rank() < UserRoleAdmin.Rank()) {
		t.Fatalf("ожидалось viewer(%d) < manager(%d) < admin(%d)",
			UserRoleViewer.Rank(), UserRoleManager.Rank(), UserRoleAdmin.Rank())
	}
	cases := []struct {
		name string
		r    UserRole
		min  UserRole
		want bool
	}{
		{"admin >= manager", UserRoleAdmin, UserRoleManager, true},
		{"admin >= admin", UserRoleAdmin, UserRoleAdmin, true},
		{"manager >= manager", UserRoleManager, UserRoleManager, true},
		{"manager < admin", UserRoleManager, UserRoleAdmin, false},
		{"viewer < manager", UserRoleViewer, UserRoleManager, false},
		{"viewer >= viewer", UserRoleViewer, UserRoleViewer, true},
		{"unknown < manager", UserRole("editor"), UserRoleManager, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.r.AtLeast(c.min); got != c.want {
				t.Errorf("%q.AtLeast(%q) = %v, want %v", c.r, c.min, got, c.want)
			}
		})
	}
}

func TestUserLang_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		l    UserLang
		want bool
	}{
		{"en", UserLangEN, true},
		{"ru", UserLangRU, true},
		{"empty", UserLang(""), false},
		{"fr-not-supported", UserLang("fr"), false},
		{"case-sensitive", UserLang("EN"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.l.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.l, got, c.want)
			}
		})
	}
}
