package sentry

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"

	"nexus/internal/platform/config"
)

func TestIsSensitive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Точные совпадения.
		{"password exact", "password", true},
		{"passwd exact", "passwd", true},
		{"secret exact", "secret", true},
		{"token exact", "token", true},
		{"api_key exact", "api_key", true},
		{"apikey exact", "apikey", true},
		{"authorization exact", "authorization", true},
		{"auth_credentials exact", "auth_credentials", true},
		{"incoming_auth_credentials exact", "incoming_auth_credentials", true},
		{"encryption_key exact", "encryption_key", true},
		{"cookie exact", "cookie", true},
		{"set-cookie exact", "set-cookie", true},
		{"client_secret exact", "client_secret", true},

		// Case-insensitive (lowercasing).
		{"Authorization", "Authorization", true},
		{"AUTH_CREDENTIALS", "AUTH_CREDENTIALS", true},
		{"X-API-Key", "X-API-Key", true},
		{"X-Auth-Token", "X-Auth-Token", true},
		{"X-CSRF-Token", "X-CSRF-Token", true},
		{"Set-Cookie", "Set-Cookie", true},

		// Substring matching: должно ловить вложенные секреты.
		{"user_password contains password", "user_password", true},
		{"db_password contains password", "db_password", true},
		{"refresh_token contains token", "refresh_token", true},
		{"my_api_key contains api_key", "my_api_key", true},

		// Не чувствительные.
		{"username not sensitive", "username", false},
		{"user_id not sensitive", "user_id", false},
		{"email", "email", false},
		{"x-request-id", "x-request-id", false},
		{"content-type", "content-type", false},
		{"empty string", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isSensitive(c.in); got != c.want {
				t.Errorf("isSensitive(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestMaskMap(t *testing.T) {
	t.Parallel()
	in := map[string]string{
		"Authorization": "Bearer eyJ...",
		"Content-Type":  "application/json",
		"X-API-Key":     "secret-value",
		"X-Request-Id":  "req-123",
	}
	got := maskMap(in)

	if got["Authorization"] != "***" {
		t.Errorf("Authorization not masked: %q", got["Authorization"])
	}
	if got["X-API-Key"] != "***" {
		t.Errorf("X-API-Key not masked: %q", got["X-API-Key"])
	}
	if got["Content-Type"] != "application/json" {
		t.Errorf("Content-Type changed: %q", got["Content-Type"])
	}
	if got["X-Request-Id"] != "req-123" {
		t.Errorf("X-Request-Id changed: %q", got["X-Request-Id"])
	}
}

func TestMaskMap_EmptyAndNil(t *testing.T) {
	t.Parallel()
	if got := maskMap(nil); got != nil {
		t.Errorf("nil input: got %v", got)
	}
	if got := maskMap(map[string]string{}); len(got) != 0 {
		t.Errorf("empty input: got %v", got)
	}
}

func TestMaskAny(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"password":  "p@ssw0rd",
		"username":  "alice",
		"api_key":   "secret-key-value",
		"timestamp": 1700000000,
	}
	got := maskAny(in)

	if got["password"] != "***" {
		t.Errorf("password not masked: %v", got["password"])
	}
	if got["api_key"] != "***" {
		t.Errorf("api_key not masked: %v", got["api_key"])
	}
	if got["username"] != "alice" {
		t.Errorf("username changed: %v", got["username"])
	}
	if got["timestamp"] != 1700000000 {
		t.Errorf("timestamp changed: %v", got["timestamp"])
	}
}

func TestMaskAny_EmptyAndNil(t *testing.T) {
	t.Parallel()
	if got := maskAny(nil); got != nil {
		t.Errorf("nil input: got %v", got)
	}
	if got := maskAny(map[string]any{}); len(got) != 0 {
		t.Errorf("empty input: got %v", got)
	}
}

func TestBeforeSend_Nil(t *testing.T) {
	t.Parallel()
	if got := beforeSend(nil, nil); got != nil {
		t.Errorf("nil event: got %v", got)
	}
}

func TestBeforeSend_MasksRequest(t *testing.T) {
	t.Parallel()
	ev := &sentry.Event{
		Request: &sentry.Request{
			Headers: map[string]string{
				"Authorization": "Bearer xxx",
				"User-Agent":    "curl/8",
			},
			Cookies:     "session=abc",
			Data:        `{"password":"p1"}`,
			QueryString: "token=leaked",
		},
		Tags: map[string]string{
			"node":      "demo/path",
			"x-api-key": "leaked-key",
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Data: map[string]any{"secret": "x", "msg": "hi"}},
		},
	}
	out := beforeSend(ev, nil)
	if out == nil {
		t.Fatal("nil out")
	}
	if out.Request.Headers["Authorization"] != "***" {
		t.Errorf("Authorization header not masked: %q", out.Request.Headers["Authorization"])
	}
	if out.Request.Headers["User-Agent"] != "curl/8" {
		t.Errorf("User-Agent changed: %q", out.Request.Headers["User-Agent"])
	}
	if out.Request.Cookies != "" {
		t.Errorf("Cookies not cleared: %q", out.Request.Cookies)
	}
	if out.Request.Data != "" {
		t.Errorf("Data not cleared: %q", out.Request.Data)
	}
	if out.Request.QueryString != "" {
		t.Errorf("QueryString not cleared: %q", out.Request.QueryString)
	}
	if out.Tags["x-api-key"] != "***" {
		t.Errorf("Tags x-api-key not masked: %q", out.Tags["x-api-key"])
	}
	if out.Tags["node"] != "demo/path" {
		t.Errorf("Tags node changed: %q", out.Tags["node"])
	}
	if out.Breadcrumbs[0].Data["secret"] != "***" {
		t.Errorf("Breadcrumb secret not masked: %v", out.Breadcrumbs[0].Data["secret"])
	}
	if out.Breadcrumbs[0].Data["msg"] != "hi" {
		t.Errorf("Breadcrumb msg changed: %v", out.Breadcrumbs[0].Data["msg"])
	}
}

