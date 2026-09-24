//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/circuitbreaker"
	"nexus/internal/platform/config"
	"nexus/internal/sender/adapter/out/httpclient"
	"nexus/internal/sender/adapter/out/nodepg"
	senderuc "nexus/internal/sender/usecase"
)

// TestCircuitBreaker_IgnoreTimeouts_E2E — §107.3 сквозь настоящие слои Sender'а:
// флаг узла прочитан из PostgreSQL его собственной выборкой (nodepg), вызов идёт
// настоящим httpclient'ом (метка таймаута по GotConn), исход учитывает настоящий
// breaker в Redis.
//
// Получатель принимает соединение и молчит — медленный MCP-сервер. С флагом
// таймауты сверх порога breaker не открывают; без флага открывают, как прежде;
// мёртвый адрес открывает breaker и с флагом.
func TestCircuitBreaker_IgnoreTimeouts_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fx, pgCleanup := newIdentityFixture(t, ctx)
	defer pgCleanup()
	rdb, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(silent.Close)

	team := fx.mkTeam("mcp")
	mk := func(path, target string) *domain.Node {
		return fx.mkNode(&domain.Node{
			Path: path, RootMethod: domain.RootMethodRequest,
			IncomingMethod: domain.HTTPMethodAny, OutgoingMethod: domain.HTTPMethodAny,
			URLMode: domain.URLModeStatic, TargetURL: target, TeamID: team.ID,
			CircuitBreakerThreshold: 2, BreakerIgnoreTimeouts: true,
		})
	}
	slow := mk("mcp-slow", silent.URL+"/mcp")
	dead := mk("mcp-dead", "http://127.0.0.1:1/mcp")

	reader := nodepg.New(fx.pool, fx.cipher, fx.logger)
	cb := circuitbreaker.New(rdb, 5, time.Minute)
	httpc := httpclient.New(&config.SenderHTTPClientConfig{
		TimeoutMs: 2000, MaxIdleConns: 10, MaxIdleConnsPerHost: 5,
		IdleConnTimeoutSec: 30, DialTimeoutMs: 1000, TLSHandshakeTimeoutMs: 1000,
	}, fx.logger, 0)
	uc := senderuc.NewSendUsecase(httpc, scenarioLogWriter{}, cb, fx.logger, 64<<20)

	// Как async-путь и повтор из DLQ: узел из PG на каждый вызов.
	send := func(id string, times int) {
		t.Helper()
		n, err := reader.GetByID(ctx, id)
		require.NoError(t, err)
		for range times {
			out := uc.Send(ctx, senderuc.SendInput{
				ID: "req-" + id, NodeID: n.ID, NodePath: n.Path, RootMethod: domain.RootMethodRequest,
				TargetURL: n.TargetURL, Method: http.MethodGet, TimeoutMs: 200,
				BreakerThreshold:      n.CircuitBreakerThreshold,
				BreakerCooldownSec:    n.CircuitBreakerCooldownSec,
				BreakerIgnoreTimeouts: n.BreakerIgnoreTimeouts,
			})
			require.NotEqual(t, http.StatusOK, int(out.StatusCode))
		}
	}
	state := func(n *domain.Node) circuitbreaker.State {
		t.Helper()
		st, err := cb.State(ctx, circuitbreaker.NodeKey(n.ID))
		require.NoError(t, err)
		return st
	}

	send(slow.ID, 4)
	assert.Equal(t, circuitbreaker.StateClosed, state(slow),
		"4 таймаута ответа при пороге 2 — защита закрыта по флагу узла")

	_, err := fx.pool.Exec(ctx, `UPDATE nodes SET breaker_ignore_timeouts = false WHERE id = $1`, slow.ID)
	require.NoError(t, err)
	send(slow.ID, 2)
	assert.Equal(t, circuitbreaker.StateOpen, state(slow), "без флага таймауты открывают защиту, как до §107")

	send(dead.ID, 2)
	assert.Equal(t, circuitbreaker.StateOpen, state(dead), "мёртвый адрес открывает защиту и с флагом")
}
