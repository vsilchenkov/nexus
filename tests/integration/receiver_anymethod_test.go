//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rcv "nexus/internal/receiver/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// TestReceiver_AnyMethod_E2E (§40): узел с incoming=ANY/outgoing=ANY принимает
// запрос любым методом (без 405) и зеркалит метод приёмнику. Заодно проверяет
// сквозной проброс ОТВЕТА внешнего узла клиенту (тело + заголовок). Реальный
// Postgres (testcontainers) + httpSenderStub (реальный HTTP-вызов на mock).
func TestReceiver_AnyMethod_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	var gotMethod, gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("X-Echo", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	// incoming=ANY, outgoing=ANY, + passthrough (§39) — полный прозрачный проксинг.
	n := &domain.Node{
		Path:             "any/echo",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/base",
		IncomingMethod:   domain.HTTPMethodAny,
		OutgoingMethod:   domain.HTTPMethodAny,
		PathPassthrough:  true,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}

	reader := &fakeReader{repo: nodeRepo}
	sender := &httpSenderStub{client: http.DefaultClient}
	routeUC := rcv.NewRouteUsecase(reader, sender, 5, logger)

	do := func(method, nodePath string) (*rcv.RouteOutput, error) {
		return routeUC.Route(ctx, rcv.RouteInput{
			NodePath: nodePath,
			Method:   method,
			Header:   http.Header{"Content-Type": []string{"application/json"}},
			Query:    url.Values{},
			Body:     []byte(`{}`),
			ClientIP: "127.0.0.1",
		})
	}

	t.Run("ANY in принимает PUT, ANY out зеркалит PUT + хвост; ответ проброшен", func(t *testing.T) {
		out, err := do("PUT", "any/echo/v1/x")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", out.StatusCode)
		}
		if gotMethod != "PUT" {
			t.Fatalf("upstream method: %q, want PUT (зеркало)", gotMethod)
		}
		if gotPath != "/base/v1/x" {
			t.Fatalf("upstream path: %q, want /base/v1/x", gotPath)
		}
		if string(out.Body) != `{"ok":true}` {
			t.Fatalf("response body: %q, want {\"ok\":true} (проброс тела ответа)", string(out.Body))
		}
		if out.Headers["X-Echo"] != "1" {
			t.Fatalf("response header X-Echo: %q, want 1 (проброс заголовка ответа)", out.Headers["X-Echo"])
		}
	})

	t.Run("ANY in принимает DELETE и зеркалит", func(t *testing.T) {
		out, err := do("DELETE", "any/echo")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status: %d (DELETE должен приниматься, не 405)", out.StatusCode)
		}
		if gotMethod != "DELETE" {
			t.Fatalf("upstream method: %q, want DELETE", gotMethod)
		}
	})

	t.Run("ANY in принимает GET (не 405)", func(t *testing.T) {
		out, err := do("GET", "any/echo")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status: %d (GET должен приниматься)", out.StatusCode)
		}
		if gotMethod != "GET" {
			t.Fatalf("upstream method: %q, want GET", gotMethod)
		}
	})
}
