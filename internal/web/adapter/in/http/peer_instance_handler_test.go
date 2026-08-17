package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// --- стабы (§73) ---

type piRepo struct {
	items     []*domain.PeerInstance
	createErr error
}

func (r *piRepo) ListPeerInstances(context.Context) ([]*domain.PeerInstance, error) {
	return r.items, nil
}

func (r *piRepo) GetPeerInstance(_ context.Context, id string) (*domain.PeerInstance, error) {
	for _, p := range r.items {
		if p.ID == id {
			clone := *p
			return &clone, nil
		}
	}
	return nil, domain.ErrPeerInstanceNotFound
}

func (r *piRepo) CreatePeerInstance(_ context.Context, p *domain.PeerInstance) error {
	if r.createErr != nil {
		return r.createErr
	}
	p.ID = "new-id"
	r.items = append(r.items, p)
	return nil
}

func (r *piRepo) UpdatePeerInstance(context.Context, *domain.PeerInstance) error { return nil }
func (r *piRepo) DeletePeerInstance(context.Context, string) error               { return nil }
func (r *piRepo) SavePeerInstanceProbe(context.Context, string, port.PeerInstanceProbe) error {
	return nil
}

type piAuditRepo struct{}

func (piAuditRepo) Write(context.Context, *domain.AuditEntry) error { return nil }
func (piAuditRepo) List(context.Context, port.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (piAuditRepo) Count(context.Context, port.AuditFilter) (int, error)    { return 0, nil }
func (piAuditRepo) DeleteOlderThan(context.Context, time.Time) (int, error) { return 0, nil }

type piProber struct {
	res   port.InstanceProbeResult
	calls []string
}

func (p *piProber) Probe(_ context.Context, baseURL string) port.InstanceProbeResult {
	p.calls = append(p.calls, baseURL)
	return p.res
}

func newPIHandler(repo *piRepo, prober *piProber) *PeerInstanceHandler {
	uc := usecase.NewPeerInstanceUsecase(repo, prober,
		usecase.NewAuditUsecase(piAuditRepo{}, logging.NewNoop()), logging.NewNoop())
	// limiter=nil — квота проверяется отдельно на живом Redis; здесь важны коды
	// ответов и маппинг доменных ошибок.
	return NewPeerInstanceHandler(uc, nil, 0, logging.NewNoop())
}

func doJSON(t *testing.T, h gin.HandlerFunc, method, target, body string, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	h(c)
	return w
}

func TestPeerInstanceHandlerList(t *testing.T) {
	latency := 42
	checked := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	repo := &piRepo{items: []*domain.PeerInstance{{
		ID: "id-1", Title: "Казахстан", BaseURL: "https://kz.example.ru",
		LastStatus: domain.PeerInstanceActive, LastVersion: "1.20.2", LastInstanceID: "kz",
		LastLatencyMS: &latency, LastCheckedAt: &checked,
	}}}
	h := newPIHandler(repo, &piProber{})

	w := doJSON(t, h.List, http.MethodGet, "/api/instances", "", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var got ListPeerInstancesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, "active", got.Items[0].LastStatus)
	assert.Equal(t, "1.20.2", got.Items[0].LastVersion)
	assert.Equal(t, "kz", got.Items[0].LastInstanceID)
	require.NotNil(t, got.Items[0].LastLatencyMS)
	assert.Equal(t, 42, *got.Items[0].LastLatencyMS)
}

// Пустой реестр отдаёт [], а не null: клиент рендерит list.items.length без
// защиты от null.
func TestPeerInstanceHandlerListEmptyIsArray(t *testing.T) {
	h := newPIHandler(&piRepo{}, &piProber{})

	w := doJSON(t, h.List, http.MethodGet, "/api/instances", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"items":[]`)
}

func TestPeerInstanceHandlerCreate(t *testing.T) {
	repo := &piRepo{}
	h := newPIHandler(repo, &piProber{})

	w := doJSON(t, h.Create, http.MethodPost, "/api/instances",
		`{"title":"Казахстан","base_url":"HTTPS://KZ.example.ru/","comment":"тест"}`, nil)
	require.Equal(t, http.StatusCreated, w.Code)

	var got PeerInstanceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "https://kz.example.ru", got.BaseURL, "адрес должен быть нормализован")
	assert.Equal(t, "unknown", got.LastStatus, "новая запись ещё не проверялась")
}

func TestPeerInstanceHandlerCreateErrors(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		createErr error
		wantCode  int
		wantKey   string
	}{
		{
			name:     "address without scheme",
			body:     `{"title":"x","base_url":"kz.example.ru"}`,
			wantCode: http.StatusBadRequest,
			wantKey:  "instance.url_invalid",
		},
		{
			name:     "address with credentials",
			body:     `{"title":"x","base_url":"https://user:pass@kz.example.ru"}`,
			wantCode: http.StatusBadRequest,
			wantKey:  "instance.url_invalid",
		},
		{
			name:     "address with path",
			body:     `{"title":"x","base_url":"https://kz.example.ru/nexus"}`,
			wantCode: http.StatusBadRequest,
			wantKey:  "instance.url_invalid",
		},
		{
			// Пустой title отсекается binding'ом до доменной валидации.
			name:     "empty title",
			body:     `{"title":"","base_url":"https://kz.example.ru"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:      "duplicate address",
			body:      `{"title":"x","base_url":"https://kz.example.ru"}`,
			createErr: domain.ErrPeerInstanceAlreadyExists,
			wantCode:  http.StatusConflict,
			wantKey:   "instance.already_exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPIHandler(&piRepo{createErr: tt.createErr}, &piProber{})
			w := doJSON(t, h.Create, http.MethodPost, "/api/instances", tt.body, nil)
			require.Equal(t, tt.wantCode, w.Code)
			if tt.wantKey != "" {
				assert.Contains(t, w.Body.String(), tt.wantKey)
			}
		})
	}
}

