//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rhttp "nexus/internal/receiver/adapter/in/http"
	rnodecache "nexus/internal/receiver/adapter/out/nodecache"
	rejectlog "nexus/internal/receiver/adapter/out/rejectlog"
	rcv "nexus/internal/receiver/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// sseRejectSink — журнал отказов §94 для теста: 405 по настройке узла в него
// попадать не должен.
type sseRejectSink struct{ n atomic.Int32 }

func (s *sseRejectSink) Add(rejectlog.Sample) { s.n.Add(1) }

// mcpLikeUpstream — MCP-сервер в миниатюре: GET с Accept: text/event-stream
// открывает бесконечный поток (пинги до отключения клиента), POST отвечает
// JSON. Счётчик GET показывает, доходила ли подписка до получателя.
func mcpLikeUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{}}`))
			return
		}
		gets.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

// TestReceiver_SSEDecline_E2E — §107.2 сквозь настоящие слои: флаг узла
// записан Web'ом в PostgreSQL, прочитан кешем Receiver'а (его собственная
// выборка, промах Redis после инвалидации §57) и применён обработчиком.
//
// Без флага подписка висит до таймаута отправки — боевой симптом команды mcp.
// С флагом клиент получает 405 за доли секунды, получатель подписку не видит,
// журнал отказов молчит, а вызов инструмента (POST) проходит как прежде.
func TestReceiver_SSEDecline_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	rdb, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()
	upstream, gets := mcpLikeUpstream(t)

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, pgrepo.NewUnitOfWorkPg(pool, cipher, logger),
		pgrepo.NewTeamRepoPg(pool, logger), nil, nil, time.Minute, 0, defaultTeam, nil, logger)
	node := &domain.Node{
		Path:           "mcp-1c-vika_dev5",
		RootMethod:     domain.RootMethodRequest,
		IncomingMethod: domain.HTTPMethodAny,
		OutgoingMethod: domain.HTTPMethodAny,
		URLMode:        domain.URLModeStatic,
		TargetURL:      upstream.URL + "/mcp",
		Status:         domain.NodeStatusEnabled,
	}
	require.NoError(t, nodeUC.Create(ctx, usecase.SystemActor(), node))

	// Короткий таймаут клиента отправки — вместо timeout_ms узла (на бою 300 с).
	reader := rnodecache.New(rdb, pool, cipher, time.Minute, logger)
	sender := &httpSenderStub{client: &http.Client{Timeout: 700 * time.Millisecond}}
	route := rcv.NewRouteUsecase(reader, sender, 5, logger)
	h := rhttp.New(route, nil, rcv.NewBodyLimitsProvider(0, 0, logger), nil, logger)
	sink := &sseRejectSink{}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(rhttp.RejectLogMiddleware(sink, nil))
	h.Register(engine)

	do := func(method string) (*httptest.ResponseRecorder, time.Duration) {
		req := httptest.NewRequest(method, "/api/v1/request/"+node.Path, strings.NewReader(""))
		req.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		start := time.Now()
		engine.ServeHTTP(w, req)
		return w, time.Since(start)
	}

	t.Run("flag off: subscription hangs until send timeout", func(t *testing.T) {
		w, took := do(http.MethodGet)
		assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code)
		assert.GreaterOrEqual(t, took, 700*time.Millisecond, "поток держал ответ до таймаута")
		assert.Equal(t, int32(1), gets.Load(), "подписка дошла до получателя")
	})

	// Включение флага в Web и инвалидация §57 — ровно боевая последовательность.
	stored, err := nodeRepo.Get(ctx, node.ID)
	require.NoError(t, err)
	stored.SSEDecline = true
	require.NoError(t, nodeUC.Update(ctx, usecase.SystemActor(), stored, defaultTeam))
	require.NoError(t, reader.Invalidate(ctx, domain.DefaultTeamSlug, node.Path))
	sinkBefore := sink.n.Load()

	t.Run("flag on: 405 at once, upstream untouched", func(t *testing.T) {
		w, took := do(http.MethodGet)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
		assert.Less(t, took, time.Second)
		assert.Equal(t, "POST, PUT, PATCH, DELETE, HEAD, OPTIONS", w.Header().Get("Allow"))
		assert.JSONEq(t, `{"error":"event stream is not supported by this node"}`, w.Body.String())
		assert.Equal(t, int32(1), gets.Load(), "новая подписка до получателя не дошла")
		assert.Equal(t, sinkBefore, sink.n.Load(), "405 по настройке узла — не отказ шины (§94)")
	})

	t.Run("flag on: tool call still reaches upstream", func(t *testing.T) {
		w, _ := do(http.MethodPost)
		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"jsonrpc":"2.0","result":{}}`, w.Body.String())
	})
}
