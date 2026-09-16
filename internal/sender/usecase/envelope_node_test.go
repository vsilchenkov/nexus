package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase/port"
)

const (
	tgMessengersID = "f6693f87-5a1e-4c1b-9d3e-000000000001"
	tgWebhookID    = "d3aae718-2b4c-4e6d-8f10-000000000002"
)

// sameNameNodes — боевой случай §101.1: путь telegram занят узлами двух команд,
// у каждой свой адрес и своя CH-таблица.
func sameNameNodes() (messengers, webhook *domain.Node) {
	mk := func(id, team, target, table string) *domain.Node {
		return &domain.Node{
			ID:              id,
			Path:            "telegram",
			TeamSlug:        team,
			RootMethod:      domain.RootMethodRequestAsync,
			Status:          domain.NodeStatusEnabled,
			URLMode:         domain.URLModeStatic,
			TargetURL:       target,
			TimeoutMs:       1000,
			LoggingEnabled:  true,
			ClickHouseTable: table,
		}
	}
	return mk(tgMessengersID, "messengers", "https://messengers.example.com/bot", "messengers.log_telegram"),
		mk(tgWebhookID, "webhook", "https://webhook.example.com/hook", "webhook.log_telegram")
}

func marshalEnvelope(t *testing.T, env Envelope) []byte {
	t.Helper()
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	return raw
}

// TestResolveEnvelopeNode — какой узел исполняет конверт (§101.5).
func TestResolveEnvelopeNode(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	solo := &domain.Node{ID: "0c1d2e3f-4a5b-4c6d-8e7f-000000000003", Path: "solo"}
	all := []*domain.Node{messengers, webhook, solo}

	cases := []struct {
		name    string
		env     Envelope
		want    *domain.Node
		wantErr error
	}{
		{"node_id выбирает узел среди одноимённых", Envelope{NodeID: tgWebhookID, NodePath: "telegram"}, webhook, nil},
		{"node_id важнее пути: узел переименован после приёма", Envelope{NodeID: tgMessengersID, NodePath: "old-name"}, messengers, nil},
		{"узел с этим id удалён", Envelope{NodeID: "9a8b7c6d-5e4f-4a3b-8c2d-000000000009", NodePath: "telegram"}, nil, domain.ErrNodeNotFound},
		{"старый конверт, путь однозначен", Envelope{NodePath: "solo"}, solo, nil},
		{"старый конверт, узла нет", Envelope{NodePath: "nope"}, nil, domain.ErrNodeNotFound},
		{"старый конверт, путь в двух командах", Envelope{NodePath: "telegram"}, nil, ErrNodeAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveEnvelopeNode(context.Background(), &stubAsyncNodeReader{nodes: all}, logging.NewNoop(), tc.env)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tc.want, got)
		})
	}
}

// TestResolveEnvelopeNode_NonCanonicalIDSkipsDatabase — node_id не в виде UUID
// до БД не доходит: ридер здесь отвечает ошибкой, и любое обращение к нему
// провалило бы тест. Иначе ошибка синтаксиса uuid в PostgreSQL выглядела бы как
// сбой БД, и сообщение повторялось бы вечно.
func TestResolveEnvelopeNode_NonCanonicalIDSkipsDatabase(t *testing.T) {
	t.Parallel()
	reader := &stubAsyncNodeReader{err: assert.AnError}
	for _, id := range []string{
		"telegram",
		"d3aae7182b4c4e6d8f10000000000002", // без дефисов
		"urn:uuid:d3aae718-2b4c-4e6d-8f10-000000000002", // uuid.Parse принимает, PostgreSQL — нет
		"{d3aae718-2b4c-4e6d-8f10-000000000002}",
	} {
		_, err := resolveEnvelopeNode(context.Background(), reader, logging.NewNoop(), Envelope{NodeID: id})
		assert.ErrorIs(t, err, domain.ErrNodeNotFound, id)
	}
}

// TestResolveEnvelopeNode_ReadErrorPropagates — сбой БД не маскируется под
// «узла нет»: сообщение обязано повториться, а не пропасть.
func TestResolveEnvelopeNode_ReadErrorPropagates(t *testing.T) {
	t.Parallel()
	reader := &stubAsyncNodeReader{err: assert.AnError}
	for _, env := range []Envelope{{NodeID: tgWebhookID}, {NodePath: "telegram"}} {
		_, err := resolveEnvelopeNode(context.Background(), reader, logging.NewNoop(), env)
		assert.ErrorIs(t, err, assert.AnError)
	}
}

