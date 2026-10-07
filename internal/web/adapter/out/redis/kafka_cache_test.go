package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKafkaCacheRedis_NilClientIsNoop (§113.8): без Redis кеш экрана Kafka —
// промах на чтении и no-op на записи, а не паника на nil-клиенте.
func TestKafkaCacheRedis_NilClientIsNoop(t *testing.T) {
	t.Parallel()
	c := NewKafkaCacheRedis(nil, 0)
	require.NoError(t, c.Set(context.Background(), "lag:g:t", int64(5)))
	var got int64
	found, err := c.Get(context.Background(), "lag:g:t", &got)
	require.NoError(t, err)
	assert.False(t, found)
}

// TestKafkaCacheRedis_RoundTrip: значение переживает JSON и живёт TTL.
func TestKafkaCacheRedis_RoundTrip(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	c := NewKafkaCacheRedis(client, 30*time.Second)

	require.NoError(t, c.Set(context.Background(), "lag:g:t", int64(960)))
	var got int64
	found, err := c.Get(context.Background(), "lag:g:t", &got)
	require.NoError(t, err)
	assert.True(t, found)
	assert.EqualValues(t, 960, got)

	mr.FastForward(31 * time.Second)
	found, err = c.Get(context.Background(), "lag:g:t", &got)
	require.NoError(t, err)
	assert.False(t, found, "после TTL — промах")
}
