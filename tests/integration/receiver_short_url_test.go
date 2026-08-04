//go:build integration

package integration

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
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	rcvhttp "nexus/internal/receiver/adapter/in/http"
	rcv "nexus/internal/receiver/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// shortURLProducer — AsyncProducer, считающий сообщения. Kafka здесь не нужна:
// проверяется маршрутизация, а доставку async покрывает TestSender_Async_E2E.
type shortURLProducer struct{ produced int }

func (p *shortURLProducer) Produce(_ context.Context, _, _ string, _ []byte, _ map[string]string) error {
	p.produced++
	return nil
}

// TestReceiver_ShortURL_E2E (§78.1) — короткий адрес узла на полном HTTP-стеке
// Receiver'а поверх РЕАЛЬНОГО PostgreSQL: gin-роутер, middleware, резолв узла
// теми же SQL-запросами, что в бою.
//
// Unit-тесты handler'а этого не покрывают: там stub-reader отдаёт узел на любой
// путь, поэтому интерпретации «первый сегмент — слог команды / часть пути»
// (resolveNode) не проверяются вовсе.
func TestReceiver_ShortURL_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	var gotUpstreamPaths []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpstreamPaths = append(gotUpstreamPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	newNode := func(path string, root domain.RootMethod) *domain.Node {
		return &domain.Node{
			Path:             path,
			RootMethod:       root,
			URLMode:          domain.URLModeStatic,
			TargetURL:        mock.URL + "/upstream/" + path,
			IncomingMethod:   domain.HTTPMethodAny,
			OutgoingMethod:   domain.HTTPMethodPOST,
			IncomingAuthType: domain.IncomingAuthTypeNone,
			Status:           domain.NodeStatusEnabled,
			TimeoutMs:        5000,
		}
	}
	// Боевая пара команды webhook: два «метода» одного API, различающиеся только
	// именем узла, плюс pull-узел и узел на паузе.
	nodes := []*domain.Node{
		newNode("sbp-qr", domain.RootMethodRequest),
		newNode("sbp-qr-async", domain.RootMethodRequestAsync),
		newNode("sbp-pull", domain.RootMethodRabbitMQAsync),
		newNode("sbp-paused", domain.RootMethodRequest),
	}
	nodes[2].RMQHost = "rabbit.local"
	nodes[2].RMQQueue = "q"
	nodes[2].PullIntervalSec = 10
	nodes[2].PullBatchSize = 10
	nodes[2].PullPrefetch = 10
	for _, n := range nodes {
		require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n), "create node %s", n.Path)
	}
	require.NoError(t, nodeUC.SetStatus(ctx, webuc.SystemActor(), nodes[3].ID, "", domain.NodeStatusPaused))

	// Полный HTTP-стек Receiver'а: тот же роутер и те же middleware, что в бою.
	reader := &fakeReader{repo: nodeRepo}
	producer := &shortURLProducer{}
	m := metrics.New("receiver")
	h := rcvhttp.New(
		rcv.NewRouteUsecase(reader, &httpSenderStub{client: http.DefaultClient}, 5, logger),
		rcv.NewRouteAsyncUsecase(reader, producer, "nexus.async", 5, logger),
		1<<20, m, logger,
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(metrics.GinMiddleware(m))
	h.Register(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	post := func(t *testing.T, path string) (int, string) {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{"q":1}`))
		require.NoError(t, err)
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	t.Run("sync-узел по короткому адресу", func(t *testing.T) {
		code, body := post(t, "/api/v1/sbp-qr")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, `{"ok":true}`, body, "ответ получателя обязан вернуться клиенту")
		assert.Contains(t, gotUpstreamPaths, "/upstream/sbp-qr")
	})

	t.Run("короткий адрес со слогом команды default", func(t *testing.T) {
		// Вторая интерпретация resolveNode: (team=default, path=sbp-qr).
		code, _ := post(t, "/api/v1/default/sbp-qr")
		require.Equal(t, http.StatusOK, code)
	})

	t.Run("async-узел по короткому адресу уходит в очередь", func(t *testing.T) {
		before := producer.produced
		code, body := post(t, "/api/v1/sbp-qr-async")
		require.Equal(t, http.StatusOK, code)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &parsed))
		assert.Equal(t, true, parsed["result"])
		assert.NotEmpty(t, parsed["id"])
		assert.Equal(t, before+1, producer.produced)
	})

	t.Run("pull-узел по короткому адресу — 404", func(t *testing.T) {
		code, body := post(t, "/api/v1/sbp-pull")
		require.Equal(t, http.StatusNotFound, code)
		assert.Contains(t, body, "node not found")
	})

	t.Run("sync-узел на паузе по короткому адресу — 202 queued", func(t *testing.T) {
		before := producer.produced
		code, body := post(t, "/api/v1/sbp-paused")
		require.Equal(t, http.StatusAccepted, code)
		assert.Contains(t, body, `"queued":true`)
		assert.Equal(t, before+1, producer.produced, "§3.6: запрос обязан уйти в очередь")
	})

	t.Run("legacy-формы адреса продолжают работать", func(t *testing.T) {
		code, _ := post(t, "/api/v1/request/sbp-qr")
		assert.Equal(t, http.StatusOK, code, "legacy sync")

		before := producer.produced
		code, _ = post(t, "/api/v1/requestAsync/sbp-qr-async")
		assert.Equal(t, http.StatusOK, code, "legacy async")
		assert.Equal(t, before+1, producer.produced)

		code, _ = post(t, "/api/v1/request/sbp-qr-async")
		assert.Equal(t, http.StatusNotFound, code,
			"legacy sync-адрес async-узла обязан оставаться 404: метод задан URL и проверяется")
	})

	t.Run("несуществующий узел — 404 без утечки", func(t *testing.T) {
		code, body := post(t, "/api/v1/no-such-node")
		require.Equal(t, http.StatusNotFound, code)
		assert.Contains(t, body, "node not found")
	})

	t.Run("метки метрик не зависят от формы адреса", func(t *testing.T) {
		// Ряды уже накоплены подтестами выше: короткая и legacy форма одного узла
		// обязаны лечь в один ряд (на method="requestAsync" стоит дашборд Kafka).
		families, err := m.Registry().Gather()
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, f := range families {
			if f.GetName() != "nexus_requests_total" {
				continue
			}
			for _, metric := range f.GetMetric() {
				var method, node string
				for _, l := range metric.GetLabel() {
					switch l.GetName() {
					case "method":
						method = l.GetValue()
					case "node":
						node = l.GetValue()
					}
				}
				seen[method+"|"+node] = true
			}
		}
		assert.True(t, seen["request|sbp-qr"], "sync-узел: одна метка на обе формы адреса")
		assert.True(t, seen["requestAsync|sbp-qr-async"], "async-узел: одна метка на обе формы адреса")
		assert.True(t, seen["route|no-such-node"], "404 по короткому адресу метится отдельным рядом")
	})
}
