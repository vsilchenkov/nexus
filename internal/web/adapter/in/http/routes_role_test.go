package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// Тесты границы роли `operator` (§87). Гейт живёт в МАРШРУТИЗАЦИИ, поэтому оба
// теста поднимают настоящий RegisterAPI, а не пересобирают группы руками: иначе
// они разъехались бы с routes.go при первом же переносе маршрута и продолжали
// зеленеть. Побочная польза — RegisterAPI вызывается целиком, так что дубль
// (метод, путь) в двух группах (gin паникует при старте) падает здесь, а не на
// боевом запуске Web.
//
// Handler'ы — нулевые указатели: до них доходят только разрешённые запросы, и
// нам достаточно факта «гейт пропустил». Method value на nil-указателе
// регистрируется без разыменования, а вызов ловит gin.Recovery и превращает в
// 500 — отсюда критерий «не 403» для разрешённых маршрутов.

// roleEngine собирает движок с настоящими маршрутами и сессией заданной роли.
func roleEngine(role domain.UserRole) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Recovery с io.Discard: nil-handler разрешённого маршрута паникует штатно
	// (см. комментарий выше), и стектрейс в выводе теста только мешает.
	r.Use(gin.RecoveryWithWriter(io.Discard))

	mw := Middlewares{
		APITokenAuth: func(c *gin.Context) { c.Next() },
		SessionAuth: func(c *gin.Context) {
			c.Set(ctxSessionKey, &domain.Session{
				UserID: "u1", Login: "u1", Role: role, CurrentTeamID: "t1",
			})
			c.Next()
		},
		RequireAdmin:    RequireMinRole(domain.UserRoleAdmin),
		RequireManager:  RequireMinRole(domain.UserRoleManager),
		RequireOperator: RequireMinRole(domain.UserRoleOperator),
	}

	// Ненулевые указатели там, где регистрация маршрута под `if h.X != nil`.
	h := Handlers{
		Replay:        &ReplayHandler{},
		AsyncQueue:    &AsyncQueueHandler{},
		Breaker:       &BreakerHandler{},
		HostAllowlist: &HostAllowlistHandler{},
		HeaderCatalog: &HeaderCatalogHandler{},
		NodeGroup:     &NodeGroupHandler{},
		RequestField:  &RequestFieldCatalogHandler{},
		CHSchema:      &CHSchemaHandler{},
		Kafka:         &KafkaHandler{},
		Team:          &TeamHandler{},
	}
	RegisterAPI(r, h, mw)
	return r
}

func roleStatus(r *gin.Engine, method, path string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code
}

// operatorAllowed — маршруты эксплуатации узла: вкладка «Очередь» целиком,
// статус, сброс защиты, повторы из логов и чтение аудита (§87.3).
var operatorAllowed = []struct{ method, path string }{
	{http.MethodPatch, "/api/nodes/n1/status"},
	{http.MethodPost, "/api/logs/l1/replay"},
	{http.MethodPost, "/api/nodes/n1/logs/replay-period/plan"},
	{http.MethodPost, "/api/nodes/n1/logs/replay-period/run"},
	{http.MethodPost, "/api/nodes/n1/breaker/reset"},
	{http.MethodGet, "/api/nodes/n1/async-queue/messages"},
	{http.MethodGet, "/api/nodes/n1/async-queue/messages/body"},
	{http.MethodDelete, "/api/nodes/n1/async-queue/messages/m1"},
	{http.MethodPost, "/api/nodes/n1/async-queue/purge"},
	{http.MethodPost, "/api/nodes/n1/async-queue/purge-failed"},
	{http.MethodPost, "/api/nodes/n1/async-queue/replay-failed"},
	{http.MethodGet, "/api/audit"},
	{http.MethodGet, "/api/audit/export.csv"},
}

