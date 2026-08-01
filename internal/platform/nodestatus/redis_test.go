package nodestatus_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
)

func newWriter(t *testing.T) (*nodestatus.RedisWriter, *miniredis.Miniredis) {
	t.Helper()

	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return nodestatus.NewRedisWriter(client, logging.NewNoop()), srv
}

// Кодировка исходов — контракт между Sender (пишет) и Web (читает): §52.
// Ошибка здесь не падает тестом доставки, а тихо превращает статус узла в чужой.
func TestEncodeDecodeOutcomeRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  domain.NodeOutcome
		want string
	}{
		{name: "ok", out: domain.NodeOutcomeOK, want: "0"},
		{name: "down", out: domain.NodeOutcomeDown, want: "1"},
		{name: "degraded", out: domain.NodeOutcomeDegraded, want: "2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded := nodestatus.EncodeOutcome(tt.out)
			assert.Equal(t, tt.want, encoded)

			decoded, ok := nodestatus.DecodeOutcome(encoded)
			require.True(t, ok)
			assert.Equal(t, tt.out, decoded)
		})
	}
}

func TestDecodeOutcomeUnknownValue(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"", "3", "true", "down"} {
		_, ok := nodestatus.DecodeOutcome(v)
		assert.False(t, ok, "значение %q не должно распознаваться: читающая сторона обязана "+
			"уйти в fallback на Prometheus, а не показать чужой статус", v)
	}
}

// Неизвестный исход кодируется как ok — worst case здесь «показали зелёным»,
// и он самоисцеляется следующим вызовом узла.
func TestEncodeUnknownOutcomeFallsBackToOK(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0", nodestatus.EncodeOutcome(domain.NodeOutcome("whatever")))
}

func TestSetLastOutcomeWritesKeyWithTTL(t *testing.T) {
	t.Parallel()

	w, srv := newWriter(t)
	w.SetLastOutcome(context.Background(), "partner/orders", domain.NodeOutcomeDegraded)

	key := nodestatus.Key("partner/orders")
	require.Contains(t, srv.Keys(), key)

	got, err := srv.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "2", got)

	// TTL нужен, чтобы ключи удалённых узлов не жили вечно.
	assert.Equal(t, 30*24*time.Hour, srv.TTL(key))
}

func TestSetLastOutcomeIgnoresEmptyPath(t *testing.T) {
	t.Parallel()

	w, srv := newWriter(t)
	w.SetLastOutcome(context.Background(), "", domain.NodeOutcomeDown)
	assert.Empty(t, srv.Keys())
}

// Фиксация статуса — best-effort: недоступный Redis не должен влиять на
// доставку, поэтому ошибка только логируется (паники/возврата ошибки нет).
func TestSetLastOutcomeSurvivesRedisDown(t *testing.T) {
	t.Parallel()

	w, srv := newWriter(t)
	srv.Close()

	assert.NotPanics(t, func() {
		w.SetLastOutcome(context.Background(), "partner/orders", domain.NodeOutcomeDown)
	})
}
