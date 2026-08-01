package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	senderv1 "nexus/proto/sender/v1"
)

type stubNodeReader struct {
	node *domain.Node
	err  error
}

func (s stubNodeReader) Get(_ context.Context, _, _ string) (*domain.Node, error) {
	return s.node, s.err
}

type stubSenderClient struct {
	resp *senderv1.SendResponse
	err  error
}

func (s stubSenderClient) Send(_ context.Context, _ *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	return s.resp, s.err
}

// TestMethodMatches_Any (§40): ANY принимает любой входящий метод; остальное —
// как было (пустой want = POST, регистронезависимо).
func TestMethodMatches_Any(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "head"} {
		if !MethodMatches(m, domain.HTTPMethodAny) {
			t.Errorf("MethodMatches(%q, ANY) = false, want true", m)
		}
	}
	if MethodMatches("GET", domain.HTTPMethodPOST) {
		t.Error("GET vs POST не должны совпадать")
	}
	if !MethodMatches("post", domain.HTTPMethodPOST) {
		t.Error("post vs POST должны совпадать (без учёта регистра)")
	}
	if !MethodMatches("POST", "") {
		t.Error("пустой want трактуется как POST")
	}
}

// TestEffectiveOutgoingMethod (§40): ANY зеркалит входящий метод; конкретный —
// остаётся; пустой → POST.
func TestEffectiveOutgoingMethod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		outgoing domain.HTTPMethod
		incoming string
		want     string
	}{
		{"ANY зеркалит PUT", domain.HTTPMethodAny, "PUT", "PUT"},
		{"ANY зеркалит нижний регистр", domain.HTTPMethodAny, "delete", "DELETE"},
		{"ANY без входящего → POST", domain.HTTPMethodAny, "", "POST"},
		{"конкретный GET остаётся GET", domain.HTTPMethodGET, "POST", "GET"},
		{"пустой → POST", "", "PUT", "POST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			n := &domain.Node{OutgoingMethod: c.outgoing}
			if got := EffectiveOutgoingMethod(n, c.incoming); got != c.want {
				t.Errorf("EffectiveOutgoingMethod(%q, %q) = %q, want %q", c.outgoing, c.incoming, got, c.want)
			}
		})
	}
}

// TestRoute_Paused: §3.6 — sync на paused-узел отдаёт ErrNodePaused,
// handler по этой ошибке переключается на async и возвращает 202.
func TestRoute_Paused(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusPaused,
	}
	u := NewRouteUsecase(stubNodeReader{node: node}, stubSenderClient{}, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/path"})
	if !errors.Is(err, domain.ErrNodePaused) {
		t.Fatalf("want ErrNodePaused, got %v", err)
	}
}

func TestRoute_Disabled(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusDisabled,
	}
	u := NewRouteUsecase(stubNodeReader{node: node}, stubSenderClient{}, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/path"})
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}

// TestRouteAsync_PausedQueued: §3.6 — RouteAsync для paused-узла
// возвращает Queued=true и не падает.
func TestRouteAsync_PausedQueued(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusPaused,
	}
	producer := &stubProducer{}
	u := NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "nexus.async", 5, logging.NewNoop())
	res, err := u.RouteAsync(context.Background(), RouteInput{NodePath: "demo/path", Method: "POST"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Queued {
		t.Fatalf("want Queued=true for paused node")
	}
	if res.NodeStatus != domain.NodeStatusPaused {
		t.Fatalf("want NodeStatus=paused, got %q", res.NodeStatus)
	}
	if producer.calls != 1 {
		t.Fatalf("want 1 produce call, got %d", producer.calls)
	}
}

type stubProducer struct {
	calls int
}

func (s *stubProducer) Produce(_ context.Context, _, _ string, _ []byte, _ map[string]string) error {
	s.calls++
	return nil
}

// capturingSender запоминает последний SendRequest для проверки исходящего метода.
type capturingSender struct {
	last *senderv1.SendRequest
	resp *senderv1.SendResponse
}

func (c *capturingSender) Send(_ context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	c.last = req
	if c.resp == nil {
		return &senderv1.SendResponse{StatusCode: 200, Body: []byte("ok")}, nil
	}
	return c.resp, nil
}

func methodTestNode() *domain.Node {
	return &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		IncomingMethod:   domain.HTTPMethodPOST,
		OutgoingMethod:   domain.HTTPMethodPUT,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
}

// TestRoute_IncomingMethodMismatch: §3.2 (#5) — узел принимает только свой
// входящий метод, иначе ErrNodeMethodNotAllowed (handler → 405).
func TestRoute_IncomingMethodMismatch(t *testing.T) {
	t.Parallel()
	u := NewRouteUsecase(stubNodeReader{node: methodTestNode()}, &capturingSender{}, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/path", Method: "GET"})
	if !errors.Is(err, domain.ErrNodeMethodNotAllowed) {
		t.Fatalf("want ErrNodeMethodNotAllowed, got %v", err)
	}
}

// TestRoute_OutgoingMethodUsed: §3.2 (#5) — исходящий вызов получателя идёт
// методом из настройки узла (OutgoingMethod), а не методом входящего запроса.
func TestRoute_OutgoingMethodUsed(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: methodTestNode()}, sender, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/path", Method: "POST"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sender.last == nil || sender.last.GetMethod() != "PUT" {
		t.Fatalf("want outgoing method PUT, got %v", sender.last.GetMethod())
	}
}

