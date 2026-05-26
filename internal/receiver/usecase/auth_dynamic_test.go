package usecase

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestBuildDynamicOutgoingAuth_TokenFromQuery(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "token",
	}
	q := url.Values{"token": {"abc123"}, "other": {"x"}}
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Bearer abc123" {
		t.Errorf("header=%q", res.Header)
	}
	if res.Query.Get("token") != "" {
		t.Errorf("token must be stripped: %v", res.Query)
	}
	if res.Query.Get("other") != "x" {
		t.Errorf("other lost")
	}
}

func TestBuildDynamicOutgoingAuth_TokenFromHeader_StripPrefix(t *testing.T) {
	n := &domain.Node{
		AuthType:               domain.AuthTypeTokenFromRequest,
		AuthDynamicSource:      domain.AuthDynSourceHeader,
		AuthDynamicField:       "Authorization",
		AuthDynamicStripPrefix: "Bearer ",
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer abc123")
	res, err := BuildDynamicOutgoingAuth(n, h, url.Values{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Bearer abc123" {
		t.Errorf("header=%q", res.Header)
	}
	if res.Headers.Get("Authorization") != "" {
		t.Errorf("source header must be stripped: %v", res.Headers)
	}
}

func TestBuildDynamicOutgoingAuth_TokenFromHeader_NoStripPrefix(t *testing.T) {
	n := &domain.Node{
		AuthType:               domain.AuthTypeTokenFromRequest,
		AuthDynamicSource:      domain.AuthDynSourceHeader,
		AuthDynamicField:       "X-Api-Key",
		AuthDynamicStripPrefix: "",
	}
	h := http.Header{}
	h.Set("X-Api-Key", "abc123")
	res, err := BuildDynamicOutgoingAuth(n, h, url.Values{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Bearer abc123" {
		t.Errorf("header=%q", res.Header)
	}
}

func TestBuildDynamicOutgoingAuth_TokenFromBody(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceBody,
		AuthDynamicField:  "token",
	}
	body := []byte(`{"token":"abc123","payload":42}`)
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Bearer abc123" {
		t.Errorf("header=%q", res.Header)
	}
	var obj map[string]any
	_ = json.Unmarshal(res.Body, &obj)
	if _, present := obj["token"]; present {
		t.Errorf("token must be stripped from body, got %s", res.Body)
	}
	if v, _ := obj["payload"].(float64); v != 42 {
		t.Errorf("payload lost from body, got %s", res.Body)
	}
}

func TestBuildDynamicOutgoingAuth_TokenMissing(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "token",
	}
	_, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, nil)
	if !errors.Is(err, domain.ErrAuthTokenRequired) {
		t.Fatalf("want ErrAuthTokenRequired, got %v", err)
	}
}

func TestBuildDynamicOutgoingAuth_BasicFromRequest_Passthrough(t *testing.T) {
	n := &domain.Node{AuthType: domain.AuthTypeBasicFromRequest}
	enc := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	h := http.Header{}
	h.Set("Authorization", "Basic "+enc)
	res, err := BuildDynamicOutgoingAuth(n, h, url.Values{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Basic "+enc {
		t.Errorf("passthrough failed: got %q", res.Header)
	}
	if res.Headers.Get("Authorization") != "" {
		t.Errorf("original Authorization must be stripped from forward-headers")
	}
}

func TestBuildDynamicOutgoingAuth_BasicFromRequest_Missing(t *testing.T) {
	n := &domain.Node{AuthType: domain.AuthTypeBasicFromRequest}
	_, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, nil)
	if !errors.Is(err, domain.ErrAuthHeaderMissing) {
		t.Fatalf("want ErrAuthHeaderMissing, got %v", err)
	}

	hWrong := http.Header{}
	hWrong.Set("Authorization", "Bearer x")
	_, err = BuildDynamicOutgoingAuth(n, hWrong, url.Values{}, nil)
	if !errors.Is(err, domain.ErrAuthHeaderMissing) {
		t.Fatalf("Bearer вместо Basic: want ErrAuthHeaderMissing, got %v", err)
	}
}

func TestBuildDynamicOutgoingAuth_BasicFromRequest_InvalidBase64(t *testing.T) {
	n := &domain.Node{AuthType: domain.AuthTypeBasicFromRequest}
	h := http.Header{}
	h.Set("Authorization", "Basic not!valid!base64!")
	_, err := BuildDynamicOutgoingAuth(n, h, url.Values{}, nil)
	if !errors.Is(err, domain.ErrAuthHeaderMalformed) {
		t.Fatalf("want ErrAuthHeaderMalformed, got %v", err)
	}
}

func TestBuildDynamicOutgoingAuth_NotDynamic(t *testing.T) {
	n := &domain.Node{AuthType: domain.AuthTypeNone}
	_, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not dynamic") {
		t.Fatalf("want 'not dynamic' error, got %v", err)
	}
}
