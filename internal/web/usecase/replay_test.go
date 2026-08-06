package usecase

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
func (s *stubNodeRepo) UpdateAllowedHostsSnapshot(_ context.Context, _ string, _ []string, _ string) error {
	return nil
}

// stubLogReader — управляемый port.LogReader. Интерфейс встроен, а не
// перечислен методами: расширение порта (§79.1 переводил CountFailed/FailedIDs
// на LogQuery) иначе ломает стаб на ровном месте. Непереопределённый метод в
// тестах replay не вызывается — вызов дал бы nil-панику, и это правильный
// сигнал «тест трогает то, что не собирался».
type stubLogReader struct {
	port.LogReader
	log       *domain.LogRecord
	err       error
	failedIDs []string      // §36.11: для ReplayFailed
	gotFailed port.LogQuery // §79.1: с каким запросом спросили неудачные
	// §81.5: набор, который стаб отдаёт, КОГДА попросили отсев по маркеру
	// причины. Пустой — отсев не эмулируется (старое поведение).
	failedIDsFiltered []string
	gotExcludes       [][]string
}

func (s *stubLogReader) GetByID(_ context.Context, _, _ string) (*domain.LogRecord, error) {
	return s.log, s.err
}
func (s *stubLogReader) FailedIDs(_ context.Context, q port.LogQuery, _ int) ([]string, bool, error) {
	s.gotFailed = q
	s.gotExcludes = append(s.gotExcludes, q.ExcludeReasonPrefixes)
	if len(q.ExcludeReasonPrefixes) > 0 && s.failedIDsFiltered != nil {
		return s.failedIDsFiltered, false, s.err
	}
	return s.failedIDs, false, s.err
}
func (s *stubLogReader) GetByIDPreview(_ context.Context, _, _ string, _ int) (*domain.LogRecord, int64, int64, error) {
	return s.log, 0, 0, s.err
}
func (s *stubLogReader) GetBodyChunk(_ context.Context, _, _, _ string, _, _ int) (string, int64, error) {
	return "", 0, s.err
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

// TestReplay_MultipartBlocked (§68): у оригинала было multipart/form-data тело,
// в логе — плейсхолдер, не тело. Без ручного override replay отклоняется 422 и
// во внешний target ничего не уходит.
func TestReplay_MultipartBlocked(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	placeholder := domain.MultipartLogPlaceholder(
		"multipart/form-data; boundary=abc",
		[]byte("--abc\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.pdf\"\r\n\r\ndata\r\n--abc--\r\n"),
	)
	log := &domain.LogRecord{ID: "log1", Method: "POST", Request: placeholder, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if !errors.Is(err, ErrReplayBodyMultipart) {
		t.Fatalf("want ErrReplayBodyMultipart, got %v", err)
	}
	if disp.gotReq.NodePath != "" {
		t.Fatalf("dispatch must not be called for multipart original")
	}
}

// TestReplay_MultipartWithOverride (§68): плейсхолдер в логе, но пользователь
// задал тело вручную → replay проходит с этим телом.
func TestReplay_MultipartWithOverride(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	placeholder := domain.MultipartLogPlaceholder("multipart/form-data; boundary=abc", []byte("--abc--\r\n"))
	log := &domain.LogRecord{ID: "log1", Method: "POST", Request: placeholder, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		BodyOverride: []byte(`{"manual":1}`), UseNodeAuth: true,
	})
	if err != nil {
		t.Fatalf("override must replay: %v", err)
	}
	if string(disp.gotReq.Body) != `{"manual":1}` {
		t.Fatalf("override body ignored: %q", string(disp.gotReq.Body))
	}
}

// newReplayUC — общий конструктор для компактных тестов GET/params.
func newReplayUC(node *domain.Node, log *domain.LogRecord, disp *stubDispatcher) *ReplayUsecase {
	return NewReplayUsecase(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}},
		disp, nil,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10, logging.NewNoop(),
	)
}

