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

// periodReader — управляемый LogReader для §85: отдаёт заранее заданные
// страницы кандидатов и запоминает курсоры, с которыми его звали.
type periodReader struct {
	port.LogReader
	pages   []replayCandPage
	calls   []port.ReplayCursor
	total   uint64
	err     error
	gotQ    port.LogQuery
	getByID *domain.LogRecord
}

type replayCandPage struct {
	items []domain.ReplayCandidate
	next  port.ReplayCursor
}

func (r *periodReader) ReplayCandidates(
	_ context.Context, q port.LogQuery, after port.ReplayCursor, _ int,
) ([]domain.ReplayCandidate, port.ReplayCursor, error) {
	r.gotQ = q
	r.calls = append(r.calls, after)
	if r.err != nil {
		return nil, port.ReplayCursor{}, r.err
	}
	if len(r.pages) == 0 {
		return nil, port.ReplayCursor{}, nil
	}
	p := r.pages[0]
	r.pages = r.pages[1:]
	return p.items, p.next, nil
}

func (r *periodReader) Count(_ context.Context, q port.LogQuery) (uint64, error) {
	r.gotQ = q
	return r.total, r.err
}

func (r *periodReader) GetByID(_ context.Context, _, id string) (*domain.LogRecord, error) {
	if r.getByID != nil {
		cp := *r.getByID
		cp.ID = id
		return &cp, nil
	}
	return &domain.LogRecord{
		ID:          id,
		Type:        domain.RootMethodRequestAsync,
		Request:     `{"ok":1}`,
		DateRequest: time.Now(),
		Done:        true,
	}, nil
}

// periodNode — узел, на котором операция §85 законна.
func periodNode() *domain.Node {
	return &domain.Node{
		ID:              "n1",
		Path:            "demo/async",
		Status:          domain.NodeStatusEnabled,
		RootMethod:      domain.RootMethodRequestAsync,
		IncomingMethod:  domain.HTTPMethodPOST,
		ClickHouseTable: "test.demo",
		LoggingEnabled:  true,
		LogRequestBody:  true,
	}
}

// okCandidate — пригодная к повтору запись.
func okCandidate(id string, at time.Time) domain.ReplayCandidate {
	return domain.ReplayCandidate{
		ID:          id,
		DateRequest: at,
		Type:        domain.RootMethodRequestAsync,
		HTTPMethod:  "POST",
		BodyHead:    `{"ok":1}`,
		StoredBytes: 8,
		RequestSize: 8,
	}
}

func newPeriodUC(t *testing.T, reader port.LogReader, node *domain.Node, disp port.ReceiverDispatcher) (*ReplayUsecase, *stubAuditRepo) {
	t.Helper()
	auditRepo := &stubAuditRepo{}
	nodes := &stubNodeRepo{nodes: map[string]*domain.Node{}}
	if node != nil {
		nodes.nodes[node.ID] = node
	}
	uc := NewReplayUsecase(
		reader, nodes, disp, nil,
		NewAuditUsecase(auditRepo, logging.NewNoop()),
		10, logging.NewNoop(),
	)
	return uc, auditRepo
}

func periodInput() ReplayPeriodInput {
	return ReplayPeriodInput{
		NodeID: "n1",
		Filter: port.LogQuery{
			SinceMs: time.Now().Add(-time.Hour).UnixMilli(),
			UntilMs: time.Now().UnixMilli(),
		},
		SkipReplayCopies: true,
	}
}

// TestReplayPeriod_CursorAdvancesThroughSkipped — курсор обязан двигаться и
// через записи, которые НЕ отправлены. Иначе следующий батч начнёт с них же и
// прогон зациклится, не сдвинувшись ни на запись.
func TestReplayPeriod_CursorAdvancesThroughSkipped(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	sync := okCandidate("skip-1", at)
	sync.Type = domain.RootMethodRequest // будет пропущена

	reader := &periodReader{pages: []replayCandPage{
		{items: []domain.ReplayCandidate{sync}, next: port.ReplayCursor{AfterMs: at.UnixMilli(), AfterID: "skip-1"}},
	}}
	disp := &stubDispatcher{}
	uc, _ := newPeriodUC(t, reader, periodNode(), disp)

	res, err := uc.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)

	assert.Equal(t, 1, res.Scanned)
	assert.Equal(t, 0, res.Replayed)
	assert.Equal(t, 1, res.SkippedBy[string(domain.ReplaySkipNotAsync)])
	require.NotNil(t, res.NextCursor, "курсор обязан быть отдан: окно не пройдено")
	assert.Equal(t, "skip-1", res.NextCursor.AfterID,
		"курсор стоит на пропущенной записи — иначе следующий батч возьмёт её снова")
}

