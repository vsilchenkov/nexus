package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// revertListRepo — журнал с заранее заданной выдачей.
type revertListRepo struct {
	aqAuditRepo
	entries []*domain.AuditEntry
}

func (r revertListRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return r.entries, nil
}

// fakeAnnotator — разметка строк: запоминает, спрашивали ли её.
type fakeAnnotator struct {
	marks  map[string]usecase.RevertMark
	called bool
}

func (a *fakeAnnotator) Annotate(_ context.Context, _ []*domain.AuditEntry) map[string]usecase.RevertMark {
	a.called = true
	return a.marks
}

// failingBatch — чтение узлов, которое всегда падает.
type failingBatch struct{}

func (failingBatch) GetByIDs(_ context.Context, _ []string) (map[string]*domain.Node, error) {
	return nil, errors.New("pg down")
}

const revertHandlerNodeID = "9c1f4a52-6f0e-4d7c-9a51-2f1f0b8a3e10"

func revertListEntries() []*domain.AuditEntry {
	return []*domain.AuditEntry{{
		ID: "e1", Action: domain.ActionNodeUpdate, TargetType: "node", TargetID: revertHandlerNodeID,
		Details: map[string]any{"status": map[string]any{"before": "paused", "after": "enabled"}},
	}}
}

// auditListEngine — GET /api/audit с сессией заданной роли; viaToken
// имитирует вход по API-токену.
func auditListEngine(h *AuditHandler, role domain.UserRole, viaToken bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: role, CurrentTeamID: "t1"})
		if viaToken {
			c.Set(ctxAPITokenKey, &domain.APIToken{})
		}
		c.Next()
	})
	r.GET("/api/audit", h.List)
	return r
}

func listItems(t *testing.T, r *gin.Engine) []map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Items
}

// TestAuditList_RevertMarkOnlyForAdminSession (§108.7): разметка раскрывает
// текущие значения узлов любой команды — ни операторам, ни API-токену её не
// отдают, и сама разметка для них даже не считается.
func TestAuditList_RevertMarkOnlyForAdminSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		role     domain.UserRole
		viaToken bool
		want     bool
	}{
		{"admin session", domain.UserRoleAdmin, false, true},
		{"admin api token", domain.UserRoleAdmin, true, false},
		{"manager", domain.UserRoleManager, false, false},
		{"operator", domain.UserRoleOperator, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ann := &fakeAnnotator{marks: map[string]usecase.RevertMark{"e1": {
				State: usecase.RevertStateStale, Revertible: true,
				Overwritten: map[string]any{"status": "paused"},
			}}}
			uc := usecase.NewAuditUsecase(revertListRepo{entries: revertListEntries()}, logging.NewNoop())
			h := NewAuditHandler(uc, logging.NewNoop()).WithRevert(ann)

			items := listItems(t, auditListEngine(h, tc.role, tc.viaToken))
			require.Len(t, items, 1)
			mark, has := items[0]["revert"]
			assert.Equal(t, tc.want, has, "поле revert")
			assert.Equal(t, tc.want, ann.called, "разметка считается только для admin-сессии")
			if tc.want {
				assert.Equal(t, map[string]any{
					"state": "stale", "revertible": true,
					"overwritten": map[string]any{"status": "paused"},
				}, mark)
			}
		})
	}
}

// TestAuditList_RevertMarkFailOpen: упавшее чтение узлов не роняет журнал —
// выдача та же, только без разметки.
func TestAuditList_RevertMarkFailOpen(t *testing.T) {
	t.Parallel()
	entries := revertListRepo{entries: revertListEntries()}
	revertUC := usecase.NewAuditRevertUsecase(entries, failingBatch{}, nil, logging.NewNoop())
	h := NewAuditHandler(usecase.NewAuditUsecase(entries, logging.NewNoop()), logging.NewNoop()).
		WithRevert(revertUC)

	items := listItems(t, auditListEngine(h, domain.UserRoleAdmin, false))
	require.Len(t, items, 1)
	assert.NotContains(t, items[0], "revert")
}

// fakeRevertUC — предпросмотр и применение с заданным исходом.
type fakeRevertUC struct {
	preview *usecase.RevertPreview
	err     error

	gotEntry    string
	gotFields   []string
	gotExpected time.Time
	gotActor    usecase.Actor
}

func (f *fakeRevertUC) Preview(_ context.Context, id string) (*usecase.RevertPreview, error) {
	f.gotEntry = id
	return f.preview, f.err
}

func (f *fakeRevertUC) Apply(_ context.Context, actor usecase.Actor, id string, fields []string, expected time.Time) error {
	f.gotEntry, f.gotFields, f.gotExpected, f.gotActor = id, fields, expected, actor
	return f.err
}

func revertEngine(uc *fakeRevertUC) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Login: "admin", Role: domain.UserRoleAdmin, CurrentTeamID: "t1"})
		c.Next()
	})
	h := NewAuditRevertHandler(uc, logging.NewNoop())
	r.GET("/api/audit/revert/:id", h.Preview)
	r.POST("/api/audit/revert/:id", h.Apply)
	return r
}

