package usecase

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"nexus/internal/domain"
)

func TestCheckIncomingAuth_None(t *testing.T) {
	n := &domain.Node{IncomingAuthType: domain.IncomingAuthTypeNone}
	if err := CheckIncomingAuth(n, http.Header{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIncomingAuth_Basic(t *testing.T) {
	n := &domain.Node{
		IncomingAuthType:        domain.IncomingAuthTypeBasic,
		IncomingAuthCredentials: "user:pass",
	}
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")))
	if err := CheckIncomingAuth(n, h, nil, nil); err != nil {
		t.Fatal(err)
	}

	hBad := http.Header{}
	hBad.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:wrong")))
	if err := CheckIncomingAuth(n, hBad, nil, nil); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}

	hMissing := http.Header{}
	if err := CheckIncomingAuth(n, hMissing, nil, nil); !errors.Is(err, domain.ErrAuthHeaderMissing) {
		t.Fatalf("want ErrAuthHeaderMissing, got %v", err)
	}
}

func TestCheckIncomingAuth_Token(t *testing.T) {
	n := &domain.Node{
		IncomingAuthType:        domain.IncomingAuthTypeToken,
		IncomingAuthCredentials: "secret-token",
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer secret-token")
	if err := CheckIncomingAuth(n, h, nil, nil); err != nil {
		t.Fatal(err)
	}

	hBad := http.Header{}
	hBad.Set("Authorization", "Bearer wrong")
	if err := CheckIncomingAuth(n, hBad, nil, nil); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

// §41: входящий token из query-параметра по настраиваемому имени поля.
func TestCheckIncomingAuth_Token_FromQuery(t *testing.T) {
	n := &domain.Node{
		IncomingAuthType:          domain.IncomingAuthTypeToken,
		IncomingAuthCredentials:   "s3cr3t",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
		IncomingAuthDynamicField:  "apikey",
	}
	// Верный токен в ?apikey= → ok (без схемы Bearer, значение и есть токен).
	if err := CheckIncomingAuth(n, http.Header{}, url.Values{"apikey": {"s3cr3t"}}, nil); err != nil {
		t.Fatalf("valid query token: %v", err)
	}
	// Неверный → 401.
	if err := CheckIncomingAuth(n, http.Header{}, url.Values{"apikey": {"nope"}}, nil); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	// Отсутствует параметр → ErrAuthHeaderMissing (гейт, 401).
	if err := CheckIncomingAuth(n, http.Header{}, url.Values{}, nil); !errors.Is(err, domain.ErrAuthHeaderMissing) {
		t.Fatalf("want ErrAuthHeaderMissing, got %v", err)
	}
}

// §41: входящий basic из query-параметра — значение это base64(login:password).
func TestCheckIncomingAuth_Basic_FromQuery(t *testing.T) {
	n := &domain.Node{
		IncomingAuthType:          domain.IncomingAuthTypeBasic,
		IncomingAuthCredentials:   "user:pass",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
		IncomingAuthDynamicField:  "creds",
	}
	good := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if err := CheckIncomingAuth(n, http.Header{}, url.Values{"creds": {good}}, nil); err != nil {
		t.Fatalf("valid query basic: %v", err)
	}
	bad := base64.StdEncoding.EncodeToString([]byte("user:wrong"))
	if err := CheckIncomingAuth(n, http.Header{}, url.Values{"creds": {bad}}, nil); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

// §41: source=header с кастомным именем поля (не Authorization) — прежний
// контракт со схемой Bearer сохраняется.
func TestCheckIncomingAuth_Token_FromCustomHeader(t *testing.T) {
	n := &domain.Node{
		IncomingAuthType:          domain.IncomingAuthTypeToken,
		IncomingAuthCredentials:   "abc",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceHeader,
		IncomingAuthDynamicField:  "X-Auth",
	}
	h := http.Header{}
	h.Set("X-Auth", "Bearer abc")
	if err := CheckIncomingAuth(n, h, nil, nil); err != nil {
		t.Fatalf("valid custom-header token: %v", err)
	}
}

func TestBuildOutgoingAuth(t *testing.T) {
	cases := []struct {
		auth  domain.AuthType
		creds string
		want  string
	}{
		{domain.AuthTypeNone, "", ""},
		{domain.AuthTypeBasic, "u:p", "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))},
		{domain.AuthTypeToken, "abc123", "Bearer abc123"},
	}
	for _, c := range cases {
		n := &domain.Node{AuthType: c.auth, AuthCredentials: c.creds}
		got, err := BuildOutgoingAuth(n)
		if err != nil {
			t.Errorf("%s: %v", c.auth, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.auth, got, c.want)
		}
	}
}
