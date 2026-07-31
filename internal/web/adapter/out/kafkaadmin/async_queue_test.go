package kafkaadmin

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkMsg(t *testing.T, key string, env queueEnvelope, partition int, offset int64) kafka.Message {
	t.Helper()
	v, err := json.Marshal(env)
	require.NoError(t, err)
	return kafka.Message{Key: []byte(key), Value: v, Partition: partition, Offset: offset}
}

func TestDecodeQueueMeta(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	env := queueEnvelope{
		ID: "id-1", NodePath: "partner/echo", Method: "POST",
		TargetURL: "https://api.example.com/x", Body: []byte(`{"k":"v"}`), ReceivedAt: now,
	}

	t.Run("matching key", func(t *testing.T) {
		t.Parallel()
		meta, ok := decodeQueueMeta(mkMsg(t, "partner/echo", env, 2, 42), "partner/echo", "nexus.async")
		require.True(t, ok)
		assert.Equal(t, "id-1", meta.ID)
		assert.Equal(t, 2, meta.Partition)
		assert.Equal(t, int64(42), meta.Offset)
		assert.Equal(t, "nexus.async", meta.Topic, "координата тела осмысленна только вместе с топиком")
		assert.Equal(t, "POST", meta.Method)
		assert.Equal(t, "https://api.example.com/x", meta.TargetURL)
		assert.Equal(t, len(`{"k":"v"}`), meta.BodySize)
		assert.WithinDuration(t, now, meta.ReceivedAt, time.Second)
	})

	t.Run("non-matching key skipped", func(t *testing.T) {
		t.Parallel()
		_, ok := decodeQueueMeta(mkMsg(t, "other/node", env, 0, 1), "partner/echo", "nexus.async")
		assert.False(t, ok, "сообщение другого узла не подходит")
	})

	t.Run("broken json skipped", func(t *testing.T) {
		t.Parallel()
		msg := kafka.Message{Key: []byte("partner/echo"), Value: []byte("not-json")}
		_, ok := decodeQueueMeta(msg, "partner/echo", "nexus.async")
		assert.False(t, ok, "битый JSON пропускается, не валит скан")
	})
}

func TestInPeriod(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	from := base.Add(-time.Hour)
	to := base.Add(time.Hour)

	tests := []struct {
		name     string
		t        time.Time
		from, to time.Time
		want     bool
	}{
		{"inside", base, from, to, true},
		{"before from", base.Add(-2 * time.Hour), from, to, false},
		{"after to", base.Add(2 * time.Hour), from, to, false},
		{"at from boundary", from, from, to, true},
		{"at to boundary", to, from, to, true},
		{"no bounds", base, time.Time{}, time.Time{}, true},
		{"only from, after", base, from, time.Time{}, true},
		{"only from, before", base.Add(-2 * time.Hour), from, time.Time{}, false},
		{"only to, before", base, time.Time{}, to, true},
		{"only to, after", base.Add(2 * time.Hour), time.Time{}, to, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, inPeriod(tc.t, tc.from, tc.to))
		})
	}
}

// §27.3/§35: queueEnvelope — намеренно МИНИМАЛЬНАЯ проекция конверта
// (internal/receiver/usecase/envelope.go), а не его копия: вкладке «Очередь»
// нужны только идентификатор, узел, адрес и время. Требование к ней одно —
// JSON-имена не должны разъезжаться с каноном, иначе peek молча покажет пустые
// поля. Полноту копии Sender'а держит tests/contract; здесь — подмножество.
func TestQueueEnvelopeTagsMatchCanon(t *testing.T) {
	t.Parallel()

	// Имена и типы из канона (receiver/usecase.Envelope). Импортировать сам
	// пакет нельзя: web-адаптер не должен зависеть от receiver.
	canon := map[string]string{
		"id":           "string",
		"node_path":    "string",
		"method":       "string",
		"target_url":   "string",
		"headers":      "map[string]string",
		"body":         "[]uint8",
		"received_at":  "time.Time",
		"auth_header":  "string",
		"client_ip":    "string",
		"request_path": "string",
		"rmq":          "*usecase.RMQMeta",
	}

	rt := reflect.TypeOf(queueEnvelope{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		require.NotEmpty(t, tag, "поле %s без json-тега", f.Name)

		want, ok := canon[tag]
		require.True(t, ok, "поле %q отсутствует в каноне конверта — проекция разъехалась", tag)
		assert.Equal(t, want, f.Type.String(), "тип поля %q разошёлся с каноном", tag)
	}
}