// TestReplay_GETWithoutBody_Allowed: у GET тела нет by design — пустой
// orig.Request не должен блокировать replay 422 (боевой кейс legat_by).
func TestReplay_GETWithoutBody_Allowed(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/get", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "t.t", IncomingMethod: domain.HTTPMethodGET,
	}
	log := &domain.LogRecord{ID: "log1", Request: "", DateRequest: time.Now(), Done: false, Parameters: "key=abc"}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("GET replay without body must pass: %v", err)
	}
	if disp.gotReq.Method != "GET" {
		t.Fatalf("method: %q", disp.gotReq.Method)
	}
	if len(disp.gotReq.Body) != 0 {
		t.Fatalf("GET replay must dispatch empty body, got %q", disp.gotReq.Body)
	}
	if disp.gotReq.Query.Get("key") != "abc" {
		t.Fatalf("original params must be preserved, got %v", disp.gotReq.Query)
	}
}

// TestReplay_AnyIncoming_LoggedGET_NoBody: ANY-узел + залогированный GET —
// тело тоже не требуется (метод реинъекции берётся из orig.HTTPMethod).
func TestReplay_AnyIncoming_LoggedGET_NoBody(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/any", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "t.t", IncomingMethod: domain.HTTPMethodAny,
	}
	log := &domain.LogRecord{ID: "log1", HTTPMethod: "GET", Request: "", DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("ANY+GET replay without body must pass: %v", err)
	}
	if disp.gotReq.Method != "GET" {
		t.Fatalf("method: %q", disp.gotReq.Method)
	}
}

// TestReplay_ParamsOverride: явный override вытесняет параметры оригинала,
// __replay_of добавляется поверх.
func TestReplay_ParamsOverride(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true, Parameters: "old=1"}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	override := "a=1&b=two"
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		UseNodeAuth: true, ParamsOverride: &override,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	q := disp.gotReq.Query
	if q.Get("a") != "1" || q.Get("b") != "two" {
		t.Fatalf("override params lost: %v", q)
	}
	if q.Get("old") != "" {
		t.Fatalf("original params must be replaced by override: %v", q)
	}
	if q.Get("__replay_of") != "log1" {
		t.Fatalf("__replay_of must survive override: %v", q)
	}
}

// TestReplay_ParamsOverride_Empty: пустая строка (не nil) — намеренный replay
// без параметров; остаётся только служебный маркер.
func TestReplay_ParamsOverride_Empty(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true, Parameters: "old=1"}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	empty := ""
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		UseNodeAuth: true, ParamsOverride: &empty,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	q := disp.gotReq.Query
	if len(q) != 1 || q.Get("__replay_of") != "log1" {
		t.Fatalf("empty override must leave only __replay_of, got %v", q)
	}
}

// stubTeamResolver — ReplayTeamResolver для тестов слага команды (§18).
type stubTeamResolver struct {
	teams map[string]*domain.Team
}

func (s *stubTeamResolver) GetByID(_ context.Context, id string) (*domain.Team, error) {
	if t, ok := s.teams[id]; ok {
		return t, nil
	}
	return nil, errors.New("team not found")
}

// TestReplay_NonDefaultTeam_PrefixesSlug (§18): узел не-default команды
// реинъектится по пути со слагом — без него Receiver ищет путь в default и
// отвечает 404 "node not found" (боевой баг: replay узла команды vika).
func TestReplay_NonDefaultTeam_PrefixesSlug(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "legat_by", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "t.t", TeamID: "team-vika",
		IncomingMethod: domain.HTTPMethodGET, PathPassthrough: true,
	}
	log := &domain.LogRecord{ID: "log1", Method: "api2/by/data", Request: "", DateRequest: time.Now(), Done: false}
	disp := &stubDispatcher{}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		nil,
		&stubTeamResolver{teams: map[string]*domain.Team{"team-vika": {ID: "team-vika", Slug: "vika"}}},
		0, logging.NewNoop(),
	)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Слаг команды + путь узла + passthrough-подпуть.
	if disp.gotReq.NodePath != "vika/legat_by/api2/by/data" {
		t.Fatalf("node path must carry team slug, got %q", disp.gotReq.NodePath)
	}
}