func TestBeforeSend_NoRequest(t *testing.T) {
	t.Parallel()
	ev := &sentry.Event{
		Tags: map[string]string{"password": "leaked"},
	}
	out := beforeSend(ev, nil)
	if out.Tags["password"] != "***" {
		t.Errorf("Tags password not masked: %q", out.Tags["password"])
	}
}

func TestBeforeBreadcrumb(t *testing.T) {
	t.Parallel()
	b := &sentry.Breadcrumb{
		Data: map[string]any{
			"authorization": "Bearer x",
			"http.method":   "GET",
		},
	}
	out := beforeBreadcrumb(b, nil)
	if out == nil {
		t.Fatal("nil out")
	}
	if out.Data["authorization"] != "***" {
		t.Errorf("authorization not masked: %v", out.Data["authorization"])
	}
	if out.Data["http.method"] != "GET" {
		t.Errorf("http.method changed: %v", out.Data["http.method"])
	}
}

func TestBeforeBreadcrumb_Nil(t *testing.T) {
	t.Parallel()
	if got := beforeBreadcrumb(nil, nil); got != nil {
		t.Errorf("nil breadcrumb: got %v", got)
	}
}

func TestTruncateValue(t *testing.T) {
	t.Parallel()
	// Короткая строка — без изменений.
	if got := truncateValue("hello"); got != "hello" {
		t.Errorf("short string changed: %q", got)
	}
	// Большая строка — режется до maxSentryValueBytes + маркер, итог много меньше.
	big := strings.Repeat("a", 1_000_000)
	got := truncateValue(big)
	if len(got) >= len(big) {
		t.Errorf("large string not truncated: len=%d", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxSentryValueBytes)) {
		t.Error("prefix not preserved")
	}
	if !strings.Contains(got, "1000000 bytes total") {
		t.Errorf("missing size marker: %q", got[len(got)-40:])
	}
	// Многобайтовое тело — обрезка по границе руны даёт валидный UTF-8.
	cyr := truncateValue(strings.Repeat("я", 1_000_000))
	if !utf8.ValidString(cyr) {
		t.Error("truncated cyrillic is not valid UTF-8")
	}
}

