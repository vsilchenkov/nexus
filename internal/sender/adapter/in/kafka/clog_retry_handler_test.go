package kafka_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	kafkaadapter "nexus/internal/sender/adapter/in/kafka"
	"nexus/internal/sender/clogwire"
	"nexus/internal/sender/usecase"
)

type stubInserter struct {
	err   error
	calls int
	rows  int
}

func (s *stubInserter) InsertBatch(_ context.Context, _ string, batch []*domain.LogRecord) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.rows += len(batch)
	return nil
}

func msg(t *testing.T) []byte {
	t.Helper()
	b, err := clogwire.Marshal("nexus_default.t", []*domain.LogRecord{{ID: "1"}, {ID: "2"}})
	require.NoError(t, err)
	return b
}

func TestChLogRetryHandler_Success_Acks(t *testing.T) {
	t.Parallel()
	ins := &stubInserter{}
	h := kafkaadapter.NewChLogRetryHandler(ins, nil, logging.NewNoop())
	res := h.Handle(context.Background(), msg(t), nil)
	assert.Equal(t, usecase.HandleAck, res)
	assert.Equal(t, 2, ins.rows)
}

// CH всё ещё лежит → InsertBatch падает → HandleRetry (offset НЕ коммитится,
// Kafka передоставит).
func TestChLogRetryHandler_InsertFails_Retries(t *testing.T) {
	t.Parallel()
	ins := &stubInserter{err: errors.New("ch down")}
	h := kafkaadapter.NewChLogRetryHandler(ins, nil, logging.NewNoop())
	res := h.Handle(context.Background(), msg(t), nil)
	assert.Equal(t, usecase.HandleRetry, res)
}

// Битое сообщение → HandleAck (drop), без зацикливания на яде.
func TestChLogRetryHandler_BadMessage_Acks(t *testing.T) {
	t.Parallel()
	ins := &stubInserter{}
	h := kafkaadapter.NewChLogRetryHandler(ins, nil, logging.NewNoop())
	res := h.Handle(context.Background(), []byte("{broken"), nil)
	assert.Equal(t, usecase.HandleAck, res)
	assert.Equal(t, 0, ins.calls, "битое сообщение не должно доходить до INSERT")
}

func TestChLogRetryHandler_Empty_Acks(t *testing.T) {
	t.Parallel()
	ins := &stubInserter{}
	h := kafkaadapter.NewChLogRetryHandler(ins, nil, logging.NewNoop())
	b, _ := clogwire.Marshal("", nil)
	res := h.Handle(context.Background(), b, nil)
	assert.Equal(t, usecase.HandleAck, res)
	assert.Equal(t, 0, ins.calls)
}
