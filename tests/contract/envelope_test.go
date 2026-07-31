// Package contract — тесты контрактов между сервисами.
//
// Конверт async-сообщения (nexus.async) определён трижды: у Receiver — канон
// (он его пишет), у Sender — полная копия (он его читает), у Web — минимальная
// проекция для вкладки «Очередь». Дублирование намеренное: иначе Sender и Web
// импортировали бы receiver/usecase, а это нарушение Clean Architecture.
//
// Цена дублирования — молчаливый рассинхрон: у Sender-копии однажды не оказалось
// блока `rmq`, и происхождение RabbitMQAsync-сообщений терялось при доставке,
// потому что encoding/json неизвестные поля просто игнорирует. Ни один тест
// этого не видел. Здесь копии сверяются по JSON-тегам.
package contract

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	receiverusecase "nexus/internal/receiver/usecase"
	senderusecase "nexus/internal/sender/usecase"
)

// jsonFields — множество JSON-имён полей структуры (без опций вроде omitempty).
// Поля с тегом "-" пропускаются: они в сообщение не попадают.
func jsonFields(t *testing.T, v any) map[string]string {
	t.Helper()

	rt := reflect.TypeOf(v)
	require.Equal(t, reflect.Struct, rt.Kind(), "ожидалась структура, получено %s", rt.Kind())

	out := make(map[string]string, rt.NumField())
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := tag
		if idx := indexComma(tag); idx >= 0 {
			name = tag[:idx]
		}
		if name == "" {
			name = f.Name // без тега json использует имя поля как есть
		}
		out[name] = f.Type.String()
	}
	return out
}

func indexComma(s string) int {
	for i := range len(s) {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}

// TestAsyncEnvelopeCopiesMatch — Sender обязан знать ВСЕ поля конверта: он
// единственный, кто его исполняет, и молча потерянное поле = потерянная функция.
func TestAsyncEnvelopeCopiesMatch(t *testing.T) {
	t.Parallel()

	canon := jsonFields(t, receiverusecase.Envelope{})
	copyOf := jsonFields(t, senderusecase.Envelope{})

	assert.Equal(t, canon, copyOf,
		"копии Envelope разошлись: обнови обе (receiver/usecase/envelope.go и sender/usecase/async_envelope.go)")
}

// TestRMQMetaCopiesMatch — вложенный блок происхождения сверяется отдельно:
// одинаковый набор полей верхнего уровня ещё не гарантирует одинаковый `rmq`.
func TestRMQMetaCopiesMatch(t *testing.T) {
	t.Parallel()

	canon := jsonFields(t, receiverusecase.RMQMeta{})
	copyOf := jsonFields(t, senderusecase.RMQMeta{})

	assert.Equal(t, canon, copyOf, "копии RMQMeta разошлись")
}

// TestAsyncEnvelopeRoundTrip — сериализация Receiver'а читается Sender'ом
// без потерь: тегов мало, но проверка держит и типы (время, []byte, map).
func TestAsyncEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	src := receiverusecase.Envelope{
		ID:          "01J0",
		NodePath:    "partner/orders",
		Method:      "POST",
		TargetURL:   "https://example.test/api?x=1",
		AuthHeader:  "Bearer t",
		Headers:     map[string]string{"X-Trace": "abc"},
		Body:        []byte(`{"a":1}`),
		ClientIP:    "10.0.0.1",
		ReceivedAt:  ts,
		RequestPath: "sub/path",
		RMQ: &receiverusecase.RMQMeta{
			Exchange:    "orders",
			RoutingKey:  "orders.new",
			DeliveryTag: 42,
			MessageID:   "msg-1",
			Timestamp:   ts,
		},
	}

	var dst senderusecase.Envelope
	requireJSONRoundTrip(t, src, &dst)

	assert.Equal(t, src.ID, dst.ID)
	assert.Equal(t, src.TargetURL, dst.TargetURL)
	assert.Equal(t, src.Headers, dst.Headers)
	assert.Equal(t, src.Body, dst.Body)
	assert.Equal(t, src.RequestPath, dst.RequestPath)
	assert.True(t, src.ReceivedAt.Equal(dst.ReceivedAt))

	require.NotNil(t, dst.RMQ, "блок rmq обязан доезжать до Sender: без него доставка "+
		"RabbitMQAsync-сообщения неотличима от обычного requestAsync")
	assert.Equal(t, src.RMQ.Exchange, dst.RMQ.Exchange)
	assert.Equal(t, src.RMQ.RoutingKey, dst.RMQ.RoutingKey)
	assert.Equal(t, src.RMQ.DeliveryTag, dst.RMQ.DeliveryTag)
	assert.Equal(t, src.RMQ.MessageID, dst.RMQ.MessageID)
	assert.True(t, src.RMQ.Timestamp.Equal(dst.RMQ.Timestamp))
}

// requireJSONRoundTrip кодирует src и декодирует в dst — ровно тот путь, что
// проходит сообщение через Kafka.
func requireJSONRoundTrip(t *testing.T, src, dst any) {
	t.Helper()
	raw, err := json.Marshal(src)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, dst))
}