// TestBeforeSend_TruncatesLargeBody — §42: большое тело, просочившееся в message
// / exception value / contexts, режется до ≈maxSentryValueBytes и не уезжает в
// Sentry целиком.
func TestBeforeSend_TruncatesLargeBody(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("X", 20*1024*1024) // 20 МиБ «тело»
	ev := &sentry.Event{
		Message:   body,
		Exception: []sentry.Exception{{Value: body}},
		Contexts:  map[string]sentry.Context{"payload": {"response": body, "status": 500}},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Message: body, Data: map[string]any{"request": body}},
		},
	}
	out := beforeSend(ev, nil)

	limit := maxSentryValueBytes + 64 // запас на маркер
	if len(out.Message) > limit {
		t.Errorf("Message not capped: %d", len(out.Message))
	}
	if len(out.Exception[0].Value) > limit {
		t.Errorf("Exception value not capped: %d", len(out.Exception[0].Value))
	}
	if s, _ := out.Contexts["payload"]["response"].(string); len(s) > limit {
		t.Errorf("Contexts value not capped: %d", len(s))
	}
	if out.Contexts["payload"]["status"] != 500 {
		t.Errorf("non-string context value changed: %v", out.Contexts["payload"]["status"])
	}
	if len(out.Breadcrumbs[0].Message) > limit {
		t.Errorf("Breadcrumb message not capped: %d", len(out.Breadcrumbs[0].Message))
	}
	if s, _ := out.Breadcrumbs[0].Data["request"].(string); len(s) > limit {
		t.Errorf("Breadcrumb data not capped: %d", len(s))
	}
}

// TestBeforeSendTransaction_ScrubsSpans — §42: транзакции performance тоже
// маскируются и режутся (спаны: tags по секрету → ***, большая data → обрезана).
func TestBeforeSendTransaction_ScrubsSpans(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("Z", 5*1024*1024)
	ev := &sentry.Event{
		Type:    "transaction",
		Message: body,
		Spans: []*sentry.Span{
			{
				Description: body,
				Tags:        map[string]string{"authorization": "Bearer x", "node": "demo"},
				Data:        map[string]any{"response": body, "code": 200},
			},
		},
	}
	out := beforeSendTransaction(ev, nil)
	if out == nil {
		t.Fatal("nil out")
	}
	limit := maxSentryValueBytes + 64
	if len(out.Message) > limit {
		t.Errorf("transaction message not capped: %d", len(out.Message))
	}
	sp := out.Spans[0]
	if sp.Tags["authorization"] != "***" {
		t.Errorf("span auth tag not masked: %q", sp.Tags["authorization"])
	}
	if sp.Tags["node"] != "demo" {
		t.Errorf("span node tag changed: %q", sp.Tags["node"])
	}
	if len(sp.Description) > limit {
		t.Errorf("span description not capped: %d", len(sp.Description))
	}
	if s, _ := sp.Data["response"].(string); len(s) > limit {
		t.Errorf("span data not capped: %d", len(s))
	}
	if sp.Data["code"] != 200 {
		t.Errorf("non-string span data changed: %v", sp.Data["code"])
	}
}

func TestInit_Disabled(t *testing.T) {
	// Не parallel: Init трогает глобальный sentry hub.
	s := &config.SentrySection{Use: false}
	if err := Init(s, "project", "v0.0.0"); err != nil {
		t.Errorf("Init(use=false) error: %v", err)
	}
}

func TestReload_Disabled(t *testing.T) {
	// Не parallel: Reload вызывает Init с глобальным hub.
	s := &config.SentrySection{Use: false}
	if err := Reload(s, "project", "v0.0.0"); err != nil {
		t.Errorf("Reload(use=false) error: %v", err)
	}
}

func TestSensitiveKeys_NotEmpty(t *testing.T) {
	t.Parallel()
	// Контракт: список не пуст, состоит из непустых lowercase-ключей.
	if len(sensitiveKeys) == 0 {
		t.Fatal("sensitiveKeys is empty")
	}
	for i, k := range sensitiveKeys {
		if k == "" {
			t.Errorf("sensitiveKeys[%d] is empty", i)
		}
		for _, r := range k {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("sensitiveKeys[%d] = %q contains uppercase", i, k)
				break
			}
		}
	}
}
