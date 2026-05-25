package usecase

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"bus/internal/domain"
)

func TestCheckIncomingAuth_None(t *testing.T) {
	n := &domain.Node{IncomingAuthType: domain.IncomingAuthTypeNone}
	if err := CheckIncomingAuth(n, http.Header{}); err != nil {
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
	if err := CheckIncomingAuth(n, h); err != nil {
		t.Fatal(err)
	}

	hBad := http.Header{}
	hBad.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:wrong")))
	if err := CheckIncomingAuth(n, hBad); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}

	hMissing := http.Header{}
	if err := CheckIncomingAuth(n, hMissing); !errors.Is(err, domain.ErrAuthHeaderMissing) {
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
	if err := CheckIncomingAuth(n, h); err != nil {
		t.Fatal(err)
	}

	hBad := http.Header{}
	hBad.Set("Authorization", "Bearer wrong")
	if err := CheckIncomingAuth(n, hBad); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestBuildOutgoingAuth(t *testing.T) {
	cases := []struct {
		auth domain.AuthType
		creds string
		want string
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