// TestReplayPeriod_FailedRecordDoesNotAbortBatch — отказ одной записи не роняет
// батч: остальные обязаны уйти.
func TestReplayPeriod_FailedRecordDoesNotAbortBatch(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	reader := &periodReader{pages: []replayCandPage{{
		items: []domain.ReplayCandidate{okCandidate("a", at), okCandidate("b", at.Add(time.Second))},
	}}}
	disp := &failOnceDispatcher{failFor: "a"}
	uc, _ := newPeriodUC(t, reader, periodNode(), disp)

	res, err := uc.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)
	assert.Equal(t, 2, res.Scanned)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 1, res.Replayed)
	assert.Nil(t, res.NextCursor, "страница пришла без курсора — окно пройдено")
}

// failOnceDispatcher — падает на реинжекции записи, чей идентификатор пришёл в
// служебном параметре __replay_of.
type failOnceDispatcher struct {
	failFor string
}

func (d *failOnceDispatcher) Dispatch(_ context.Context, req port.DispatchRequest) (*port.DispatchResponse, error) {
	if req.Query.Get(domain.ReplayOfParam) == d.failFor {
		return nil, errors.New("upstream refused")
	}
	return &port.DispatchResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)}, nil
}

// TestReplayPeriod_AuditPerBatch — след пишется на каждый батч, включая
// прерванный прогон: иначе закрытая вкладка не оставляет записи о том, что уже
// ушло получателю.
func TestReplayPeriod_AuditPerBatch(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	reader := &periodReader{pages: []replayCandPage{{
		items: []domain.ReplayCandidate{okCandidate("a", at)},
		next:  port.ReplayCursor{AfterMs: at.UnixMilli(), AfterID: "a"},
	}}}
	uc, auditRepo := newPeriodUC(t, reader, periodNode(), &stubDispatcher{})

	_, err := uc.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)

	require.Len(t, auditRepo.entries, 1)
	e := auditRepo.entries[0]
	assert.Equal(t, domain.ActionNodeReplayPeriod, e.Action,
		"отдельное действие от node.replay: фильтр аудита работает по action")
	assert.Equal(t, 1, e.Details["replayed"])
	assert.Equal(t, false, e.Details["done"], "прогон не завершён — курсор ещё есть")
}

// TestReplayPeriod_Gates — операция отклоняется до чтения журнала.
func TestReplayPeriod_Gates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*domain.Node)
		mutIn   func(*ReplayPeriodInput)
		wantErr error
	}{
		{
			name:    "синхронный узел",
			mutate:  func(n *domain.Node) { n.RootMethod = domain.RootMethodRequest },
			wantErr: ErrReplayPeriodSyncNode,
		},
		{
			name:    "логирование выключено",
			mutate:  func(n *domain.Node) { n.LoggingEnabled = false },
			wantErr: domain.ErrNodeLogsNotConfigured,
		},
		{
			name:    "тело запроса не логируется",
			mutate:  func(n *domain.Node) { n.LogRequestBody = false },
			wantErr: ErrReplayPeriodBodyNotLogged,
		},
		{
			name:    "узел отключён",
			mutate:  func(n *domain.Node) { n.Status = domain.NodeStatusDisabled },
			wantErr: domain.ErrNodeDisabled,
		},
		{
			name:    "нет нижней границы окна",
			mutate:  func(*domain.Node) {},
			mutIn:   func(in *ReplayPeriodInput) { in.Filter.SinceMs = 0 },
			wantErr: ErrReplayPeriodNoWindow,
		},
		{
			name:    "нет верхней границы окна",
			mutate:  func(*domain.Node) {},
			mutIn:   func(in *ReplayPeriodInput) { in.Filter.UntilMs = 0 },
			wantErr: ErrReplayPeriodNoWindow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			node := periodNode()
			tt.mutate(node)
			in := periodInput()
			if tt.mutIn != nil {
				tt.mutIn(&in)
			}
			reader := &periodReader{}
			uc, _ := newPeriodUC(t, reader, node, &refusingDispatcher{t: t})

			_, err := uc.ReplayPeriod(context.Background(), SystemActor(), in)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, reader.calls, "гейт обязан сработать ДО чтения журнала")
		})
	}
}

