package kafkaadmin

import (
	"context"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// hitRec — что отдал matchEnvelope вызывающей стороне.
type hitRec struct {
	id    string
	env   *queueEnvelope
	topic string
}

func collector() (*[]hitRec, func(string, *queueEnvelope, string)) {
	var got []hitRec
	return &got, func(id string, env *queueEnvelope, topic string) {
		got = append(got, hitRec{id: id, env: env, topic: topic})
	}
}

func TestMatchEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	env := queueEnvelope{
		ID: "id-1", NodePath: "partner/echo", Method: "POST",
		TargetURL: "https://api.example.com/x",
		Headers:   map[string]string{"Content-Type": "application/json"},
		Body:      []byte(`{"k":"v"}`), ReceivedAt: now,
	}
	c := &Client{logger: logging.NewNoop()}

	t.Run("тело читается, когда оно нужно", func(t *testing.T) {
		t.Parallel()
		got, hit := collector()
		want := map[string]struct{}{"id-1": {}}
		c.matchEnvelope(mkMsg(t, "partner/echo", env, 0, 7), "nexus.async.dlq", want, true, hit)

		require.Len(t, *got, 1)
		assert.Equal(t, "id-1", (*got)[0].id)
		assert.Equal(t, "nexus.async.dlq", (*got)[0].topic)
		require.NotNil(t, (*got)[0].env)
		assert.Equal(t, []byte(`{"k":"v"}`), (*got)[0].env.Body, "конверт несёт ПОЛНОЕ тело — в этом весь §96")
		assert.Empty(t, want, "найденный идентификатор выбывает из набора")
	})

	t.Run("проба не читает тело", func(t *testing.T) {
		t.Parallel()
		got, hit := collector()
		want := map[string]struct{}{"id-1": {}}
		c.matchEnvelope(mkMsg(t, "partner/echo", env, 0, 7), "nexus.async.dlq", want, false, hit)

		require.Len(t, *got, 1)
		assert.Nil(t, (*got)[0].env, "предпросмотру §85 тела не нужны и вредны — окно бывает в тысячи записей")
		assert.Empty(t, want)
	})

	t.Run("чужой идентификатор пропускается", func(t *testing.T) {
		t.Parallel()
		got, hit := collector()
		want := map[string]struct{}{"id-other": {}}
		c.matchEnvelope(mkMsg(t, "partner/echo", env, 0, 7), "nexus.async", want, true, hit)

		assert.Empty(t, *got)
		assert.Len(t, want, 1, "набор не тронут")
	})

	t.Run("битый конверт не роняет поиск", func(t *testing.T) {
		t.Parallel()
		got, hit := collector()
		want := map[string]struct{}{"id-1": {}}
		msg := kafka.Message{Key: []byte("partner/echo"), Value: []byte("{не json")}
		c.matchEnvelope(msg, "nexus.async", want, true, hit)

		assert.Empty(t, *got)
		assert.Len(t, want, 1)
	})
}

// TestLookupOriginals_NoWork фиксирует ранние выходы: без набора ID, без
// источников или без пути узла поиск не должен ходить в брокеры вовсе. Клиент
// здесь заведомо нерабочий (адресов нет) — любой сетевой вызов вернул бы
// ошибку и завалил бы тест по таймауту.
func TestLookupOriginals_NoWork(t *testing.T) {
	t.Parallel()
	c := &Client{logger: logging.NewNoop()}
	src := []port.QueueSource{{Topic: "nexus.async.dlq", Group: "nexus-dlq-reprocess", Deep: true}}

	cases := map[string]port.OriginalLookup{
		"без идентификаторов": {Sources: src, NodePath: "partner/echo"},
		"без источников":      {NodePath: "partner/echo", IDs: []string{"id-1"}},
		"без пути узла":       {Sources: src, IDs: []string{"id-1"}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, hit := collector()
			c.lookupOriginals(context.Background(), in, true, hit)
			assert.Empty(t, *got)
		})
	}
}

// TestFindOriginals_EmptyResult — контракт best-effort: промах поиска это пустая
// карта и nil-ошибка, а не сбой. Решение «отказать в повторе» принимает домен
// (§96.4), и оно одинаково для всех причин промаха.
func TestFindOriginals_EmptyResult(t *testing.T) {
	t.Parallel()
	c := &Client{logger: logging.NewNoop()}

	found, err := c.FindOriginals(context.Background(), port.OriginalLookup{NodePath: "partner/echo"})
	require.NoError(t, err)
	assert.Empty(t, found)

	probe, err := c.ProbeOriginals(context.Background(), port.OriginalLookup{NodePath: "partner/echo"})
	require.NoError(t, err)
	assert.Empty(t, probe)
}