// TestAsync_SameNamePaths_DeliversToEnvelopeNode (§101.5) — до §101 Sender искал
// узел по пути и исполнял конверт узлом случайной из команд: с чужим адресом и
// в чужую CH-таблицу.
func TestAsync_SameNamePaths_DeliversToEnvelopeNode(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	for _, target := range []*domain.Node{messengers, webhook} {
		httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
		logw := &stubLogWriter{}
		send := NewSendUsecase(httpc, logw, nil, logging.NewNoop(), 64<<20)
		p := NewAsyncProcessor(&stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
			send, &stubDLQProducer{}, nil, nil, "nexus.async.dlq", nil, logging.NewNoop())

		raw := marshalEnvelope(t, Envelope{
			ID: "id-" + target.TeamSlug, NodeID: target.ID, Team: target.TeamSlug,
			NodePath: "telegram", Method: "POST", TargetURL: target.TargetURL,
		})
		require.Equal(t, HandleAck, p.Handle(context.Background(), raw, nil), target.TeamSlug)

		assert.Equal(t, target.TargetURL, httpc.lastReqURL, target.TeamSlug)
		require.Len(t, logw.written, 1, target.TeamSlug)
		assert.Equal(t, target.ClickHouseTable, logw.written[0].table, target.TeamSlug)
		assert.Equal(t, target.ID, logw.written[0].rec.NodeID, target.TeamSlug)
	}
}

// TestAsync_LegacyAmbiguousEnvelope_GoesToDLQ (§101.5) — старый конверт без
// node_id при пути, занятом двумя командами, никому не доставляется.
func TestAsync_LegacyAmbiguousEnvelope_GoesToDLQ(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	dlq := &stubDLQProducer{}
	p, httpc := newAsyncProcessorWithCancel(t,
		&stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
		&port.HTTPResponse{StatusCode: 200}, dlq, nil)

	raw := marshalEnvelope(t, Envelope{ID: "id-legacy", NodePath: "telegram", Method: "POST",
		TargetURL: "https://messengers.example.com/bot"})
	assert.Equal(t, HandleDLQed, p.Handle(context.Background(), raw, nil))

	assert.Zero(t, httpc.calls, "доставлять некому — чей конверт, неизвестно")
	require.Len(t, dlq.produced, 1)
	msg := dlq.produced[0]
	assert.Equal(t, "nexus.async.dlq", msg.topic)
	assert.Equal(t, "telegram", msg.key, "ключ партиции — путь, как у всех сообщений узла")
	assert.Equal(t, ReasonNodeAmbiguous, msg.headers["reason"])
	assert.Equal(t, raw, msg.value, "конверт в DLQ — байт в байт")
}

// TestAsync_LegacyAmbiguousEnvelope_Cancelled (§101.5, §34.4) — отменённый
// оператором неоднозначный конверт подтверждается, как любой отменённый. Очередь
// paused-узла могли очистить до обновления, когда отмена шла по пути: в DLQ
// такое сообщение дошло бы до сброса и подняло алерт потерь. Сбой проверки
// отмены — fail-open, конверт идёт в DLQ.
func TestAsync_LegacyAmbiguousEnvelope_Cancelled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		cancel     *stubCancelSet
		want       HandleResult
		wantDLQ    int
		wantMetric string
	}{
		{"отменён — ack без DLQ", &stubCancelSet{cancelled: map[string]bool{"id-legacy": true}}, HandleAck, 0, "cancelled"},
		{"не отменён — DLQ", &stubCancelSet{}, HandleDLQed, 1, ReasonNodeAmbiguous},
		{"сбой проверки отмены — DLQ", &stubCancelSet{err: assert.AnError}, HandleDLQed, 1, ReasonNodeAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			messengers, webhook := sameNameNodes()
			dlq := &stubDLQProducer{}
			m := metrics.New("sender")
			httpc := &stubHTTPCaller{}
			send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
			p := NewAsyncProcessor(&stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
				send, dlq, tc.cancel, nil, "nexus.async.dlq", m, logging.NewNoop())

			raw := marshalEnvelope(t, Envelope{ID: "id-legacy", NodePath: "telegram", Method: "POST"})
			assert.Equal(t, tc.want, p.Handle(context.Background(), raw, nil))

			assert.Zero(t, httpc.calls)
			assert.Len(t, dlq.produced, tc.wantDLQ)
			assert.InDelta(t, 1, testutil.ToFloat64(
				m.RequestsTotal.WithLabelValues("requestAsync", "", "telegram", tc.wantMetric)), 0)
		})
	}
}

