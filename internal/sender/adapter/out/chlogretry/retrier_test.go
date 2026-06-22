package chlogretry_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlogretry"
	"nexus/internal/sender/clogwire"
)

type captureProducer struct {
	mu       sync.Mutex
	msgs     [][]byte
	topics   []string
	keys     []string
	failNext bool
}

func (p *captureProducer) Produce(_ context.Context, topic, key string, value []byte, _ map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext {
		return errors.New("kafka down")
	}
	p.topics = append(p.topics, topic)
	p.keys = append(p.keys, key)
	p.msgs = append(p.msgs, value)
	return nil
}

func TestRetrier_ProducesEnvelope(t *testing.T) {
	t.Parallel()
	p := &captureProducer{}
	r := chlogretry.New(p, "nexus.logs.retry", 0, logging.NewNoop())

	batch := []*domain.LogRecord{{ID: "1", Status: 500}, {ID: "2", Status: 200}}
	require.NoError(t, r.Retry(context.Background(), "nexus_default.t", batch))

	require.Len(t, p.msgs, 1)
	assert.Equal(t, "nexus.logs.retry", p.topics[0])
	assert.Equal(t, "nexus_default.t", p.keys[0], "ключ = таблица (порядок в партишне)")

	env, err := clogwire.Unmarshal(p.msgs[0])
	require.NoError(t, err)
	assert.Equal(t, "nexus_default.t", env.Table)
	assert.Len(t, env.Logs, 2)
}

func TestRetrier_ChunksLargeBatch(t *testing.T) {
	t.Parallel()
	p := &captureProducer{}
	r := chlogretry.New(p, "nexus.logs.retry", 800, logging.NewNoop())

	body := strings.Repeat("x", 300)
	batch := make([]*domain.LogRecord, 6)
	for i := range batch {
		batch[i] = &domain.LogRecord{ID: "id", Request: body}
	}
	require.NoError(t, r.Retry(context.Background(), "t", batch))
	assert.Greater(t, len(p.msgs), 1, "большой батч режется на несколько сообщений")
}

func TestRetrier_ProduceError(t *testing.T) {
	t.Parallel()
	p := &captureProducer{failNext: true}
	r := chlogretry.New(p, "nexus.logs.retry", 0, logging.NewNoop())
	err := r.Retry(context.Background(), "t", []*domain.LogRecord{{ID: "1"}})
	require.Error(t, err)
}

func TestRetrier_EmptyBatch(t *testing.T) {
	t.Parallel()
	p := &captureProducer{}
	r := chlogretry.New(p, "nexus.logs.retry", 0, logging.NewNoop())
	require.NoError(t, r.Retry(context.Background(), "t", nil))
	assert.Empty(t, p.msgs)
}