// TestReplay_DefaultTeam_NoSlugPrefix: default-команда — legacy-путь без слага.
func TestReplay_DefaultTeam_NoSlugPrefix(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "t.t", TeamID: "team-default",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		nil,
		&stubTeamResolver{teams: map[string]*domain.Team{"team-default": {ID: "team-default", Slug: domain.DefaultTeamSlug}}},
		0, logging.NewNoop(),
	)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if disp.gotReq.NodePath != "demo/x" {
		t.Fatalf("default team must use legacy path without slug, got %q", disp.gotReq.NodePath)
	}
}

// TestReplay_TeamResolveError_FallsBackNoSlug: ошибка резолва команды не
// блокирует replay — путь уходит без слага (для default это корректно).
func TestReplay_TeamResolveError_FallsBackNoSlug(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled,
		ClickHouseTable: "t.t", TeamID: "unknown-team",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{log: log},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		nil, &stubTeamResolver{teams: map[string]*domain.Team{}}, 0, logging.NewNoop(),
	)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay must not fail on team resolve error: %v", err)
	}
	if disp.gotReq.NodePath != "demo/x" {
		t.Fatalf("resolve error must fall back to path without slug, got %q", disp.gotReq.NodePath)
	}
}

// TestReplay_UseNodeAuth_IncomingBasic_AutofillsHeader: «Использовать
// авторизацию узла» обязан подставить ВХОДЯЩУЮ креду в запрос — replay идёт
// через реальный endpoint Receiver'а, и без неё узел с входящей basic отвечает
// 401 «authorization header missing» (боевой баг). Механизм §55.6 (как dry-run).
func TestReplay_UseNodeAuth_IncomingBasic_AutofillsHeader(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t",
		IncomingAuthType: domain.IncomingAuthTypeBasic, IncomingAuthCredentials: "u:p",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	// base64("u:p") = dTpw — та же схема, что проверяет checkIncomingBasic.
	if got := disp.gotReq.Headers["Authorization"]; got != "Basic dTpw" {
		t.Fatalf("incoming basic cred must be autofilled, got %q", got)
	}
}

// TestReplay_UseNodeAuth_IncomingTokenQuery_AutofillsParam: входящий token из
// query-параметра — креда подставляется в query, не в заголовок.
func TestReplay_UseNodeAuth_IncomingTokenQuery_AutofillsParam(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t",
		IncomingAuthType: domain.IncomingAuthTypeToken, IncomingAuthCredentials: "sec-token",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery, IncomingAuthDynamicField: "token",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := disp.gotReq.Query.Get("token"); got != "sec-token" {
		t.Fatalf("incoming query token must be autofilled, got %q", got)
	}
	if _, ok := disp.gotReq.Headers["Authorization"]; ok {
		t.Fatalf("query-source cred must not leak into Authorization header")
	}
}

// TestReplay_NoNodeAuth_SkipsAutofill: «Без авторизации» — креда НЕ
// подставляется (сценарий отладки 401 из §7.4.1 остаётся рабочим).
func TestReplay_NoNodeAuth_SkipsAutofill(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t",
		IncomingAuthType: domain.IncomingAuthTypeBasic, IncomingAuthCredentials: "u:p",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{UseNodeAuth: false})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, ok := disp.gotReq.Headers["Authorization"]; ok {
		t.Fatalf("UseNodeAuth=false must not autofill Authorization")
	}
}