func TestPeerInstanceHandlerDeleteMissing(t *testing.T) {
	h := newPIHandler(&piRepo{}, &piProber{})

	w := doJSON(t, h.Delete, http.MethodDelete, "/api/instances/nope", "",
		gin.Params{{Key: "id", Value: "nope"}})
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "instance.not_found")
}

func TestPeerInstanceHandlerCheckOne(t *testing.T) {
	repo := &piRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://a.example.ru"},
	}}
	prober := &piProber{res: port.InstanceProbeResult{
		Status: domain.PeerInstanceDegraded, Version: "1.20.2", InstanceID: "kz",
	}}
	h := newPIHandler(repo, prober)

	w := doJSON(t, h.CheckOne, http.MethodPost, "/api/instances/id-1/check", "",
		gin.Params{{Key: "id", Value: "id-1"}})
	require.Equal(t, http.StatusOK, w.Code)

	var got PeerInstanceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "degraded", got.LastStatus)
	assert.Equal(t, []string{"https://a.example.ru"}, prober.calls)
}

// Недоступность соседа — это его статус, а не ошибка запроса: ручка обязана
// вернуть 200, иначе вкладка показала бы ошибку вместо таблицы.
func TestPeerInstanceHandlerCheckAllReturnsOKForDeadPeers(t *testing.T) {
	repo := &piRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://dead.example.ru"},
	}}
	prober := &piProber{res: port.InstanceProbeResult{
		Status: domain.PeerInstanceUnreachable, Error: "timeout",
	}}
	h := newPIHandler(repo, prober)

	w := doJSON(t, h.CheckAll, http.MethodPost, "/api/instances/check", "", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var got ListPeerInstancesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, "unreachable", got.Items[0].LastStatus)
	assert.Equal(t, "timeout", got.Items[0].LastError)
}

func TestPeerInstanceHandlerProbe(t *testing.T) {
	latency := 7
	prober := &piProber{res: port.InstanceProbeResult{
		Status: domain.PeerInstanceActive, Version: "1.20.2", InstanceID: "kz", LatencyMS: &latency,
	}}
	h := newPIHandler(&piRepo{}, prober)

	w := doJSON(t, h.Probe, http.MethodPost, "/api/instances/probe",
		`{"base_url":"https://KZ.example.ru/"}`, nil)
	require.Equal(t, http.StatusOK, w.Code)

	var got ProbeInstanceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "1.20.2", got.Version)
	assert.Equal(t, "kz", got.InstanceID)
	assert.Equal(t, []string{"https://kz.example.ru"}, prober.calls, "адрес нормализуется до пробы")
}

func TestPeerInstanceHandlerProbeRejectsBadAddress(t *testing.T) {
	prober := &piProber{}
	h := newPIHandler(&piRepo{}, prober)

	w := doJSON(t, h.Probe, http.MethodPost, "/api/instances/probe",
		`{"base_url":"file:///etc/passwd"}`, nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "instance.url_invalid")
	assert.Empty(t, prober.calls, "невалидный адрес не должен уходить в сеть")
}