func postRevert(r *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/audit/revert/e1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

const revertApplyBody = `{"fields":["target_url"],"expected_updated_at":"2026-09-24T10:00:00.123456Z"}`

// TestAuditRevertApply_ErrorMapping — доменные ошибки → статус и код (§108.7).
// Неизвестная ошибка — 500 без её текста в ответе.
func TestAuditRevertApply_ErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantExtra  map[string]any
	}{
		{"entry not found", domain.ErrAuditEntryNotFound, http.StatusNotFound, "audit.revert.entry_not_found", nil},
		{"not revertible", domain.ErrAuditNotRevertible, http.StatusBadRequest, "audit.revert.not_revertible", nil},
		{"field invalid", fmt.Errorf("%w: status", domain.ErrAuditRevertFieldInvalid), http.StatusBadRequest, "audit.revert.field_invalid", nil},
		{"conflict", domain.ErrAuditRevertConflict, http.StatusConflict, "audit.revert.conflict", nil},
		{
			"not held", fmt.Errorf("revert: %w", &usecase.RevertNotHeldError{Fields: []string{"sse_decline"}}),
			http.StatusBadRequest, "audit.revert.not_held", map[string]any{"fields": []any{"sse_decline"}},
		},
		{"node gone", domain.ErrNodeNotFound, http.StatusNotFound, "audit.revert.node_gone", nil},
		{"path taken", domain.ErrNodeAlreadyExists, http.StatusConflict, "node.already_exists", nil},
		{
			"node validation", fmt.Errorf("validate: %w", domain.ErrNodeRMQHostRequired),
			http.StatusBadRequest, "node.validation.rmq_host", map[string]any{"field": "rmq_host"},
		},
		{"internal", errors.New("pq: secret-ish details"), http.StatusInternalServerError, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := postRevert(revertEngine(&fakeRevertUC{err: tc.err}), revertApplyBody)
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.NotContains(t, w.Body.String(), "secret-ish", "текст внутренней ошибки наружу не уходит")
			if tc.wantCode != "" {
				assert.Equal(t, tc.wantCode, body["code"])
				assert.NotEmpty(t, body["error"])
			}
			for k, v := range tc.wantExtra {
				assert.Equal(t, v, body[k], k)
			}
		})
	}
}

// TestAuditRevertApply_PassesRequest: строка updated_at разбирается без потери
// микросекунд, поля и автор доходят до usecase; кривой запрос — 400 без эха
// тела.
func TestAuditRevertApply_PassesRequest(t *testing.T) {
	t.Parallel()
	uc := &fakeRevertUC{}
	w := postRevert(revertEngine(uc), revertApplyBody)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, "e1", uc.gotEntry)
	assert.Equal(t, []string{"target_url"}, uc.gotFields)
	assert.Equal(t, 123456000, uc.gotExpected.Nanosecond(), "микросекунды не теряются")
	assert.Equal(t, "u1", uc.gotActor.UserID)

	for name, body := range map[string]string{
		"bad json":       `{"fields":["x"],"expected_updated_at":<leak>}`,
		"bad timestamp":  `{"fields":["x"],"expected_updated_at":"вчера"}`,
		"missing fields": `{"expected_updated_at":"2026-09-24T10:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := postRevert(revertEngine(&fakeRevertUC{}), body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.NotContains(t, w.Body.String(), "leak")
		})
	}
}

// TestAuditRevertPreview_Response — форма ответа предпросмотра (§108.7).
func TestAuditRevertPreview_Response(t *testing.T) {
	t.Parallel()
	updated := time.Date(2026, 9, 24, 10, 0, 0, 123456000, time.UTC)
	p := &usecase.RevertPreview{
		Entry: &domain.AuditEntry{ID: "e1", Action: domain.ActionNodeUpdate, UserLogin: "Иванов"},
		Node:  &domain.Node{ID: "n1", Path: "svc/x", TeamID: "t2", UpdatedAt: updated},
		State: usecase.RevertStateStale, Moved: true,
		Fields: []usecase.RevertField{
			{Name: "target_url", Before: "http://a", After: "http://b", Current: "http://c", Status: usecase.RevertFieldOverwritten},
			{Name: "auth_credentials", Before: "changed", After: "changed", Status: usecase.RevertFieldSecret},
		},
		DryRunErr: &usecase.RevertNotHeldError{Fields: []string{"sse_decline"}},
	}
	body := getPreview(t, revertEngine(&fakeRevertUC{preview: p}))

	assert.Equal(t, "2026-09-24T10:00:00.123456Z", body["node"].(map[string]any)["updated_at"])
	assert.Equal(t, "stale", body["state"])
	assert.Equal(t, true, body["moved"])
	fields := body["fields"].([]any)
	require.Len(t, fields, 2)
	assert.Equal(t, "http://c", fields[0].(map[string]any)["current"])
	assert.Nil(t, fields[1].(map[string]any)["current"], "у секрета текущего значения нет")
	assert.Equal(t, map[string]any{
		"code": "audit.revert.not_held", "field": "", "not_held": []any{"sse_decline"},
		"error": body["dry_run"].(map[string]any)["error"],
	}, body["dry_run"])
}

// TestAuditRevertPreview_GoneAndInternalDryRun: удалённый узел — node null,
// not_held пустым массивом; внутренняя ошибка пробного прогона предпросмотр
// не роняет и наружу не утекает.
func TestAuditRevertPreview_GoneAndInternalDryRun(t *testing.T) {
	t.Parallel()
	p := &usecase.RevertPreview{
		Entry: &domain.AuditEntry{ID: "e1", Action: domain.ActionNodeUpdate},
		State: usecase.RevertStateGone, Fields: []usecase.RevertField{},
		DryRunErr: errors.New("clickhouse: secret-ish"),
	}
	body := getPreview(t, revertEngine(&fakeRevertUC{preview: p}))
	assert.Nil(t, body["node"])
	dry := body["dry_run"].(map[string]any)
	assert.Equal(t, "error.internal", dry["code"])
	assert.Equal(t, []any{}, dry["not_held"])
	assert.NotContains(t, fmt.Sprint(body), "secret-ish")
}

func getPreview(t *testing.T, r *gin.Engine) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/audit/revert/e1", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}