// TestReplay_CustomAuth_WinsOverAutofill: явный Authorization оператора
// приоритетнее автоподстановки.
func TestReplay_CustomAuth_WinsOverAutofill(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t",
		IncomingAuthType: domain.IncomingAuthTypeBasic, IncomingAuthCredentials: "u:p",
	}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		UseNodeAuth: true, CustomAuth: "Bearer manual",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := disp.gotReq.Headers["Authorization"]; got != "Bearer manual" {
		t.Fatalf("custom auth must win, got %q", got)
	}
}

// TestReplay_ParamsOverride_Invalid: кривой query-override — явная 400-ошибка,
// dispatch не вызывается (в отличие от мусора в логе, который глотается).
func TestReplay_ParamsOverride_Invalid(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/x", Status: domain.NodeStatusEnabled, ClickHouseTable: "t.t"}
	log := &domain.LogRecord{ID: "log1", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
	disp := &stubDispatcher{}
	uc := newReplayUC(node, log, disp)

	bad := "%zz=1"
	_, err := uc.Replay(context.Background(), SystemActor(), "log1", "n1", "", ReplayOptions{
		UseNodeAuth: true, ParamsOverride: &bad,
	})
	if !errors.Is(err, ErrReplayBadParams) {
		t.Fatalf("want ErrReplayBadParams, got %v", err)
	}
	if disp.gotReq.NodePath != "" {
		t.Fatalf("dispatch must not be called on bad override")
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

// TestReplay_PathPassthrough_ReconstructsSubpath (§39): для passthrough-узла
// replay реинъектит по ПОЛНОМУ пути (узел + подпуть из колонки method), иначе
// запрос ушёл бы на корень узла, а не на исходный эндпоинт.
func TestReplay_PathPassthrough_ReconstructsSubpath(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID:              "n1",
		Path:            "demo/svc",
		Status:          domain.NodeStatusEnabled,
		ClickHouseTable: "t.t",
		IncomingMethod:  domain.HTTPMethodPOST,
		PathPassthrough: true,
	}
	// §39: orig.Method хранит подпуть passthrough (не глагол).
	log := &domain.LogRecord{ID: "log1", Method: "v1/GetParcelsInfo", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
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
	if disp.gotReq.NodePath != "demo/svc/v1/GetParcelsInfo" {
		t.Fatalf("passthrough replay must reinject full subpath, got %q", disp.gotReq.NodePath)
	}
}

// TestReplay_AnyIncomingUsesLoggedMethod (§40): у узла с IncomingMethod=ANY
// нет единственного входящего метода — для реинъекции берём залогированный
// глагол исходного запроса (orig.HTTPMethod, колонка http_method §39), а не
// литерал "ANY".
func TestReplay_AnyIncomingUsesLoggedMethod(t *testing.T) {
	t.Parallel()
	node := &domain.Node{
		ID:              "n1",
		Path:            "demo/any",
		Status:          domain.NodeStatusEnabled,
		ClickHouseTable: "t.t",
		IncomingMethod:  domain.HTTPMethodAny,
	}
	log := &domain.LogRecord{ID: "log1", HTTPMethod: "PUT", Request: `{"a":1}`, DateRequest: time.Now(), Done: true}
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
	if disp.gotReq.Method != "PUT" {
		t.Fatalf("ANY-узел: replay должен слать залогированный метод PUT, got %q", disp.gotReq.Method)
	}
}

// §36.11: «Повторить все сейчас» — пере-инжектирует все неудачные и отменяет их
// оригиналы в DLQ (без двойной доставки).
func TestReplay_ReplayFailed_AllAndCancelsOriginals(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now(), Done: false}
	disp := &stubDispatcher{}
	cancelW := &stubCancelWriter{}
	logs := &stubLogReader{log: log, failedIDs: []string{"f1", "f2", "f3"}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		cancelW, nil, time.Hour, logging.NewNoop(),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if res.Total != 3 || res.Replayed != 3 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(cancelW.gotIDs) != 3 {
		t.Fatalf("expected 3 originals cancelled, got %v", cancelW.gotIDs)
	}
}

// stubFailedCleaner — управляемый port.FailedLogsCleaner (§79.2).
type stubFailedCleaner struct {
	calls   int
	gotQ    port.LogQuery
	gotIDs  []string
	deleted uint64
	err     error
}

func (s *stubFailedCleaner) DeleteFailedRows(_ context.Context, q port.LogQuery, ids []string) (uint64, error) {
	s.calls++
	s.gotQ, s.gotIDs = q, ids
	return s.deleted, s.err
}

// §79.1: набор для повтора — записи БЕЗ успешного прогона. По строкам done=0
// «Повторить все» захватывало уже доставленные и слало дубли получателю
// (бой 2026-08-05: 15 дублей в 1С из 16 повторённых сообщений).
func TestReplay_ReplayFailed_AsksUnresolvedRecords(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	logs := &stubLogReader{log: &domain.LogRecord{ID: "x", Request: `{"a":1}`, DateRequest: time.Now()}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
	)

	if _, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{}); err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if !logs.gotFailed.Unresolved {
		t.Fatal("want Unresolved=true: уже доставленные записи повторять нельзя")
	}
	if logs.gotFailed.Done != "" {
		t.Fatalf("Done — фильтр СТРОК журнала, здесь он неуместен: %q", logs.gotFailed.Done)
	}
	if !logs.gotFailed.DateCreateAligned {
		t.Fatal("§72.4: сужение по партициям обязано доехать до набора ID")
	}
}

// §79.2: после успешного повтора строки done=0 оригиналов убираются ОДНИМ
// вызовом и только для тех записей, что реально уехали.
func TestReplay_ReplayFailed_CleansOriginalsAfterSuccess(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now()}
	cleaner := &stubFailedCleaner{deleted: 2}
	logs := &stubLogReader{log: log, failedIDs: []string{"f1", "f2"}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
		WithFailedCleaner(cleaner),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("want ровно один batch-вызов очистки, got %d", cleaner.calls)
	}
	if len(cleaner.gotIDs) != 2 {
		t.Fatalf("want очистку обеих записей, got %v", cleaner.gotIDs)
	}
	if res.Cleaned != 2 {
		t.Fatalf("want Cleaned=2, got %+v", res)
	}
}

// §79.2: запись, которую не удалось пере-инжектировать, из «Неудачных доставок»
// не убирается — она остаётся авто-репроцессору.
func TestReplay_ReplayFailed_CleansOnlyReplayed(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	cleaner := &stubFailedCleaner{deleted: 1}
	// Тело не сохранено (Request пуст) → replayOne отказывает на КАЖДОЙ записи.
	logs := &stubLogReader{log: &domain.LogRecord{ID: "x", DateRequest: time.Now()}, failedIDs: []string{"f1"}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
		WithFailedCleaner(cleaner),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if res.Failed != 1 || res.Replayed != 0 {
		t.Fatalf("предпосылка кейса не выполнена: %+v", res)
	}
	if cleaner.calls != 0 {
		t.Fatalf("нечего убирать — очистка не должна вызываться, got %d", cleaner.calls)
	}
	if res.Cleaned != 0 {
		t.Fatalf("want Cleaned=0, got %+v", res)
	}
}

// §79.2: отказ очистки (гейт владения §70.4 на чужой/внешней таблице) НЕ роняет
// операцию — сообщения к этому моменту уже отправлены во внешнюю систему.
func TestReplay_ReplayFailed_CleanupFailureDoesNotFailOperation(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now()}
	cleaner := &stubFailedCleaner{err: domain.ErrCHForeignDatabase}
	logs := &stubLogReader{log: log, failedIDs: []string{"f1"}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
		WithFailedCleaner(cleaner),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("отказ очистки не имеет права ронять операцию: %v", err)
	}
	if res.Replayed != 1 {
		t.Fatalf("сообщение обязано числиться отправленным: %+v", res)
	}
	if res.Cleaned != 0 {
		t.Fatalf("want Cleaned=0 при отказе очистки, got %+v", res)
	}
}

// Без cleaner'а (инсталляция без ClickHouse) поведение прежнее: повтор
// работает, очистки нет.
func TestReplay_ReplayFailed_NoCleaner_Works(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now()}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{log: log, failedIDs: []string{"f1"}},
		&stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if res.Replayed != 1 || res.Cleaned != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// При ошибке dispatch — оригинал НЕ отменяется (остаётся авто-репроцессору).
func TestReplay_ReplayFailed_DispatchError_KeepsOriginals(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now(), Done: false}
	disp := &stubDispatcher{err: errors.New("receiver down")}
	cancelW := &stubCancelWriter{}
	logs := &stubLogReader{log: log, failedIDs: []string{"f1", "f2"}}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		cancelW, nil, time.Hour, logging.NewNoop(),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("replay-all: %v", err)
	}
	if res.Replayed != 0 || res.Failed != 2 {
		t.Fatalf("expected all failed, got %+v", res)
	}
	if len(cancelW.gotIDs) != 0 {
		t.Fatalf("failed replays must not cancel originals, got %v", cancelW.gotIDs)
	}
}

