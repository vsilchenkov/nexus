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

func (s stubNodeReader) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
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
	u := NewRouteUsecase(stubNodeReader{node: node}, stubSenderClient{}, logging.NewNoop())
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
	u := NewRouteUsecase(stubNodeReader{node: node}, stubSenderClient{}, logging.NewNoop())
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
	u := NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "databus.async", logging.NewNoop())
	res, err := u.RouteAsync(context.Background(), RouteInput{NodePath: "demo/path"})
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
