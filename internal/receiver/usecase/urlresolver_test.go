package usecase

import (
	"errors"
	"net/url"
	"testing"

	"nexus/internal/domain"
)

func TestResolveURL_Static(t *testing.T) {
	n := &domain.Node{
		URLMode:   domain.URLModeStatic,
		TargetURL: "https://api.example.com/hook",
	}
	q := url.Values{"foo": {"bar"}}
	target, clean, err := ResolveURL(n, q)
	if err != nil {
		t.Fatal(err)
	}
	if target != "https://api.example.com/hook" {
		t.Errorf("target=%q", target)
	}
	if clean.Get("foo") != "bar" {
		t.Errorf("query lost: %v", clean)
	}
}

func TestResolveURL_FromRequest_OK(t *testing.T) {
	n := &domain.Node{
		URLMode:         domain.URLModeFromRequest,
		URLParamName:    "url_base",
		URLAllowedHosts: []string{"api.partner.com"},
	}
	q := url.Values{
		"url_base": {"https://api.partner.com/hook"},
		"other":    {"x"},
	}
	target, clean, err := ResolveURL(n, q)
	if err != nil {
		t.Fatal(err)
	}
	if target != "https://api.partner.com/hook" {
		t.Errorf("target=%q", target)
	}
	if clean.Get("url_base") != "" {
		t.Errorf("url_base must be stripped: %v", clean)
	}
	if clean.Get("other") != "x" {
		t.Errorf("other query lost: %v", clean)
	}
}

func TestResolveURL_FromRequest_Missing(t *testing.T) {
	n := &domain.Node{URLMode: domain.URLModeFromRequest, URLParamName: "url_base"}
	_, _, err := ResolveURL(n, url.Values{})
	if !errors.Is(err, domain.ErrURLParamRequired) {
		t.Fatalf("want ErrURLParamRequired, got %v", err)
	}
}

func TestResolveURL_FromRequest_Invalid(t *testing.T) {
	n := &domain.Node{URLMode: domain.URLModeFromRequest, URLParamName: "url_base"}
	for _, bad := range []string{"not-a-url", "ftp://example.com", "https://"} {
		_, _, err := ResolveURL(n, url.Values{"url_base": {bad}})
		if !errors.Is(err, domain.ErrURLInvalid) {
			t.Errorf("%q: want ErrURLInvalid, got %v", bad, err)
		}
	}
}

func TestResolveURL_FromRequest_NotAllowed(t *testing.T) {
	n := &domain.Node{
		URLMode:         domain.URLModeFromRequest,
		URLParamName:    "url_base",
		URLAllowedHosts: []string{"api.partner.com"},
	}
	_, _, err := ResolveURL(n, url.Values{"url_base": {"https://evil.com/hook"}})
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("want ErrURLNotAllowed, got %v", err)
	}
}

func TestResolveURL_FromRequest_Wildcard(t *testing.T) {
	n := &domain.Node{
		URLMode:         domain.URLModeFromRequest,
		URLParamName:    "url_base",
		URLAllowedHosts: []string{"*.partner.com"},
	}
	for _, host := range []string{
		"https://api.partner.com/hook",
		"https://eu.api.partner.com/hook",
	} {
		_, _, err := ResolveURL(n, url.Values{"url_base": {host}})
		if err != nil {
			t.Errorf("%q must be allowed: %v", host, err)
		}
	}
	_, _, err := ResolveURL(n, url.Values{"url_base": {"https://partner.com/hook"}})
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("partner.com (без поддомена) не должен матчить *.partner.com, got %v", err)
	}
}

func TestResolveURL_FromRequest_Regex(t *testing.T) {
	n := &domain.Node{
		URLMode:         domain.URLModeFromRequest,
		URLParamName:    "url_base",
		URLAllowedHosts: []string{`re:^api-\d+\.legacy\.io$`},
	}
	_, _, err := ResolveURL(n, url.Values{"url_base": {"https://api-42.legacy.io/cb"}})
	if err != nil {
		t.Errorf("api-42.legacy.io должен матчить regex: %v", err)
	}
	_, _, err = ResolveURL(n, url.Values{"url_base": {"https://api-x.legacy.io/cb"}})
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("api-x.legacy.io не должен матчить regex, got %v", err)
	}
}

// Хост с портом матчит exact-паттерн без порта: HostAllowed срезает порт
// (§23.2), и нестандартный порт не обходит allowlist.
func TestResolveURL_FromRequest_HostWithPort(t *testing.T) {
	n := &domain.Node{
		URLMode:         domain.URLModeFromRequest,
		URLParamName:    "url_base",
		URLAllowedHosts: []string{"api.partner.com"},
	}
	target, _, err := ResolveURL(n, url.Values{"url_base": {"https://api.partner.com:8443/hook"}})
	if err != nil {
		t.Fatalf("хост с портом должен матчить exact-паттерн без порта: %v", err)
	}
	if target != "https://api.partner.com:8443/hook" {
		t.Errorf("target=%q", target)
	}
}

func TestResolveURL_FromRequest_EmptyAllowlist(t *testing.T) {
	n := &domain.Node{
		URLMode:      domain.URLModeFromRequest,
		URLParamName: "url_base",
	}
	_, _, err := ResolveURL(n, url.Values{"url_base": {"https://anywhere.com/x"}})
	if err != nil {
		t.Fatalf("empty allowlist = разрешено всё: %v", err)
	}
}