// operatorForbidden — конфигурация узла и административный периметр: оператор
// узлы не создаёт, не меняет и не копирует (§87.3).
var operatorForbidden = []struct{ method, path string }{
	{http.MethodPost, "/api/nodes"},
	{http.MethodPut, "/api/nodes/n1"},
	{http.MethodDelete, "/api/nodes/n1"},
	{http.MethodPost, "/api/nodes/n1/copy"},
	{http.MethodPost, "/api/nodes/dry-run"},
	{http.MethodPost, "/api/nodes/n1/ch-schema/plan"},
	{http.MethodPost, "/api/nodes/n1/ch-schema/apply"},
	{http.MethodPost, "/api/allowed-hosts"},
	{http.MethodPost, "/api/nodes/n1/allowed-hosts"},
	{http.MethodPost, "/api/headers"},
	{http.MethodPost, "/api/node-groups"},
	{http.MethodPatch, "/api/node-groups/g1"},
	{http.MethodDelete, "/api/node-groups/g1"},
	{http.MethodPost, "/api/node-groups/g1/move"},
	{http.MethodPost, "/api/request-fields"},
	{http.MethodPost, "/api/nodes/n1/move"},
	{http.MethodPost, "/api/users"},
	{http.MethodPost, "/api/teams"},
	{http.MethodGet, "/api/kafka/overview"},
}

func TestOperatorRoutes_QueueStatusReplayAndAuditAllowed(t *testing.T) {
	t.Parallel()
	r := roleEngine(domain.UserRoleOperator)

	for _, rt := range operatorAllowed {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			assert.NotEqual(t, http.StatusForbidden, roleStatus(r, rt.method, rt.path),
				"оператор обязан проходить гейт роли на %s %s", rt.method, rt.path)
		})
	}
}

func TestOperatorRoutes_NodeConfigForbidden(t *testing.T) {
	t.Parallel()
	r := roleEngine(domain.UserRoleOperator)

	for _, rt := range operatorForbidden {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, roleStatus(r, rt.method, rt.path),
				"оператор не должен менять конфигурацию: %s %s", rt.method, rt.path)
		})
	}
}

// Регрессия на сам гейт: если бы операторские маршруты остались в группе
// `authed`, предыдущий тест зеленел бы и для наблюдателя.
func TestOperatorRoutes_ViewerStillForbidden(t *testing.T) {
	t.Parallel()
	r := roleEngine(domain.UserRoleViewer)

	for _, rt := range operatorAllowed {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, roleStatus(r, rt.method, rt.path),
				"наблюдателю эксплуатация узла закрыта: %s %s", rt.method, rt.path)
		})
	}
}

// Менеджер и админ проходят операторские гейты автоматически (иерархия §26).
func TestOperatorRoutes_ManagerAndAdminInherit(t *testing.T) {
	t.Parallel()
	for _, role := range []domain.UserRole{domain.UserRoleManager, domain.UserRoleAdmin} {
		r := roleEngine(role)
		for _, rt := range operatorAllowed {
			t.Run(string(role)+" "+rt.path, func(t *testing.T) {
				assert.NotEqual(t, http.StatusForbidden, roleStatus(r, rt.method, rt.path),
					"%s обязан проходить операторский гейт на %s", role, rt.path)
			})
		}
	}
}

// TestNodeGroupRoutes_ListOpenToEveryRole: чтение справочника групп (§99.4)
// доступно ЛЮБОЙ сессии, включая наблюдателя, — экран «Узлы» группирует список
// для всех ролей, и закрытый GET оставил бы viewer'а без группировки вовсе.
// Мутации при этом закрыты (см. operatorForbidden выше).
func TestNodeGroupRoutes_ListOpenToEveryRole(t *testing.T) {
	t.Parallel()
	for _, role := range []domain.UserRole{
		domain.UserRoleViewer, domain.UserRoleOperator,
		domain.UserRoleManager, domain.UserRoleAdmin,
	} {
		r := roleEngine(role)
		t.Run(string(role), func(t *testing.T) {
			assert.NotEqual(t, http.StatusForbidden, roleStatus(r, http.MethodGet, "/api/node-groups"),
				"%s обязан читать справочник групп", role)
		})
	}
}
