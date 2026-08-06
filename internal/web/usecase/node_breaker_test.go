package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// stubBreakerPort — управляемый port.NodeBreaker; фиксирует, по какому пути
// узла его звали (ключ breaker'а — именно node.Path, а не id).
type stubBreakerPort struct {
	state     port.BreakerState
	stateErr  error
	resetErr  error
	gotPaths  []string
	resetCall int
}

func (s *stubBreakerPort) State(_ context.Context, nodePath string) (port.BreakerState, error) {
	s.gotPaths = append(s.gotPaths, nodePath)
	return s.state, s.stateErr
}

func (s *stubBreakerPort) Reset(_ context.Context, nodePath string) (port.BreakerState, error) {
	s.gotPaths = append(s.gotPaths, nodePath)
	s.resetCall++
	return s.state, s.resetErr
}

// stubStatusResetter — снятие бейджа «Down»; умеет отказывать.
type stubStatusResetter struct {
	err      error
	gotPaths []string
}

func (s *stubStatusResetter) ClearLastOutcome(_ context.Context, nodePath string) error {
	s.gotPaths = append(s.gotPaths, nodePath)
	return s.err
}

func newBreakerUC(breaker port.NodeBreaker, status port.NodeStatusResetter, node *domain.Node) (*NodeBreakerUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{}}
	if node != nil {
		nodes.nodes[node.ID] = node
	}
	return NewNodeBreakerUsecase(breaker, status, nodes,
		NewAuditUsecase(repo, logging.NewNoop()), logging.NewNoop()), repo
}

func breakerNode() *domain.Node {
	return &domain.Node{ID: "n1", Path: "partner/echo", TeamID: "t1", RootMethod: domain.RootMethodRequest}
}

func TestBreakerUC_State_OpenNode(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{state: port.BreakerState{
		Exists: true, State: domain.BreakerStateOpen, Failures: 5, Threshold: 5,
		OpenedAt: time.Now().Add(-10 * time.Second), RetryAfter: 20 * time.Second,
	}}
	uc, _ := newBreakerUC(br, &stubStatusResetter{}, breakerNode())

	got, err := uc.State(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.True(t, got.Available)
	assert.Equal(t, domain.BreakerStateOpen, got.State)
	assert.Equal(t, 5, got.Failures)
	assert.Equal(t, 5, got.Threshold)
	assert.Equal(t, 20*time.Second, got.RetryAfter)
	assert.Equal(t, []string{"partner/echo"}, br.gotPaths, "ключ breaker'а — путь узла")
}

// Узел, по которому breaker ни разу не срабатывал: карточка обязана показать
// «закрыт», а не пустоту или ошибку.
func TestBreakerUC_State_NeverOpened(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{state: port.BreakerState{Exists: false, State: domain.BreakerStateClosed}}
	uc, _ := newBreakerUC(br, &stubStatusResetter{}, breakerNode())

	got, err := uc.State(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.True(t, got.Available)
	assert.False(t, got.Exists)
	assert.Equal(t, domain.BreakerStateClosed, got.State)
}

// Без Redis защиты в инсталляции нет вообще — это честный ответ, а не ошибка.
func TestBreakerUC_State_WithoutRedis(t *testing.T) {
	t.Parallel()

	uc, _ := newBreakerUC(nil, nil, breakerNode())

	got, err := uc.State(context.Background(), "n1", "t1")
	require.NoError(t, err)
	assert.False(t, got.Available)
	assert.Equal(t, domain.BreakerStateClosed, got.State)
}

// Ошибка Redis НЕ маскируется под «закрыт»: соврать оператору хуже, чем
// сказать «не знаю».
func TestBreakerUC_State_RedisError_NotFailOpen(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{stateErr: errors.New("redis down")}
	uc, _ := newBreakerUC(br, &stubStatusResetter{}, breakerNode())

	_, err := uc.State(context.Background(), "n1", "t1")
	assert.ErrorIs(t, err, ErrBreakerUnavailable)
}

func TestBreakerUC_State_ForeignTeam(t *testing.T) {
	t.Parallel()

	uc, _ := newBreakerUC(&stubBreakerPort{}, &stubStatusResetter{}, breakerNode())

	_, err := uc.State(context.Background(), "n1", "other-team")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound, "чужой узел неотличим от несуществующего")
}

func TestBreakerUC_Reset_OpenNode(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{state: port.BreakerState{
		Exists: true, State: domain.BreakerStateOpen, Failures: 5,
	}}
	st := &stubStatusResetter{}
	uc, audit := newBreakerUC(br, st, breakerNode())

	res, err := uc.Reset(context.Background(), Actor{UserLogin: "op"}, "n1", "t1")
	require.NoError(t, err)
	assert.True(t, res.WasOpen)
	assert.Equal(t, domain.BreakerStateOpen, res.PreviousState, "в ответе состояние ДО сброса")
	assert.Equal(t, 5, res.PreviousFailures)
	assert.True(t, res.NodeStatusCleared)
	assert.Equal(t, []string{"partner/echo"}, st.gotPaths, "бейдж «Down» снят тем же путём")

	require.Len(t, audit.entries, 1)
	e := audit.entries[0]
	assert.Equal(t, domain.ActionNodeBreakerReset, e.Action)
	assert.Equal(t, "node", e.TargetType)
	assert.Equal(t, "n1", e.TargetID)
	assert.Equal(t, true, e.Details["was_open"])
	assert.Equal(t, domain.BreakerStateOpen, e.Details["previous_state"])
}

// Нажатие на закрытом breaker'е — не ошибка, но в аудит попадает: при разборе
// важно знать, что оператор вмешивался.
func TestBreakerUC_Reset_AlreadyClosed_IsAudited(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{state: port.BreakerState{Exists: false, State: domain.BreakerStateClosed}}
	uc, audit := newBreakerUC(br, &stubStatusResetter{}, breakerNode())

	res, err := uc.Reset(context.Background(), Actor{UserLogin: "op"}, "n1", "t1")
	require.NoError(t, err)
	assert.False(t, res.WasOpen)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, false, audit.entries[0].Details["was_open"])
}