// dynAuthTestNode — узел с исходящей token_from_request из query-параметра Bearer.
func dynAuthTestNode() *domain.Node {
	return &domain.Node{
		Path:              "demo/dyn",
		RootMethod:        domain.RootMethodRequest,
		IncomingMethod:    domain.HTTPMethodPOST,
		OutgoingMethod:    domain.HTTPMethodPOST,
		URLMode:           domain.URLModeStatic,
		TargetURL:         "https://example.com/hook",
		AuthType:          domain.AuthTypeTokenFromRequest,
		AuthDynamicSource: domain.AuthDynSourceQuery,
		AuthDynamicField:  "Bearer",
		IncomingAuthType:  domain.IncomingAuthTypeNone,
		Status:            domain.NodeStatusEnabled,
	}
}

// TestRoute_DynamicAuth_TokenForwarded (§41): значение ?Bearer=Bearer+<jwt>
// доходит до Sender как один Authorization: Bearer <jwt>, а служебный параметр
// вырезается из проксируемого target URL.
func TestRoute_DynamicAuth_TokenForwarded(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: dynAuthTestNode()}, sender, 5, logging.NewNoop())
	q := url.Values{"Bearer": {"Bearer eyJabc"}}
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/dyn", Method: "POST", Query: q})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := sender.last.GetAuth().GetAuthorizationHeader(); got != "Bearer eyJabc" {
		t.Fatalf("auth header = %q, want single 'Bearer eyJabc'", got)
	}
	if strings.Contains(sender.last.GetTargetUrl(), "Bearer") {
		t.Fatalf("служебный параметр Bearer не должен попасть в target URL: %s", sender.last.GetTargetUrl())
	}
}

// fromRequestNode — узел url_mode=from_request с заданным allowlist (§3.4/§23).
func fromRequestNode(hosts []string) *domain.Node {
	return &domain.Node{
		Path:             "demo/dyn-url",
		RootMethod:       domain.RootMethodRequest,
		IncomingMethod:   domain.HTTPMethodPOST,
		OutgoingMethod:   domain.HTTPMethodPOST,
		URLMode:          domain.URLModeFromRequest,
		URLParamName:     "url_base",
		URLAllowedHosts:  hosts,
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
}

// TestRoute_FromRequest_AllowedHost (§3.4): разрешённый хост — Sender получает
// URL из query-параметра, сам url_base вырезан, остальная query сохранена.
func TestRoute_FromRequest_AllowedHost(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: fromRequestNode([]string{"api.partner.com"})}, sender, 5, logging.NewNoop())
	q := url.Values{
		"url_base": {"https://api.partner.com/hook"},
		"extra":    {"1"},
	}
	out, err := u.Route(context.Background(), RouteInput{NodePath: "demo/dyn-url", Method: "POST", Query: q})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", out.StatusCode)
	}
	got := sender.last.GetTargetUrl()
	if !strings.HasPrefix(got, "https://api.partner.com/hook") {
		t.Fatalf("target = %q, want URL из запроса", got)
	}
	if strings.Contains(got, "url_base") {
		t.Fatalf("служебный url_base должен быть вырезан из target: %q", got)
	}
	if !strings.Contains(got, "extra=1") {
		t.Fatalf("остальная query должна сохраниться: %q", got)
	}
}

// TestRoute_FromRequest_BlockedHost (§3.4): хост вне allowlist —
// ErrURLNotAllowed (handler → 403), Sender НЕ вызывается.
func TestRoute_FromRequest_BlockedHost(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: fromRequestNode([]string{"api.partner.com"})}, sender, 5, logging.NewNoop())
	q := url.Values{"url_base": {"https://evil.example.com/hook"}}
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/dyn-url", Method: "POST", Query: q})
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("want ErrURLNotAllowed, got %v", err)
	}
	if sender.last != nil {
		t.Fatal("запрос на запрещённый хост не должен дойти до Sender")
	}
}