// refusingDispatcher роняет тест при любой попытке отправки: им проверяются
// операции, которые обязаны быть read-only.
type refusingDispatcher struct{ t *testing.T }

func (d *refusingDispatcher) Dispatch(_ context.Context, _ port.DispatchRequest) (*port.DispatchResponse, error) {
	d.t.Fatal("диспетчер не должен вызываться")
	return nil, nil
}

// TestPlanPeriod_ReadOnly — предпросмотр ничего не отправляет и не пишет в аудит.
func TestPlanPeriod_ReadOnly(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	trunc := okCandidate("cut", at.Add(time.Second))
	trunc.BodyTruncationMarker = true

	reader := &periodReader{
		total: 42,
		pages: []replayCandPage{{items: []domain.ReplayCandidate{okCandidate("a", at), trunc}}},
	}
	uc, auditRepo := newPeriodUC(t, reader, periodNode(), &refusingDispatcher{t: t})

	plan, err := uc.PlanPeriod(context.Background(), periodInput())
	require.NoError(t, err)

	assert.EqualValues(t, 42, plan.Total)
	assert.Equal(t, 2, plan.Scanned)
	assert.Equal(t, 1, plan.Eligible)
	assert.Equal(t, 1, plan.SkippedBy[string(domain.ReplaySkipTruncated)])
	assert.True(t, plan.Exact, "страница пришла без курсора — окно разобрано целиком")
	assert.Len(t, plan.Eligibles, 1)
	assert.Len(t, plan.Ineligibles, 1)
	assert.Empty(t, auditRepo.entries, "предпросмотр в аудит не пишется")
	// Границы окна возвращаются клиенту, чтобы он слал их обратно в каждый батч.
	assert.Equal(t, plan.From.UnixMilli(), reader.gotQ.SinceMs)
	assert.Equal(t, plan.To.UnixMilli(), reader.gotQ.UntilMs)
}

// TestPlanPeriod_NotExactWhenCapped — упёршись в потолок разбора, план обязан
// честно сказать «числа неполные», а не выдать частичный счёт за полный.
func TestPlanPeriod_NotExactWhenCapped(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	// Полные страницы: потолок ЗАПИСЕЙ достигается раньше потолка проходов.
	full := make([]domain.ReplayCandidate, 0, replayPeriodBatchCap)
	for i := range replayPeriodBatchCap {
		full = append(full, okCandidate("id", at.Add(time.Duration(i)*time.Millisecond)))
	}
	pages := make([]replayCandPage, 0, replayPeriodPreviewPasses)
	for i := range replayPeriodPreviewPasses {
		pages = append(pages, replayCandPage{
			items: full,
			next:  port.ReplayCursor{AfterMs: at.UnixMilli() + int64(i) + 1, AfterID: "id"},
		})
	}
	reader := &periodReader{total: 1_000_000, pages: pages}
	uc, _ := newPeriodUC(t, reader, periodNode(), &refusingDispatcher{t: t})

	plan, err := uc.PlanPeriod(context.Background(), periodInput())
	require.NoError(t, err)
	assert.False(t, plan.Exact, "потолок разбора достигнут — числа неполные")
	assert.Equal(t, replayPeriodPreviewCap, plan.Scanned)
}