// TestAsync_NonCanonicalNodeID_Dropped — битый node_id: сообщение отпускается
// как для удалённого узла, без доставки и без DLQ.
func TestAsync_NonCanonicalNodeID_Dropped(t *testing.T) {
	t.Parallel()
	messengers, _ := sameNameNodes()
	dlq := &stubDLQProducer{}
	p, httpc := newAsyncProcessorWithCancel(t, &stubAsyncNodeReader{nodes: []*domain.Node{messengers}},
		&port.HTTPResponse{StatusCode: 200}, dlq, nil)

	raw := marshalEnvelope(t, Envelope{ID: "id-bad", NodeID: "telegram", NodePath: "telegram", Method: "POST"})
	assert.Equal(t, HandleAck, p.Handle(context.Background(), raw, nil))
	assert.Zero(t, httpc.calls)
	assert.Empty(t, dlq.produced)
}

// TestAsync_DLQHeadersCarryNodeIdentity (§101.4) — копия в DLQ несёт узел
// конверта в заголовках, как и исходное сообщение.
func TestAsync_DLQHeadersCarryNodeIdentity(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	dlq := &stubDLQProducer{}
	p := newAsyncProcessorForTest(t, &stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
		&port.HTTPResponse{StatusCode: 500, Body: []byte("boom")}, nil, dlq)

	raw := marshalEnvelope(t, Envelope{ID: "id-500", NodeID: tgWebhookID, Team: "webhook",
		NodePath: "telegram", Method: "POST", TargetURL: webhook.TargetURL})
	require.Equal(t, HandleDLQed, p.Handle(context.Background(), raw, nil))

	require.Len(t, dlq.produced, 1)
	assert.Equal(t, tgWebhookID, dlq.produced[0].headers["node_id"])
	assert.Equal(t, "webhook", dlq.produced[0].headers["team"])
}

// TestReprocess_LegacyAmbiguousEnvelope_Dropped (§101.5) — репроцессор сбрасывает
// неоднозначный конверт и учитывает это в метрике потерь, а не возвращает его в
// хвост DLQ до истечения TTL.
func TestReprocess_LegacyAmbiguousEnvelope_Dropped(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	httpc := &stubHTTPCaller{}
	dlq := &stubDLQProducer{}
	m := metrics.New("sender")
	send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
	proc := NewDLQReprocessor(&stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
		send, dlq, &stubLogWriter{}, nil, nil, "nexus.async.dlq", m, logging.NewNoop())

	raw := makeDLQEnvelope(t, "telegram", time.Now().UTC())
	got := proc.ProcessMessage(context.Background(), raw, map[string]string{"reason": ReasonNodeAmbiguous})

	assert.Equal(t, ReprocessCommit, got)
	assert.Zero(t, httpc.calls)
	assert.Empty(t, dlq.produced, "в хвост DLQ не возвращаем — крутился бы до TTL")
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.DLQReprocessTotal.WithLabelValues("", "telegram", reprocessDropped)), 0)
}

// TestAsync_SameNamePaths_MetricsSplitByTeam (§101.3) — ряды одноимённых узлов
// разных команд не складываются. Слог берётся из узла, а не из конверта: конверт
// мог быть принят до переноса узла в другую команду.
func TestAsync_SameNamePaths_MetricsSplitByTeam(t *testing.T) {
	t.Parallel()
	messengers, webhook := sameNameNodes()
	m := metrics.New("sender")
	for _, target := range []*domain.Node{messengers, messengers, webhook} {
		httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
		send := NewSendUsecase(httpc, &stubLogWriter{}, nil, logging.NewNoop(), 64<<20)
		p := NewAsyncProcessor(&stubAsyncNodeReader{nodes: []*domain.Node{messengers, webhook}},
			send, &stubDLQProducer{}, nil, nil, "nexus.async.dlq", m, logging.NewNoop())

		raw := marshalEnvelope(t, Envelope{ID: "id", NodeID: target.ID, Team: "stale",
			NodePath: "telegram", Method: "POST", TargetURL: target.TargetURL})
		require.Equal(t, HandleAck, p.Handle(context.Background(), raw, nil))
	}

	require.Equal(t, 2, testutil.CollectAndCount(m.RequestsTotal))
	assert.InDelta(t, 2, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("requestAsync", "messengers", "telegram", "200")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.RequestsTotal.WithLabelValues("requestAsync", "webhook", "telegram", "200")), 0)
}
