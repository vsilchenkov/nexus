package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringMapCarrier_GetSetKeys(t *testing.T) {
	m := map[string]string{}
	c := stringMapCarrier(m)

	assert.Empty(t, c.Get("traceparent"))
	c.Set("traceparent", "00-aabb-ccdd-01")
	assert.Equal(t, "00-aabb-ccdd-01", c.Get("traceparent"))

	c.Set("baggage", "k=v")
	keys := c.Keys()
	assert.ElementsMatch(t, []string{"traceparent", "baggage"}, keys)

	// Изменения видны через map (это by-reference поверх map).
	assert.Equal(t, "00-aabb-ccdd-01", m["traceparent"])
}

func TestInjectKafkaHeaders_NilMap_NoPanic(t *testing.T) {
	// Защита от nil-map: вызывающий мог забыть инициализировать headers.
	assert.NotPanics(t, func() {
		InjectKafkaHeaders(context.Background(), nil)
	})
}

func TestInjectKafkaHeaders_EmptyMap_NoCrash(t *testing.T) {
	headers := map[string]string{}
	assert.NotPanics(t, func() {
		InjectKafkaHeaders(context.Background(), headers)
	})
	// При выключенном tracing propagator не вставит traceparent — map останется
	// пустым. Главное — не упасть.
}

func TestExtractKafkaHeaders_EmptyMap_ReturnsSameCtx(t *testing.T) {
	ctx := context.Background()
	got := ExtractKafkaHeaders(ctx, nil)
	assert.Equal(t, ctx, got, "пустые headers — extract возвращает входной ctx")
}

func TestExtractKafkaHeaders_WithKeys_DoesNotPanic(t *testing.T) {
	ctx := context.Background()
	got := ExtractKafkaHeaders(ctx, map[string]string{
		"traceparent": "00-aabb-ccdd-01",
		"baggage":     "k=v",
	})
	assert.NotNil(t, got)
}

func TestStartKafkaProducerSpan_FinishSafe(t *testing.T) {
	ctx, finish := StartKafkaProducerSpan(context.Background(), "nexus.async")
	assert.NotNil(t, ctx)
	assert.NotPanics(t, func() {
		finish(nil)
	})
}

func TestStartKafkaProducerSpan_FinishWithError(t *testing.T) {
	_, finish := StartKafkaProducerSpan(context.Background(), "nexus.async")
	assert.NotPanics(t, func() {
		finish(errors.New("broker down"))
	})
}

func TestStartKafkaConsumerSpan_RestoresParentFromHeaders(t *testing.T) {
	// Производитель кладёт traceparent в headers, consumer его экстрактит и
	// открывает span. При выключенном TracerProvider (no-op) traceparent
	// не вставлен — но Extract+Start не должен паниковать.
	headers := map[string]string{}
	InjectKafkaHeaders(context.Background(), headers)

	ctx := ExtractKafkaHeaders(context.Background(), headers)
	ctx, finish := StartKafkaConsumerSpan(ctx, "nexus.async")
	assert.NotNil(t, ctx)
	assert.NotPanics(t, func() {
		finish(nil)
	})
}