// TestRoute_FromRequest_MissingParam (§3.4): без url_base — ErrURLParamRequired
// (handler → 400), Sender НЕ вызывается; fallback на target_url отсутствует.
func TestRoute_FromRequest_MissingParam(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	node := fromRequestNode(nil)
	node.TargetURL = "https://static.example.com/hook" // не должен использоваться
	u := NewRouteUsecase(stubNodeReader{node: node}, sender, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/dyn-url", Method: "POST", Query: url.Values{}})
	if !errors.Is(err, domain.ErrURLParamRequired) {
		t.Fatalf("want ErrURLParamRequired, got %v", err)
	}
	if sender.last != nil {
		t.Fatal("без url_base запрос не должен дойти до Sender (нет fallback на target_url)")
	}
}

// TestRouteAsync_FromRequest_AllowedHost (§3.4 async-путь): разрешённый хост —
// envelope уходит в Kafka с резолвнутым URL из запроса, url_base вырезан.
func TestRouteAsync_FromRequest_AllowedHost(t *testing.T) {
	t.Parallel()
	node := fromRequestNode([]string{"*.partner.com"})
	producer := &capturingProducer{}
	u := NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "nexus.async", 5, logging.NewNoop())
	q := url.Values{
		"url_base": {"https://api.partner.com/hook"},
		"extra":    {"1"},
	}
	res, err := u.RouteAsync(context.Background(), RouteInput{NodePath: "demo/dyn-url", Method: "POST", Query: q})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.ID == "" {
		t.Fatal("empty envelope id")
	}
	if producer.last == nil {
		t.Fatal("envelope must be produced for allowed host")
	}
	var env Envelope
	if err := json.Unmarshal(producer.last, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !strings.HasPrefix(env.TargetURL, "https://api.partner.com/hook") {
		t.Fatalf("envelope target = %q, want URL из запроса", env.TargetURL)
	}
	if strings.Contains(env.TargetURL, "url_base") {
		t.Fatalf("служебный url_base должен быть вырезан из envelope target: %q", env.TargetURL)
	}
	if !strings.Contains(env.TargetURL, "extra=1") {
		t.Fatalf("остальная query должна сохраниться в envelope: %q", env.TargetURL)
	}
}

// TestRouteAsync_FromRequest_BlockedHost (§3.4 async-путь): хост вне allowlist —
// ErrURLNotAllowed, в Kafka НИЧЕГО не ставится (сообщение не теряется в очереди,
// клиент сразу получает 403).
func TestRouteAsync_FromRequest_BlockedHost(t *testing.T) {
	t.Parallel()
	node := fromRequestNode([]string{"api.partner.com"})
	producer := &capturingProducer{}
	u := NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "nexus.async", 5, logging.NewNoop())
	q := url.Values{"url_base": {"https://evil.example.com/hook"}}
	_, err := u.RouteAsync(context.Background(), RouteInput{NodePath: "demo/dyn-url", Method: "POST", Query: q})
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("want ErrURLNotAllowed, got %v", err)
	}
	if producer.last != nil {
		t.Fatal("запрос на запрещённый хост не должен попасть в Kafka")
	}
}

// TestRoute_DynamicAuth_MissingToken_NoAuth (§41): поле не пришло → Sender
// получает пустой Authorization, запрос не падает.
func TestRoute_DynamicAuth_MissingToken_NoAuth(t *testing.T) {
	t.Parallel()
	sender := &capturingSender{}
	u := NewRouteUsecase(stubNodeReader{node: dynAuthTestNode()}, sender, 5, logging.NewNoop())
	_, err := u.Route(context.Background(), RouteInput{NodePath: "demo/dyn", Method: "POST", Query: url.Values{}})
	if err != nil {
		t.Fatalf("отсутствие токена не должно быть ошибкой: %v", err)
	}
	if got := sender.last.GetAuth().GetAuthorizationHeader(); got != "" {
		t.Fatalf("ожидался пустой Authorization, got %q", got)
	}
}

// Внешний узел не ответил: Sender возвращает status_code=0. Клиент получает
// 502, а на ТАЙМАУТ — 504: для вызывающей стороны это разные ситуации (таймаут
// имеет смысл повторить, отказ соединения — нет).
//
// Красный на коде до ревью: там 504 стоял под `errors.Is(err, ...)`, где err в
// этой точке всегда nil (ошибку вернули строкой выше), поэтому ветка была
// недостижима и таймаут всегда отдавался как 502 — вопреки контракту proto.
func TestRoute_UpstreamNoResponse_StatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout bool
		want    int
	}{
		{name: "таймаут внешнего узла", timeout: true, want: http.StatusGatewayTimeout},
		{name: "отказ соединения/DNS", timeout: false, want: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := stubSenderClient{resp: &senderv1.SendResponse{
				StatusCode: 0,
				Error:      "upstream did not respond",
				Timeout:    tt.timeout,
			}}
			u := NewRouteUsecase(stubNodeReader{node: methodTestNode()}, sender, 5, logging.NewNoop())

			out, err := u.Route(context.Background(), RouteInput{NodePath: "demo/path", Method: "POST"})
			require.NoError(t, err)
			assert.Equal(t, tt.want, out.StatusCode)
			assert.JSONEq(t, `{"error":"upstream unavailable"}`, string(out.Body))
		})
	}
}
