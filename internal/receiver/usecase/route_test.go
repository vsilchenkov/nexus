package usecase

import (
	"context"
	"errors"
	"testing"

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
