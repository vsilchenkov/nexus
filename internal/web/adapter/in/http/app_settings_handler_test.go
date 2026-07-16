package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/reloader"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// ---- фейки портов (зеркало fakes из usecase/app_settings_test.go) ----

type fakeSettingsRepo struct {
	mu        sync.Mutex
	current   *domain.AppSettings
	lastSaved *domain.AppSettings
}

func (f *fakeSettingsRepo) Get(_ context.Context) (*domain.AppSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == nil {
		return &domain.AppSettings{}, nil
	}
	cpy := *f.current
	return &cpy, nil
}

func (f *fakeSettingsRepo) Update(_ context.Context, s *domain.AppSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cpy := *s
	f.lastSaved = &cpy
	f.current = &cpy
	return nil
}

type nopAuditRepo struct{}

func (nopAuditRepo) Write(_ context.Context, _ *domain.AuditEntry) error { return nil }
func (nopAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (nopAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) { return 0, nil }

type capturePublisher struct {
	mu       sync.Mutex
	sections []string
}

func (p *capturePublisher) Publish(_ context.Context, s reloader.Section) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sections = append(p.sections, string(s))
	return nil
}

// newSettingsRouter собирает gin-роутер с реальным AppSettingsUsecase на
// фейках (tester=nil — test-эндпоинты не участвуют).
func newSettingsRouter(t *testing.T, repo *fakeSettingsRepo, pub *capturePublisher, allowVersionOverride bool) *gin.Engine {
	t.Helper()
	uc := usecase.NewAppSettingsUsecase(
		repo, usecase.NewAuditUsecase(nopAuditRepo{}, logging.NewNoop()), pub,
		allowVersionOverride, logging.NewNoop())
	h := NewAppSettingsHandler(uc, nil, logging.NewNoop())
	r := gin.New()
	r.GET("/api/settings/app", h.Get)
	r.PUT("/api/settings/app", h.Update)
	return r
}

func doSettings(t *testing.T, r *gin.Engine, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/settings/app", nil)
	} else {
		req = httptest.NewRequest(method, "/api/settings/app", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAppSettingsHandler_Update_LogLevel_NoContent(t *testing.T) {
	t.Parallel()
	repo := &fakeSettingsRepo{current: &domain.AppSettings{}}
	pub := &capturePublisher{}
	r := newSettingsRouter(t, repo, pub, false)

	w := doSettings(t, r, http.MethodPut, `{"logging":{"level":4}}`)

	assert.Equal(t, http.StatusNoContent, w.Code)
	require.NotNil(t, repo.lastSaved)
	require.NotNil(t, repo.lastSaved.Logging.Level)
	assert.Equal(t, 4, *repo.lastSaved.Logging.Level)
	assert.Equal(t, []string{"logging"}, pub.sections)
}

func TestAppSettingsHandler_Update_BadRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
	}{
		{"malformed json", `{"logging":`},
		{"log level above max", `{"logging":{"level":7}}`},
		{"log level below min", `{"logging":{"level":1}}`},
		{"session ttl too small", `{"security":{"session_ttl_seconds":10}}`},
		{"bad cron", `{"notifications":{"telegram":{"cron":"not a cron"}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeSettingsRepo{current: &domain.AppSettings{}}
			r := newSettingsRouter(t, repo, &capturePublisher{}, false)

			w := doSettings(t, r, http.MethodPut, tc.body)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Nil(t, repo.lastSaved, "invalid patch must not persist")
		})
	}
}

func TestAppSettingsHandler_Update_VersionOverrideForbidden(t *testing.T) {
	t.Parallel()
	repo := &fakeSettingsRepo{current: &domain.AppSettings{}}
	r := newSettingsRouter(t, repo, &capturePublisher{}, false)

	w := doSettings(t, r, http.MethodPut, `{"general":{"version_override":"dev-x"}}`)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Nil(t, repo.lastSaved)
}

func TestAppSettingsHandler_Get_MasksSecretsKeepsLogLevel(t *testing.T) {
	t.Parallel()
	repo := &fakeSettingsRepo{current: &domain.AppSettings{
		Sentry:     domain.SentrySettings{DSN: new("https://realtoken@sentry.io/123456")},
		ClickHouse: domain.ClickHouseSettings{Password: new("topsecret")},
		Logging:    domain.LoggingSettings{Level: new(5)},
	}}
	r := newSettingsRouter(t, repo, &capturePublisher{}, false)

	w := doSettings(t, r, http.MethodGet, "")

	require.Equal(t, http.StatusOK, w.Code)
	var got domain.AppSettings
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.NotNil(t, got.Sentry.DSN)
	assert.Contains(t, *got.Sentry.DSN, "***")
	assert.NotContains(t, *got.Sentry.DSN, "realtoken")
	require.NotNil(t, got.ClickHouse.Password)
	assert.Equal(t, "***", *got.ClickHouse.Password)
	require.NotNil(t, got.Logging.Level)
	assert.Equal(t, 5, *got.Logging.Level, "log level is not a secret")
}
