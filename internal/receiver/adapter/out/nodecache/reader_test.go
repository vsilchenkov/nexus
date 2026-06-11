package nodecache

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// timeoutErr — минимальная реализация net.Error для теста.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsRedisUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"client closed", goredis.ErrClosed, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"net.Error timeout", timeoutErr{}, true},
		{"wrapped net.OpError", fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")}), true},
		{"unmarshal-style error", errors.New("unmarshal node: unexpected end of JSON input"), false},
		{"generic error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isRedisUnavailable(tt.err))
		})
	}
}

// TestReader_WriteBackDedup_InflightFlag — пока write-back ключа «в полёте»,
// повторный LoadOrStore видит busy=true (контракт дедупа из Get).
func TestReader_WriteBackDedup_InflightFlag(t *testing.T) {
	t.Parallel()

	r := &Reader{}
	key := nodeKey("default", "partner/echo")

	_, busy := r.inflight.LoadOrStore(key, struct{}{})
	assert.False(t, busy, "первый write-back должен стартовать")

	_, busy = r.inflight.LoadOrStore(key, struct{}{})
	assert.True(t, busy, "повторный cache-miss того же узла не должен плодить горутину")

	r.inflight.Delete(key)
	_, busy = r.inflight.LoadOrStore(key, struct{}{})
	assert.False(t, busy, "после завершения write-back ключ снова свободен")
}
