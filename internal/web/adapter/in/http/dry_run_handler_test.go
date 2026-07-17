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
	senderv1 "nexus/proto/sender/v1"
)

// --- стабы ---

// drNodes — сохранённый узел (nodeCredsLoader). Фиксирует, с каким скоупом
// команды его спросили: подмешивание кредов обязано быть team-scoped.
type drNodes struct {
	node       *domain.Node
	err        error
	gotID      string
	gotTeamID  string
	callsCount int
}

func (r *drNodes) Get(_ context.Context, id, teamID string) (*domain.Node, error) {
	r.gotID, r.gotTeamID = id, teamID
	r.callsCount++
	if r.err != nil {
		return nil, r.err
	}
	return r.node, nil
}

type drAuditRepo struct{}

func (drAuditRepo) Write(_ context.Context, _ *domain.AuditEntry) error { return nil }
func (drAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (drAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) { return 0, nil }

// drSender — SenderClient: ловит исходящий запрос к target.
type drSender struct{ got *senderv1.SendRequest }

func (s *drSender) Send(_ context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.got = req
	return &senderv1.SendResponse{StatusCode: 200, Attempts: 1}, nil
}

// drEngine — движок с сессией (admin, команда t1) и роутом dry-run.
func drEngine(nodes nodeCredsLoader, sender port.SenderClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	uc := usecase.NewDryRunUsecase(
		usecase.NewAuditUsecase(drAuditRepo{}, logging.NewNoop()),
		sender, nil, logging.NewNoop(),
	)
	h := NewDryRunHandler(uc, nodes, logging.NewNoop())

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxSessionKey, &domain.Session{UserID: "u1", Role: domain.UserRoleAdmin, CurrentTeamID: "t1"})
		c.Next()
	})
	r.POST("/api/nodes/dry-run", h.Run)
	return r
}

func drPost(t *testing.T, r *gin.Engine, body string) (*httptest.ResponseRecorder, *usecase.DryRunReport) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/dry-run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return w, nil
	}
	var rep usecase.DryRunReport
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rep))
	return w, &rep
}

const drNodeID = "3f4a1b2c-0000-4000-8000-000000000001"

// storedNode — сохранённый узел с кредами (в БД они есть, наружу не отдаются).
func storedNode() *domain.Node {
	return &domain.Node{
		ID:                      drNodeID,
		TeamID:                  "t1",
		Path:                    "team/node",
		RootMethod:              domain.RootMethodRequest,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               "https://example.com/hook",
		AuthType:                domain.AuthTypeToken,
		AuthCredentials:         "stored-secret-token",
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		IncomingAuthCredentials: "stored-incoming",
		Status:                  domain.NodeStatusEnabled,
	}
}

// drBody — тело как его шлёт фронт для СОХРАНЁННОГО узла: креды пустые, потому
// что API их не отдаёт (nodeToResponse → только auth_credentials_set).
func drBody(nodeID string, useMock bool) string {
	b, _ := json.Marshal(map[string]any{
		"node": map[string]any{
			"path":               "team/node",
			"root_method":        "request",
			"url_mode":           "static",
			"target_url":         "https://example.com/hook",
			"auth_type":          "token",
			"auth_credentials":   "",
			"incoming_auth_type": "none",
			"status":             "enabled",
			"clickhouse_table":   "nexus.demo",
		},
		"node_id":  nodeID,
		"request":  map[string]any{"method": "POST", "body": "{}"},
		"use_mock": useMock,
	})
	return string(b)
}