// disabled-узел → ошибка (не реинжектим в отключённый узел).
func TestReplay_ReplayFailed_DisabledNode(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusDisabled, ClickHouseTable: "test.async"}
	uc := NewReplayUsecaseWithCancel(
		&stubLogReader{failedIDs: []string{"f1"}}, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		&stubDispatcher{}, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
	)
	_, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	if !errors.Is(err, domain.ErrNodeDisabled) {
		t.Fatalf("expected ErrNodeDisabled, got %v", err)
	}
}

// §81.5: массовый повтор пропускает записи, где ушла ВЫЗЫВАЮЩАЯ сторона —
// приёмник их, скорее всего, уже обработал (тело принял целиком), и повтор дал
// бы дубли (урок §79: 15 дублей в 1С на узле kz). Очистка «неудачных» такие
// записи, наоборот, не щадит — убрать их с глаз это ровно то, что от неё ждут.
func TestReplayFailed_SkipsClientCanceled(t *testing.T) {
	t.Parallel()

	node := &domain.Node{ID: "n1", Path: "demo/async", Status: domain.NodeStatusEnabled, ClickHouseTable: "test.async"}
	log := &domain.LogRecord{ID: "x", Method: "POST", Type: domain.RootMethodRequestAsync, Request: `{"a":1}`, DateRequest: time.Now(), Done: false}
	disp := &stubDispatcher{}
	logs := &stubLogReader{
		log:               log,
		failedIDs:         []string{"f1", "f2", "f3"}, // всего недоставленных в окне
		failedIDsFiltered: []string{"f1"},             // из них безопасны для повтора
	}
	uc := NewReplayUsecaseWithCancel(
		logs, &stubNodeRepo{nodes: map[string]*domain.Node{"n1": node}},
		disp, nil, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), 10,
		&stubCancelWriter{}, nil, time.Hour, logging.NewNoop(),
	)

	res, err := uc.ReplayFailed(context.Background(), SystemActor(), "n1", "", time.Time{}, time.Time{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Total, "в работу берём только то, что повторять безопасно")
	assert.Equal(t, 1, res.Replayed)
	assert.Equal(t, 2, res.SkippedClientCanceled, "пропущенные показываем, а не теряем молча")

	// Отсев запрошен именно маркером §81.2.1, и только во втором вызове:
	// первый считает полное множество для счётчика пропущенных.
	require.Len(t, logs.gotExcludes, 2)
	assert.Empty(t, logs.gotExcludes[0])
	assert.Equal(t, []string{domain.ReasonClientCanceled}, logs.gotExcludes[1])
}
