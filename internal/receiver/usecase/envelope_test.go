package usecase

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// stubNode возвращает узел c заданным списком forward-заголовков —
// этого достаточно для BuildEnvelope (поля шифрования/auth не нужны).
func stubNodeForEnvelope(path string, forward ...string) *domain.Node {
	return &domain.Node{
		Path:           path,
		ForwardHeaders: forward,
	}
}

func TestBuildEnvelope_HappyPath(t *testing.T) {
	t.Parallel()

	node := stubNodeForEnvelope("partner/echo", "X-Trace-Id")
	hdr := http.Header{}
	hdr.Set("X-Trace-Id", "abc-123")
	hdr.Set("Content-Type", "application/json")
	hdr.Set("X-Unrelated", "drop-me") // не в ForwardHeaders → не должен попасть

	before := time.Now().UTC().Add(-time.Second)

	env := BuildEnvelope(
		"id-42",
		node,
		http.MethodPost,
		"https://api.example.com/hook",
		"Bearer xyz",
		"10.0.0.1",
		"v1/GetParcelsInfo",
		hdr,
		url.Values{"limit": []string{"10"}},
		[]byte(`{"a":1}`),
	)

	require.NotNil(t, env)
	assert.Equal(t, "id-42", env.ID)
	assert.Equal(t, "partner/echo", env.NodePath)
	assert.Equal(t, http.MethodPost, env.Method)
	assert.Equal(t, "https://api.example.com/hook?limit=10", env.TargetURL)
	assert.Equal(t, "Bearer xyz", env.AuthHeader)
	assert.Equal(t, "10.0.0.1", env.ClientIP)
	assert.Equal(t, "v1/GetParcelsInfo", env.RequestPath)
	assert.Equal(t, []byte(`{"a":1}`), env.Body)

	// ForwardHeaders + Content-Type — только.
	assert.Equal(t, "abc-123", env.Headers["X-Trace-Id"])
	assert.Equal(t, "application/json", env.Headers["Content-Type"])
	_, ok := env.Headers["X-Unrelated"]
	assert.False(t, ok, "не из ForwardHeaders → не должен оказаться в envelope.Headers")

	assert.True(t, env.ReceivedAt.After(before), "ReceivedAt должно быть свежим")
	assert.Equal(t, "UTC", env.ReceivedAt.Location().String(), "ReceivedAt всегда в UTC")
}

func TestBuildEnvelope_EmptyForwardHeaders_OnlyContentType(t *testing.T) {
	t.Parallel()

	node := stubNodeForEnvelope("svc/empty") // forward = nil
	hdr := http.Header{}
	hdr.Set("Content-Type", "text/plain")
	hdr.Set("Authorization", "Bearer should-not-leak")
	hdr.Set("Cookie", "session=should-not-leak")

	env := BuildEnvelope(
		"id-1",
		node,
		http.MethodGet,
		"https://api.example.com/x",
		"",
		"",
		"",
		hdr,
		nil,
		nil,
	)

	require.NotNil(t, env)
	// Content-Type подхватывается всегда, даже если ForwardHeaders пуст.
	assert.Equal(t, "text/plain", env.Headers["Content-Type"])
	assert.Len(t, env.Headers, 1, "только Content-Type, ничего лишнего из исходного header'а")
	// AuthHeader не дублируется в Headers — он отдельным полем.
	_, hasAuth := env.Headers["Authorization"]
	assert.False(t, hasAuth)
}

func TestBuildEnvelope_NoContentType_NoQuery(t *testing.T) {
	t.Parallel()

	node := stubNodeForEnvelope("svc/raw", "X-Custom")
	hdr := http.Header{}
	hdr.Set("X-Custom", "v")

	env := BuildEnvelope(
		"id-2",
		node,
		http.MethodPut,
		"https://api.example.com/r",
		"",
		"127.0.0.1",
		"",
		hdr,
		nil,
		nil,
	)

	require.NotNil(t, env)
	// Когда Content-Type не выставлен — его и в map'е нет.
	_, ok := env.Headers["Content-Type"]
	assert.False(t, ok)
	assert.Equal(t, "v", env.Headers["X-Custom"])
	// URL не получает "?" если query пуста.
	assert.Equal(t, "https://api.example.com/r", env.TargetURL)
}

func TestBuildEnvelope_QueryMergedWithExistingURL(t *testing.T) {
	t.Parallel()

	node := stubNodeForEnvelope("svc/m")
	env := BuildEnvelope(
		"id-3",
		node,
		http.MethodGet,
		"https://api.example.com/x?already=1",
		"",
		"",
		"",
		http.Header{},
		url.Values{"extra": []string{"z"}},
		nil,
	)

	require.NotNil(t, env)
	// Порядок не гарантирован — парсим обратно для устойчивой проверки.
	u, err := url.Parse(env.TargetURL)
	require.NoError(t, err)
	got := u.Query()
	assert.Equal(t, "1", got.Get("already"))
	assert.Equal(t, "z", got.Get("extra"))
}

// ForwardHeaders — case-insensitive, BuildEnvelope нормализует имя к CanonicalHeaderKey.
func TestBuildEnvelope_ForwardHeaders_CaseInsensitive(t *testing.T) {
	t.Parallel()

	node := stubNodeForEnvelope("svc/case", "x-trace-id")
	hdr := http.Header{}
	hdr.Set("X-Trace-Id", "trace-1")

	env := BuildEnvelope(
		"id-4",
		node,
		http.MethodPost,
		"https://api.example.com/x",
		"",
		"",
		"",
		hdr,
		nil,
		nil,
	)

	require.NotNil(t, env)
	// http.CanonicalHeaderKey("x-trace-id") == "X-Trace-Id".
	assert.Equal(t, "trace-1", env.Headers["X-Trace-Id"])
}