// TestPlanPeriod_StopsOnPassLimit — второй ограничитель предпросмотра.
//
// Страница может целиком уйти в отсев повторных прогонов (§85.5.1), и тогда
// Scanned не растёт вовсе. Без потолка ПРОХОДОВ такой предпросмотр превращался
// бы в полный обход журнала, оставаясь под лимитом записей.
func TestPlanPeriod_StopsOnPassLimit(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-30 * time.Minute)
	// Каждая страница пустая (всё отсеяно), но курсор двигается — окно «не
	// кончается».
	pages := make([]replayCandPage, 0, replayPeriodPreviewPasses*2)
	for i := range replayPeriodPreviewPasses * 2 {
		pages = append(pages, replayCandPage{
			next: port.ReplayCursor{AfterMs: at.UnixMilli() + int64(i) + 1, AfterID: "id"},
		})
	}
	reader := &periodReader{total: 1_000_000, pages: pages}
	uc, _ := newPeriodUC(t, reader, periodNode(), &refusingDispatcher{t: t})

	plan, err := uc.PlanPeriod(context.Background(), periodInput())
	require.NoError(t, err)
	assert.False(t, plan.Exact)
	assert.Equal(t, 0, plan.Scanned)
	assert.Len(t, reader.calls, replayPeriodPreviewPasses,
		"обход обязан остановиться по числу проходов, а не крутиться до конца журнала")
}

// TestReplayPeriod_ExternalTableKeepsPartitionNarrowingOff — на внешней таблице
// §64 сужение по date_create запрещено: инвариант write-path там не
// гарантирован, и сужение молча теряло бы записи.
func TestReplayPeriod_ExternalTableKeepsPartitionNarrowingOff(t *testing.T) {
	t.Parallel()

	node := periodNode()
	node.ExternalTable = true
	reader := &periodReader{}
	uc, _ := newPeriodUC(t, reader, node, &stubDispatcher{})

	_, err := uc.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)
	assert.False(t, reader.gotQ.DateCreateAligned)

	node.ExternalTable = false
	reader2 := &periodReader{}
	uc2, _ := newPeriodUC(t, reader2, node, &stubDispatcher{})
	_, err = uc2.ReplayPeriod(context.Background(), SystemActor(), periodInput())
	require.NoError(t, err)
	assert.True(t, reader2.gotQ.DateCreateAligned)
}

// cancelAfterFirstDispatcher — отменяет контекст прогона после первой успешной
// реинжекции: имитирует уход клиента посреди батча.
type cancelAfterFirstDispatcher struct {
	cancel context.CancelFunc
	calls  int
}

func (d *cancelAfterFirstDispatcher) Dispatch(_ context.Context, _ port.DispatchRequest) (*port.DispatchResponse, error) {
	d.calls++
	if d.calls == 1 {
		d.cancel()
	}
	return &port.DispatchResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)}, nil
}

// TestReplayPeriod_AuditSurvivesClientAbort — НАХОДКА РЕВИЗИИ.
//
// Прерванный батч обязан оставить след: часть записей уже ушла получателю, и
// это единственное свидетельство того, что именно он получил. Ранний return по
// ctx.Err() выходил ДО записи аудита, а сама запись шла с уже отменённым
// контекстом — след терялся вместе с ушедшим клиентом.
func TestReplayPeriod_AuditSurvivesClientAbort(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := time.Now().Add(-30 * time.Minute)
	reader := &periodReader{pages: []replayCandPage{{
		items: []domain.ReplayCandidate{
			okCandidate("a", at),
			okCandidate("b", at.Add(time.Second)),
			okCandidate("c", at.Add(2*time.Second)),
		},
		next: port.ReplayCursor{AfterMs: at.UnixMilli(), AfterID: "c"},
	}}}
	disp := &cancelAfterFirstDispatcher{cancel: cancel}
	uc, auditRepo := newPeriodUC(t, reader, periodNode(), disp)

	res, err := uc.ReplayPeriod(ctx, SystemActor(), periodInput())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, res.Replayed, "успела уйти ровно одна запись")
	assert.Nil(t, res.NextCursor, "курсор после обрыва не отдаём: остаток страницы пропущен не был бы")

	require.Len(t, auditRepo.entries, 1, "прерванный батч обязан оставить след в аудите")
	e := auditRepo.entries[0]
	assert.Equal(t, domain.ActionNodeReplayPeriod, e.Action)
	assert.Equal(t, 1, e.Details["replayed"])
	assert.Equal(t, true, e.Details["aborted"])
	assert.Equal(t, false, e.Details["done"])
}
