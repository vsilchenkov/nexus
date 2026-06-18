package usecase

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// stubNodeRepo — реализует port.NodeRepo минимально, чтобы тестировать
// поведение ReplayUsecase. В replay используется только Get(); остальные
// методы возвращают ErrNotFound.
type stubNodeRepo struct {
	nodes map[string]*domain.Node
}

func (s *stubNodeRepo) Get(_ context.Context, id string) (*domain.Node, error) {
	if n, ok := s.nodes[id]; ok {
		return n, nil
	}
	return nil, domain.ErrNodeNotFound
}
func (s *stubNodeRepo) GetByPath(_ context.Context, _ string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (s *stubNodeRepo) List(_ context.Context, _ port.ListNodesFilter) ([]*domain.Node, error) {
	return nil, nil
}
func (s *stubNodeRepo) Count(_ context.Context, _ string) (int, error) { return 0, nil }
func (s *stubNodeRepo) Create(_ context.Context, _ *domain.Node) error { return nil }
func (s *stubNodeRepo) Update(_ context.Context, _ *domain.Node) error { return nil }
func (s *stubNodeRepo) Delete(_ context.Context, _ string) error       { return nil }
func (s *stubNodeRepo) UpdateAllowedHostsSnapshot(_ context.Context, _ string, _ []string) error {
	return nil
}

// stubLogReader — реализует port.LogReader для одной запись.
type stubLogReader struct {
	log *domain.LogRecord
	err error
}

func (s *stubLogReader) GetByID(_ context.Context, _, _ string) (*domain.LogRecord, error) {
	return s.log, s.err
}
func (s *stubLogReader) ListSince(_ context.Context, _ string, _ int64, _ int) ([]*domain.LogRecord, error) {
	return nil, nil
}
func (s *stubLogReader) Search(_ context.Context, _ port.LogQuery) ([]*domain.LogRecord, error) {
	return nil, nil
}
func (s *stubLogReader) CountErrors(_ context.Context, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}
func (s *stubLogReader) CountFailed(_ context.Context, _ string, _, _ int64) (uint64, error) {
	return 0, nil
}

// stubDispatcher — реализует port.ReceiverDispatcher; сохраняет последний
// запрос для проверки.
type stubDispatcher struct {
	gotReq port.DispatchRequest
	resp   *port.DispatchResponse
	err    error
}

func (s *stubDispatcher) Dispatch(_ context.Context, req port.DispatchRequest) (*port.DispatchResponse, error) {
	s.gotReq = req
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &port.DispatchResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)}, nil
}

func TestReplay_HappyPath(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/sync", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.demo"}
	log := &domain.LogRecord{
		ID:          "log1",
		Method:      "POST",
		Type:        domain.RootMethodRequest,
		Request:     `{"orig":true}`,
		DateRequest: time.Now(),
		Done:        true,
		Parameters:  "foo=bar",
	}

	disp := &stubDispatcher{}
	auditRepo := &stubAuditRepo{}
	auditUC := NewAuditUsecase(auditRepo, logging.NewNoop())

	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp,
		nil, // rate limiter не нужен — actor.UserID="" пропускает Allow()
		auditUC,
		10,
		logging.NewNoop(),
	)

	res, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status: %d", res.StatusCode)
	}
	// Маркер __replay_of должен попасть в query.
	q, _ := url.ParseQuery(disp.gotReq.Query.Encode())
	if q.Get("__replay_of") != "log1" {
		t.Fatalf("missing __replay_of marker; got query=%v", q)
	}
	// Audit-запись node.replay.
	if len(auditRepo.entries) != 1 || auditRepo.entries[0].Action != domain.ActionNodeReplay {
		t.Fatalf("expected one node.replay audit entry, got %+v", auditRepo.entries)
	}
}

func TestReplay_NodeDisabled(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusDisabled}
	uc := NewReplayUsecase(
		&stubLogReader{log: &domain.LogRecord{ID: "log1", DateRequest: time.Now()}},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{},
		nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{})
	if !errors.Is(err, domain.ErrNodeDisabled) {
		t.Fatalf("want ErrNodeDisabled, got %v", err)
	}
}