// §55.6 — ради этого кнопка «Тестовый запрос» на странице узла вообще может
// работать: креды сохранённого узла наружу не отдаются, и без подмешивания
// реальный вызов ушёл бы с пустым `Bearer ` → 401, а оператор решил бы, что
// сломана авторизация узла.
func TestDryRun_StoredCredsMergedIntoRealCall(t *testing.T) {
	t.Parallel()

	nodes := &drNodes{node: storedNode()}
	sender := &drSender{}
	w, rep := drPost(t, drEngine(nodes, sender), drBody(drNodeID, false))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, rep)

	require.NotNil(t, sender.got, "реальный режим обязан дойти до Sender")
	require.NotNil(t, sender.got.GetAuth())
	assert.Equal(t, "Bearer stored-secret-token", sender.got.GetAuth().GetAuthorizationHeader(),
		"в target ушли сохранённые креды, а не пустой заголовок")

	assert.Equal(t, drNodeID, nodes.gotID)
	assert.Equal(t, "t1", nodes.gotTeamID,
		"узел читается в скоупе текущей команды — чужой не подтянуть")

	// Отчёт не светит сам кред (§55.6 — только маска).
	assert.NotContains(t, w.Body.String(), "stored-secret-token")
	assert.Contains(t, w.Body.String(), "Bearer ***")
}

// Изоляция команд: узел чужой команды → NodeUsecase.Get даёт ErrNodeNotFound →
// 404, а не молчаливый прогон с пустыми кредами.
func TestDryRun_ForeignNodeIsNotFound(t *testing.T) {
	t.Parallel()

	nodes := &drNodes{err: domain.ErrNodeNotFound}
	sender := &drSender{}
	w, _ := drPost(t, drEngine(nodes, sender), drBody(drNodeID, false))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Nil(t, sender.got, "чужой узел — запрос к target не уходит")
}

// Явно введённый в форме кред побеждает сохранённый: оператор проверяет НОВОЕ
// значение, иначе тест «нового кредa» молча тестировал бы старый (§5.5).
func TestDryRun_ExplicitCredsWinOverStored(t *testing.T) {
	t.Parallel()

	nodes := &drNodes{node: storedNode()}
	sender := &drSender{}
	body, _ := json.Marshal(map[string]any{
		"node": map[string]any{
			"path": "team/node", "root_method": "request", "url_mode": "static",
			"target_url": "https://example.com/hook",
			"auth_type":  "token", "auth_credentials": "typed-in-form",
			"incoming_auth_type": "none", "status": "enabled",
			"clickhouse_table": "nexus.demo",
		},
		"node_id":  drNodeID,
		"request":  map[string]any{"method": "POST"},
		"use_mock": false,
	})
	w, _ := drPost(t, drEngine(nodes, sender), string(body))
	require.Equal(t, http.StatusOK, w.Code)

	require.NotNil(t, sender.got.GetAuth())
	assert.Equal(t, "Bearer typed-in-form", sender.got.GetAuth().GetAuthorizationHeader())
}

// Создание узла (node_id пуст) — сохранённого конфига нет, в БД не ходим.
func TestDryRun_NoNodeIDSkipsLookup(t *testing.T) {
	t.Parallel()

	nodes := &drNodes{node: storedNode()}
	w, rep := drPost(t, drEngine(nodes, &drSender{}), drBody("", true))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, rep)

	assert.Zero(t, nodes.callsCount, "без node_id обращения к БД быть не должно")
}

// node_id не-uuid отсекается биндингом: 400, до usecase не доходит.
func TestDryRun_InvalidNodeIDRejected(t *testing.T) {
	t.Parallel()

	nodes := &drNodes{node: storedNode()}
	w, _ := drPost(t, drEngine(nodes, &drSender{}), drBody("not-a-uuid", true))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Zero(t, nodes.callsCount)
}

// use_mock по умолчанию (поле не прислано) = true — старый контракт сохраняется,
// клиент без §55 не начнёт внезапно бить в реальный target.
func TestDryRun_UseMockDefaultsToTrue(t *testing.T) {
	t.Parallel()

	sender := &drSender{}
	body, _ := json.Marshal(map[string]any{
		"node": map[string]any{
			"path": "team/node", "root_method": "request", "url_mode": "static",
			"target_url": "https://example.com/hook",
			"auth_type":  "none", "incoming_auth_type": "none", "status": "enabled",
			"clickhouse_table": "nexus.demo",
		},
		"request": map[string]any{"method": "POST"},
	})
	w, rep := drPost(t, drEngine(&drNodes{}, sender), string(body))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, rep)

	assert.Nil(t, sender.got, "по умолчанию реального вызова быть не должно")
}
