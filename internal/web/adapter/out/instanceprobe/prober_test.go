package instanceprobe_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/adapter/out/instanceprobe"
)

// nexusStub — заглушка соседней ноды: отдаёт /api/version и /ready так, как это
// делает настоящий Web Service.
type nexusStub struct {
	versionStatus int
	versionBody   string
	readyStatus   int
	readyBody     string
	// versionDelay имитирует зависший инстанс (проверка таймаута).
	versionDelay time.Duration
}

func (s nexusStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		if s.versionDelay > 0 {
			time.Sleep(s.versionDelay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cmpOr(s.versionStatus, http.StatusOK))
		_, _ = w.Write([]byte(s.versionBody))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cmpOr(s.readyStatus, http.StatusOK))
		_, _ = w.Write([]byte(cmpOrString(s.readyBody, `{"ready":true,"checks":{"postgres":"ok"}}`)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func cmpOr(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func cmpOrString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func newProber() *instanceprobe.Prober {
	return instanceprobe.New(2*time.Second, logging.NewNoop())
}

func TestProbeActive(t *testing.T) {
	t.Parallel()

	srv := nexusStub{versionBody: `{"version":"1.20.2","commit":"abc","instance":"kz"}`}.server(t)
	res := newProber().Probe(context.Background(), srv.URL)

	assert.Equal(t, domain.PeerInstanceActive, res.Status)
	assert.Equal(t, "1.20.2", res.Version)
	assert.Equal(t, "kz", res.InstanceID)
	assert.Empty(t, res.Error)
	require.NotNil(t, res.LatencyMS)
	assert.GreaterOrEqual(t, *res.LatencyMS, 0)
}

// Нода без суффикса (§70.1) отдаёт пустой instance — это валидное значение, а
// не признак ошибки.
func TestProbeActiveWithoutInstanceID(t *testing.T) {
	t.Parallel()

	srv := nexusStub{versionBody: `{"version":"1.20.2"}`}.server(t)
	res := newProber().Probe(context.Background(), srv.URL)

	assert.Equal(t, domain.PeerInstanceActive, res.Status)
	assert.Equal(t, "1.20.2", res.Version)
	assert.Empty(t, res.InstanceID)
}

func TestProbeDegraded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		readyStatus int
		readyBody   string
	}{
		{
			name:      "optional dependency down",
			readyBody: `{"ready":true,"degraded":true,"checks":{"redis":"down: dial tcp"}}`,
		},
		{
			name:        "required dependency down returns 503",
			readyStatus: http.StatusServiceUnavailable,
			readyBody:   `{"ready":false,"checks":{"postgres":"down: dial tcp"}}`,
		},
		{
			name:      "ready false with 200",
			readyBody: `{"ready":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := nexusStub{
				versionBody: `{"version":"1.20.2","instance":"kz"}`,
				readyStatus: tt.readyStatus,
				readyBody:   tt.readyBody,
			}.server(t)

			res := newProber().Probe(context.Background(), srv.URL)
			assert.Equal(t, domain.PeerInstanceDegraded, res.Status)
			// Версия всё равно получена — инстанс жив, просто нездоров.
			assert.Equal(t, "1.20.2", res.Version)
		})
	}
}

// Тексты checks из /ready содержат внутренние адреса соседа и не должны
// просачиваться в результат пробы.
func TestProbeDoesNotLeakReadyChecks(t *testing.T) {
	t.Parallel()

	srv := nexusStub{
		versionBody: `{"version":"1.20.2"}`,
		readyStatus: http.StatusServiceUnavailable,
		readyBody:   `{"ready":false,"checks":{"postgres":"down: dial tcp 10.11.12.13:5432: connect: refused"}}`,
	}.server(t)

	res := newProber().Probe(context.Background(), srv.URL)
	assert.Equal(t, domain.PeerInstanceDegraded, res.Status)
	assert.NotContains(t, res.Error, "10.11.12.13")
	assert.NotContains(t, res.Error, "postgres")
}

func TestProbeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		versionStatus int
		versionBody   string
		wantStatus    domain.PeerInstanceStatus
		wantError     string
	}{
		{
			name:          "internal error",
			versionStatus: http.StatusInternalServerError,
			versionBody:   `boom`,
			wantStatus:    domain.PeerInstanceError,
			wantError:     "http 500",
		},
		{
			name:          "unauthorized proxy",
			versionStatus: http.StatusUnauthorized,
			versionBody:   `{}`,
			wantStatus:    domain.PeerInstanceError,
			wantError:     "http 401",
		},
		{
			name:        "html instead of json",
			versionBody: `<!doctype html><title>login</title>`,
			wantStatus:  domain.PeerInstanceError,
			wantError:   "invalid response",
		},
		{
			name:        "json without version",
			versionBody: `{"hello":"world"}`,
			wantStatus:  domain.PeerInstanceError,
			wantError:   "invalid response",
		},
		{
			name:        "empty version string",
			versionBody: `{"version":""}`,
			wantStatus:  domain.PeerInstanceError,
			wantError:   "invalid response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := nexusStub{versionStatus: tt.versionStatus, versionBody: tt.versionBody}.server(t)

			res := newProber().Probe(context.Background(), srv.URL)
			assert.Equal(t, tt.wantStatus, res.Status)
			assert.Equal(t, tt.wantError, res.Error)
			assert.Empty(t, res.Version)
			assert.Nil(t, res.LatencyMS)
		})
	}
}

// Текст ошибки парсера JSON вклеивает в себя кусок тела (грабли §68) — в
// last_error он попасть не должен.
func TestProbeErrorDoesNotLeakBody(t *testing.T) {
	t.Parallel()

	secret := "SECRET-TOKEN-abcdef"
	srv := nexusStub{versionBody: secret}.server(t)

	res := newProber().Probe(context.Background(), srv.URL)
	assert.Equal(t, domain.PeerInstanceError, res.Status)
	assert.NotContains(t, res.Error, secret)
	assert.Equal(t, "invalid response", res.Error)
}

// Редиректы не выполняются: следование за 30x увело бы серверный запрос на
// адрес, которого администратор не вводил.
func TestProbeDoesNotFollowRedirect(t *testing.T) {
	t.Parallel()

	target := nexusStub{versionBody: `{"version":"9.9.9"}`}.server(t)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	res := newProber().Probe(context.Background(), redirector.URL)
	assert.Equal(t, domain.PeerInstanceError, res.Status)
	assert.Equal(t, "http 302", res.Error)
	assert.Empty(t, res.Version, "версия с адреса редиректа не должна попасть в результат")
}

func TestProbeUnreachable(t *testing.T) {
	t.Parallel()

	// Занимаем порт и сразу освобождаем — соединение получит отказ.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	res := newProber().Probe(context.Background(), "http://"+addr)
	assert.Equal(t, domain.PeerInstanceUnreachable, res.Status)
	assert.Equal(t, "connection failed", res.Error)
	assert.Nil(t, res.LatencyMS)
}

func TestProbeTimeout(t *testing.T) {
	t.Parallel()

	srv := nexusStub{
		versionBody:  `{"version":"1.0.0"}`,
		versionDelay: 300 * time.Millisecond,
	}.server(t)

	res := instanceprobe.New(50*time.Millisecond, logging.NewNoop()).Probe(context.Background(), srv.URL)
	assert.Equal(t, domain.PeerInstanceUnreachable, res.Status)
	assert.Equal(t, "timeout", res.Error)
}

func TestProbeCanceledContext(t *testing.T) {
	t.Parallel()

	srv := nexusStub{versionBody: `{"version":"1.0.0"}`, versionDelay: time.Second}.server(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := newProber().Probe(ctx, srv.URL)
	assert.Equal(t, domain.PeerInstanceUnreachable, res.Status)
	assert.Equal(t, "canceled", res.Error)
}

// Ответ гигантского размера не читается целиком: io.LimitReader обрезает тело,
// и обрезанный JSON распознаётся как невалидный ответ, а не съедает память.
func TestProbeLimitsBodySize(t *testing.T) {
	t.Parallel()

	huge := `{"version":"1.0.0","pad":"` + strings.Repeat("x", 1<<20) + `"}`
	srv := nexusStub{versionBody: huge}.server(t)

	res := newProber().Probe(context.Background(), srv.URL)
	assert.Equal(t, domain.PeerInstanceError, res.Status)
	assert.Equal(t, "invalid response", res.Error)
}

// Отсутствие /ready (старая сборка соседа) не понижает статус: версия получена,
// значит инстанс отвечает.
func TestProbeMissingReadyKeepsActive(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.0.0","instance":"kz"}`))
	})
	// /ready не зарегистрирован — ServeMux ответит 404.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res := newProber().Probe(context.Background(), srv.URL)
	assert.Equal(t, domain.PeerInstanceActive, res.Status)
	assert.Equal(t, "1.0.0", res.Version)
}