func TestReplay_TooOldFailure(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{
		ID:          "log1",
		DateRequest: time.Now().AddDate(0, 0, -10),
		Done:        false,
	}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{},
		nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{})
	if !errors.Is(err, ErrReplayTooOldFailure) {
		t.Fatalf("want ErrReplayTooOldFailure, got %v", err)
	}
}

type stubRateLimiter struct {
	allow bool
}

func (s *stubRateLimiter) Allow(_ context.Context, _ string, _ int) (bool, error) {
	return s.allow, nil
}

func TestReplay_RateLimit(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	uc := NewReplayUsecase(
		&stubLogReader{log: &domain.LogRecord{ID: "log1", DateRequest: time.Now()}},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{},
		&stubRateLimiter{allow: false},
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	// Только пользователи с UserID попадают под rate-limit (актёр "system" — нет).
	actor := Actor{UserID: "u1", UserLogin: "alice"}
	_, err := uc.Replay(context.Background(), actor, "log1", "n1", "", ReplayOptions{})
	if !errors.Is(err, ErrReplayRateLimit) {
		t.Fatalf("want ErrReplayRateLimit, got %v", err)
	}
}

func TestReplay_BodyOverride(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{
		ID:          "log1",
		Method:      "POST",
		Request:     `{"orig":true}`,
		DateRequest: time.Now(),
		Done:        true,
	}
	disp := &stubDispatcher{}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		BodyOverride: []byte(`{"new":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(disp.gotReq.Body) != `{"new":1}` {
		t.Fatalf("body override ignored: %q", string(disp.gotReq.Body))
	}
}

// TestReplay_BodyUnavailable (QA-2026-02 / П1): узел не логировал тело
// (orig.Request пуст) и override не задан → явная ошибка вместо отправки
// пустого тела во внешний target (которое возвращало невнятную 400).
func TestReplay_BodyUnavailable(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Method: "POST", Request: "", DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if !errors.Is(err, ErrReplayBodyUnavailable) {
		t.Fatalf("want ErrReplayBodyUnavailable, got %v", err)
	}
	if disp.gotReq.NodePath != "" {
		t.Fatalf("dispatch must not be called when body unavailable")
	}
}

// TestReplay_ExplicitEmptyBody (QA-2026-02 / П1): если пользователь намеренно
// задал пустое тело (BodyOverride = []byte{}), replay выполняется — это не
// «отсутствие тела», а осознанный выбор.
func TestReplay_ExplicitEmptyBody(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Method: "POST", Request: "", DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		BodyOverride: []byte{}, UseNodeAuth: true,
	})
	if err != nil {
		t.Fatalf("explicit empty body must replay: %v", err)
	}
}

// TestReplay_UsesIncomingMethod (§34.5): replay должен слать ВХОДЯЩИЙ метод
// узла, а не залогированный исходящий (orig.Method == OutgoingMethod). Узел
// POST-in / GET-out (внешний GET без тела) логировал method=GET; раньше replay
// слал GET во входной endpoint и получал 405 ErrNodeMethodNotAllowed. Теперь —
// IncomingMethod (POST).
func TestReplay_UsesIncomingMethod(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID:              "n1",
		Path:            "demo/async",
		Status:          domain.NodeStatusEnabled,
		ClickHouseTable: "t.t",
		IncomingMethod:  domain.HTTPMethodPOST,
		OutgoingMethod:  domain.HTTPMethodGET,
	}
	// В логе зафиксирован исходящий метод GET (то, чем Sender ходил наружу).
	log := &domain.LogRecord{ID: "log1", Method: "GET", Request: `{"orig":true}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if disp.gotReq.Method != "POST" {
		t.Fatalf("replay must dispatch IncomingMethod POST, got %q (regression of 405)", disp.gotReq.Method)
	}
}

// TestReplay_IncomingMethodEmptyDefaultsPost (§34.5): пустой IncomingMethod
// узла трактуется как POST — так же, как methodMatches в Receiver.
func TestReplay_IncomingMethodEmptyDefaultsPost(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Method: "GET", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if disp.gotReq.Method != "POST" {
		t.Fatalf("empty IncomingMethod must default to POST, got %q", disp.gotReq.Method)
	}
}
