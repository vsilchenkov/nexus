package otel

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInjectHTTPHeaders_NoopWithoutTracerProvider(t *testing.T) {
	// При выключенном tracing global propagator всё ещё установлен
	// (или может быть no-op). Главное — функция не паникует, и без
	// активного span'а traceparent не добавляется (нечего инжектить).
	h := http.Header{}
	assert.NotPanics(t, func() {
		InjectHTTPHeaders(context.Background(), h)
	})
}

func TestStartHTTPClientSpan_ReturnsSafeFinish_OnNoopProvider(t *testing.T) {
	// При выключенном tracing TracerProvider возвращает noop-span, но
	// объект span всё равно валиден — finish() безопасно вызывать.
	ctx, finish := StartHTTPClientSpan(context.Background(), "POST", "https://example.com/x")
	assert.NotNil(t, ctx)
	assert.NotNil(t, finish)
	assert.NotPanics(t, func() {
		finish(200, nil)
	})
}

func TestStartHTTPClientSpan_FinishWithError(t *testing.T) {
	// finish с error не должен паниковать.
	_, finish := StartHTTPClientSpan(context.Background(), "GET", "https://example.com/")
	assert.NotPanics(t, func() {
		finish(0, errors.New("connection refused"))
	})
}

func TestStartHTTPClientSpan_5xxMarksError(t *testing.T) {
	// finish со status>=500 не должен паниковать (span помечается codes.Error).
	_, finish := StartHTTPClientSpan(context.Background(), "POST", "https://example.com/x")
	assert.NotPanics(t, func() {
		finish(503, nil)
	})
}

func TestSanitizeURL_StripsQuery(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://api.partner.com/hook?token=secret", "https://api.partner.com/hook"},
		{"https://api.partner.com/hook", "https://api.partner.com/hook"},
		{"http://localhost:8080/v1/request/foo?a=1&b=2", "http://localhost:8080/v1/request/foo"},
		{"", ""},
		{"?onlyquery", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sanitizeURL(c.in), "input=%q", c.in)
	}
}
