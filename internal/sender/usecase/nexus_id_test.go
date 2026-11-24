package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// TestSend_NexusIDHeader_StableAcrossAttempts (§118.7): каждый исходящий запрос
// несёт X-Nexus-Id = ID записи лога, ОДИНАКОВЫЙ на всех попытках — по нему
// получатель дедуплицирует повторы. До §118 получатель не видел ни одного
// идентификатора шины.
func TestSend_NexusIDHeader_StableAcrossAttempts(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{
		{StatusCode: 503}, {StatusCode: 502}, {StatusCode: 200},
	}}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(httpc, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.RetryCount = 3
	in.RetryBackoffMs = 1

	out := uc.Send(context.Background(), in)
	require.Equal(t, int32(200), out.StatusCode)
	require.Len(t, httpc.reqHeaders, 3)
	for i, h := range httpc.reqHeaders {
		assert.Equal(t, in.ID, h[domain.HeaderNexusID], "попытка %d", i+1)
	}
	require.Len(t, logw.written, 1)
	assert.Equal(t, logw.written[0].rec.ID, httpc.reqHeaders[0][domain.HeaderNexusID],
		"значение заголовка = ID записи лога")
}

// TestSend_NexusIDHeader_OverridesClientHeader: одноимённый заголовок клиента
// (узел пробрасывает его через forward_headers) в ЛЮБОМ регистре не уходит
// получателю — иначе клиент HTTP, ставящий заголовки обходом карты, через раз
// отправлял бы значение клиента. Карта вызывающего не меняется.
func TestSend_NexusIDHeader_OverridesClientHeader(t *testing.T) {
	t.Parallel()

	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)
	in := baseInput()
	in.Headers = map[string]string{"x-nexus-id": "forged", "Content-Type": "application/json"}

	uc.Send(context.Background(), in)
	require.Len(t, httpc.reqHeaders, 1)
	sent := httpc.reqHeaders[0]
	assert.Equal(t, in.ID, sent[domain.HeaderNexusID])
	assert.NotContains(t, sent, "x-nexus-id")
	assert.Equal(t, "application/json", sent["Content-Type"], "прочие заголовки пробрасываются")
	assert.Equal(t, map[string]string{"x-nexus-id": "forged", "Content-Type": "application/json"}, in.Headers,
		"входная карта не мутируется")
}

// TestSend_NexusIDHeader_InvalidIDOmitted: ID с управляющим символом (AMQP
// message_id издателя RabbitMQAsync) в заголовок не ставится — net/http отверг
// бы такой запрос на каждой попытке, и сообщение ушло бы в DLQ вместо доставки.
// Заголовок клиента при этом всё равно не проходит: X-Nexus-Id — только от шины.
func TestSend_NexusIDHeader_InvalidIDOmitted(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"msg-1\r\nX-Evil: 1", "msg\x00id", "del\x7fid"} {
		httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
		uc := NewSendUsecase(httpc, &stubLogWriter{}, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)
		in := baseInput()
		in.ID = id
		in.Headers = map[string]string{"X-Nexus-Id": "forged", "Content-Type": "application/json"}

		out := uc.Send(context.Background(), in)
		require.Equal(t, int32(200), out.StatusCode, "id %q: доставка идёт", id)
		require.Len(t, httpc.reqHeaders, 1)
		sent := httpc.reqHeaders[0]
		assert.NotContains(t, sent, domain.HeaderNexusID, "id %q", id)
		assert.Equal(t, "application/json", sent["Content-Type"])
	}
}
