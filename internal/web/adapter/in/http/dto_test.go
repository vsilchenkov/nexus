package http

import (
	"testing"

	"nexus/internal/domain"
)

// TestBasicLogin — контракт разбора basic-кредов "login:password" для отдачи
// логина наружу (auth_login/incoming_auth_login): режем по ПЕРВОМУ «:», логин
// отдаём только для basic-типов, секреты (token/webhook/пароль) не отдаём.
func TestBasicLogin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		isBasic bool
		creds   string
		want    string
	}{
		{"basic login:password", true, "user:secret", "user"},
		{"password with colons", true, "user:p:a:ss", "user"},
		{"legacy creds without colon", true, "justlogin", "justlogin"},
		{"empty creds", true, "", ""},
		{"colon-first (empty login)", true, ":pwd", ""},
		{"token type must not leak", false, "sometoken", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := basicLogin(c.isBasic, c.creds); got != c.want {
				t.Fatalf("basicLogin(%v, %q) = %q, want %q", c.isBasic, c.creds, got, c.want)
			}
		})
	}
}

// TestNodeToResponse_AuthLogin — логин попадает в ответ только при типе basic;
// сами креды в NodeResponse отсутствуют по построению (только *_set-флаги).
func TestNodeToResponse_AuthLogin(t *testing.T) {
	t.Parallel()
	n := &domain.Node{
		AuthType:                domain.AuthTypeBasic,
		AuthCredentials:         "out_user:out_pass",
		IncomingAuthType:        domain.IncomingAuthTypeBasic,
		IncomingAuthCredentials: "in_user:in_pass",
	}
	r := nodeToResponse(n)
	if r.AuthLogin != "out_user" || r.IncomingAuthLogin != "in_user" {
		t.Fatalf("logins = %q/%q, want out_user/in_user", r.AuthLogin, r.IncomingAuthLogin)
	}
	if !r.AuthCredentialsSet || !r.IncomingAuthCredsSet {
		t.Fatalf("*_set flags must be true")
	}

	// token-типы: логин не отдаётся (креды — сам секрет).
	n2 := &domain.Node{
		AuthType:                domain.AuthTypeToken,
		AuthCredentials:         "bearer-secret",
		IncomingAuthType:        domain.IncomingAuthTypeWebhookSignature,
		IncomingAuthCredentials: "hmac-secret",
	}
	r2 := nodeToResponse(n2)
	if r2.AuthLogin != "" || r2.IncomingAuthLogin != "" {
		t.Fatalf("non-basic types must not expose login, got %q/%q", r2.AuthLogin, r2.IncomingAuthLogin)
	}
}
