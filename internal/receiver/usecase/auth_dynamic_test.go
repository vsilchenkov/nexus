package usecase

import (
	"encoding/base64"
	"encoding/json"
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

// §41 п.2: значение в query уже содержит схему "Bearer <jwt>" (кейс
// ?Bearer=Bearer+<jwt>) — умный дедуп не должен удваивать префикс.
func TestBuildDynamicOutgoingAuth_SmartBearer_NoDoublePrefix(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "Bearer",
	}
	q := url.Values{"Bearer": {"Bearer eyJabc"}}
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Bearer eyJabc" {
		t.Errorf("smart dedup failed: header=%q (ожидался один префикс)", res.Header)
	}
	if res.Query.Get("Bearer") != "" {
		t.Errorf("служебный параметр Bearer должен быть вырезан: %v", res.Query)
	}
}

// Регистронезависимость дедупа: "bearer x" не получает второй "Bearer ".
func TestBuildDynamicOutgoingAuth_SmartBearer_CaseInsensitive(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "token",
	}
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{"token": {"bearer xyz"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "bearer xyz" {
		t.Errorf("case-insensitive dedup failed: header=%q", res.Header)
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

// §41: поле не пришло/пустое → Header=="" (запрос уходит без Authorization),
// ошибки нет, остальные query-параметры пробрасываются.
func TestBuildDynamicOutgoingAuth_TokenMissing_NoAuthForwarded(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "token",
	}
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{"other": {"keep"}}, nil)
	if err != nil {
		t.Fatalf("пустой токен не должен быть ошибкой: %v", err)
	}
	if res.Header != "" {
		t.Errorf("ожидался пустой Header (без Authorization), got %q", res.Header)
	}
	if res.Query.Get("other") != "keep" {
		t.Errorf("остальные параметры должны пробрасываться: %v", res.Query)
	}
}

// §41: basic_from_request теперь honored source/field. Дефолтный режим
// (header/Authorization) — прозрачный проброс "Basic <base64>".
func TestBuildDynamicOutgoingAuth_BasicFromHeader_Passthrough(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeBasicFromRequest,
		AuthDynamicSource: domain.AuthDynSourceHeader,
		AuthDynamicField:  "Authorization",
	}
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

// §41: basic из query-параметра — значение это base64(login:password),
// схема "Basic " добавляется умным дедупом.
func TestBuildDynamicOutgoingAuth_BasicFromQuery(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeBasicFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "creds",
	}
	enc := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{"creds": {enc}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header != "Basic "+enc {
		t.Errorf("basic from query failed: got %q", res.Header)
	}
	if res.Query.Get("creds") != "" {
		t.Errorf("служебный параметр creds должен быть вырезан: %v", res.Query)
	}
}

// §41: basic без креды на входе → Header=="" (без Authorization), не ошибка.
func TestBuildDynamicOutgoingAuth_BasicMissing_NoAuthForwarded(t *testing.T) {
	n := &domain.Node{
		AuthType:          domain.AuthTypeBasicFromRequest,
		AuthDynamicSource: domain.AuthDynSourceHeader,
		AuthDynamicField:  "Authorization",
	}
	res, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, nil)
	if err != nil {
		t.Fatalf("пустая креда не должна быть ошибкой: %v", err)
	}
	if res.Header != "" {
		t.Errorf("ожидался пустой Header, got %q", res.Header)
	}
}

func TestBuildDynamicOutgoingAuth_NotDynamic(t *testing.T) {
	n := &domain.Node{AuthType: domain.AuthTypeNone}
	_, err := BuildDynamicOutgoingAuth(n, http.Header{}, url.Values{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not dynamic") {
		t.Fatalf("want 'not dynamic' error, got %v", err)
	}
}
