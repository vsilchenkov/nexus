package usecase

import (
	"net/http"
	"net/url"
	"testing"

	"nexus/internal/domain"
)

// TestBuildIncomingAuthValue_RoundTrip: значение, построенное из сохранённых
// кредов узла, инъектируется в синтетический запрос и обязано пройти боевой
// CheckIncomingAuth — иначе автоподстановка dry-run разошлась бы с проверкой.
func TestBuildIncomingAuthValue_RoundTrip(t *testing.T) {
	t.Parallel()
	body := []byte(`{"payload":true}`)
	cases := []struct {
		name       string
		node       *domain.Node
		wantSource domain.IncomingAuthSource
		wantField  string
	}{
		{
			name: "basic header default field",
			node: &domain.Node{
				IncomingAuthType:        domain.IncomingAuthTypeBasic,
				IncomingAuthCredentials: "user:pass",
			},
			wantSource: domain.IncomingAuthSourceHeader,
			wantField:  "Authorization",
		},
		{
			name: "basic query custom field",
			node: &domain.Node{
				IncomingAuthType:          domain.IncomingAuthTypeBasic,
				IncomingAuthCredentials:   "user:pass",
				IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
				IncomingAuthDynamicField:  "creds",
			},
			wantSource: domain.IncomingAuthSourceQuery,
			wantField:  "creds",
		},
		{
			name: "token header default field",
			node: &domain.Node{
				IncomingAuthType:        domain.IncomingAuthTypeToken,
				IncomingAuthCredentials: "s3cr3t",
			},
			wantSource: domain.IncomingAuthSourceHeader,
			wantField:  "Authorization",
		},
		{
			name: "token query custom field",
			node: &domain.Node{
				IncomingAuthType:          domain.IncomingAuthTypeToken,
				IncomingAuthCredentials:   "s3cr3t",
				IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
				IncomingAuthDynamicField:  "apikey",
			},
			wantSource: domain.IncomingAuthSourceQuery,
			wantField:  "apikey",
		},
		{
			name: "token custom header field",
			node: &domain.Node{
				IncomingAuthType:          domain.IncomingAuthTypeToken,
				IncomingAuthCredentials:   "abc",
				IncomingAuthDynamicSource: domain.IncomingAuthSourceHeader,
				IncomingAuthDynamicField:  "X-Auth",
			},
			wantSource: domain.IncomingAuthSourceHeader,
			wantField:  "X-Auth",
		},
		{
			name: "webhook signature",
			node: &domain.Node{
				IncomingAuthType:        domain.IncomingAuthTypeWebhookSignature,
				IncomingAuthCredentials: "shared-secret",
				WebhookSignatureHeader:  "X-Hub-Signature-256",
				WebhookSignaturePrefix:  "sha256=",
			},
			wantSource: domain.IncomingAuthSourceHeader,
			wantField:  "X-Hub-Signature-256",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pres, ok, err := BuildIncomingAuthValue(c.node, body)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !ok {
				t.Fatal("want ok=true, got false")
			}
			if pres.Source != c.wantSource || pres.Field != c.wantField {
				t.Fatalf("presentation: got source=%q field=%q, want source=%q field=%q",
					pres.Source, pres.Field, c.wantSource, c.wantField)
			}
			h := http.Header{}
			q := url.Values{}
			if pres.Source == domain.IncomingAuthSourceQuery {
				q.Set(pres.Field, pres.Value)
			} else {
				h.Set(pres.Field, pres.Value)
			}
			// Round-trip: боевой валидатор обязан принять построенное значение.
			if err := CheckIncomingAuth(c.node, h, q, body); err != nil {
				t.Fatalf("CheckIncomingAuth rejected built value: %v", err)
			}
		})
	}
}

// TestBuildIncomingAuthValue_NoAutofill: подставлять нечего — тип none или креды
// (для webhook — и имя заголовка) пусты. ok=false, без ошибки.
func TestBuildIncomingAuthValue_NoAutofill(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		node *domain.Node
	}{
		{"none", &domain.Node{IncomingAuthType: domain.IncomingAuthTypeNone}},
		{"basic empty creds", &domain.Node{IncomingAuthType: domain.IncomingAuthTypeBasic}},
		{"token empty creds", &domain.Node{IncomingAuthType: domain.IncomingAuthTypeToken}},
		{"webhook empty secret", &domain.Node{
			IncomingAuthType:       domain.IncomingAuthTypeWebhookSignature,
			WebhookSignatureHeader: "X-Sig",
		}},
		{"webhook empty header", &domain.Node{
			IncomingAuthType:        domain.IncomingAuthTypeWebhookSignature,
			IncomingAuthCredentials: "secret",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pres, ok, err := BuildIncomingAuthValue(c.node, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Fatalf("want ok=false, got true (%+v)", pres)
			}
		})
	}
}

// TestIncomingAuthPresented: читает уже предъявленное клиентом значение из того
// источника/поля, куда смотрит узел (для правила «ручной ввод побеждает»).
func TestIncomingAuthPresented(t *testing.T) {
	t.Parallel()

	none := &domain.Node{IncomingAuthType: domain.IncomingAuthTypeNone}
	if got := IncomingAuthPresented(none, http.Header{}, nil); got != "" {
		t.Fatalf("none: want empty, got %q", got)
	}

	basicHeader := &domain.Node{IncomingAuthType: domain.IncomingAuthTypeBasic}
	h := http.Header{}
	h.Set("Authorization", "Basic xxx")
	if got := IncomingAuthPresented(basicHeader, h, nil); got != "Basic xxx" {
		t.Fatalf("basic header: got %q", got)
	}
	if got := IncomingAuthPresented(basicHeader, http.Header{}, nil); got != "" {
		t.Fatalf("basic header empty: want empty, got %q", got)
	}

	tokenQuery := &domain.Node{
		IncomingAuthType:          domain.IncomingAuthTypeToken,
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
		IncomingAuthDynamicField:  "apikey",
	}
	if got := IncomingAuthPresented(tokenQuery, http.Header{}, url.Values{"apikey": {"tok"}}); got != "tok" {
		t.Fatalf("token query: got %q", got)
	}

	webhook := &domain.Node{
		IncomingAuthType:       domain.IncomingAuthTypeWebhookSignature,
		WebhookSignatureHeader: "X-Sig",
	}
	hs := http.Header{}
	hs.Set("X-Sig", "sha256=deadbeef")
	if got := IncomingAuthPresented(webhook, hs, nil); got != "sha256=deadbeef" {
		t.Fatalf("webhook: got %q", got)
	}
}