// Отказ снятия бейджа не роняет сброс: главное (защита снята) уже сделано.
func TestBreakerUC_Reset_StatusClearFails_StillSucceeds(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{state: port.BreakerState{Exists: true, State: domain.BreakerStateOpen, Failures: 3}}
	st := &stubStatusResetter{err: errors.New("redis del failed")}
	uc, audit := newBreakerUC(br, st, breakerNode())

	res, err := uc.Reset(context.Background(), Actor{UserLogin: "op"}, "n1", "t1")
	require.NoError(t, err, "сброс breaker'а состоялся — операция успешна")
	assert.False(t, res.NodeStatusCleared, "но оператор видит, что бейдж не снят")
	require.Len(t, audit.entries, 1)
	assert.Equal(t, false, audit.entries[0].Details["node_status_cleared"])
}

func TestBreakerUC_Reset_WithoutRedis(t *testing.T) {
	t.Parallel()

	uc, audit := newBreakerUC(nil, nil, breakerNode())

	_, err := uc.Reset(context.Background(), Actor{UserLogin: "op"}, "n1", "t1")
	assert.ErrorIs(t, err, ErrBreakerUnavailable, "сбрасывать нечего — это отказ, а не успех")
	assert.Empty(t, audit.entries, "несостоявшееся действие в аудит не пишем")
}

func TestBreakerUC_Reset_ForeignTeam(t *testing.T) {
	t.Parallel()

	br := &stubBreakerPort{}
	uc, audit := newBreakerUC(br, &stubStatusResetter{}, breakerNode())

	_, err := uc.Reset(context.Background(), Actor{UserLogin: "op"}, "n1", "other-team")
	assert.ErrorIs(t, err, domain.ErrNodeNotFound)
	assert.Zero(t, br.resetCall, "чужой узел не трогаем вовсе")
	assert.Empty(t, audit.entries)
}
