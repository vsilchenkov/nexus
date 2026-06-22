//go:build integration

package integration

import (
	"context"
	"errors"
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

// TestReceiver_PathPassthrough_Sync_E2E (§39): узел с включённым path_passthrough
// приклеивает хвост входящего пути к Target URL; без флага — точный матч и 404;
// точный узел приоритетнее префиксного. Полный путь через реальный Postgres.
func TestReceiver_PathPassthrough_Sync_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
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

	mustCreate := func(n *domain.Node) {
		t.Helper()
		if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
			t.Fatalf("create node %s: %v", n.Path, err)
		}
	}

	// Узел с passthrough.
	mustCreate(&domain.Node{
		Path:             "demo/svc",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/api",
		PathPassthrough:  true,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	})
	// Узел без passthrough.
	mustCreate(&domain.Node{
		Path:             "demo/exact",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/exact",
		PathPassthrough:  false,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	})
	// Точный узел-потомок: проверка, что exact приоритетнее prefix.
	mustCreate(&domain.Node{
		Path:             "demo/svc/user",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/exact-user",
		PathPassthrough:  false,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	})

	reader := &fakeReader{repo: nodeRepo}
	sender := &httpSenderStub{client: http.DefaultClient}
	routeUC := rcv.NewRouteUsecase(reader, sender, 5, logger)

	do := func(nodePath string) (*rcv.RouteOutput, error) {
		return routeUC.Route(ctx, rcv.RouteInput{
			NodePath: nodePath,
			Method:   "POST",
			Header:   http.Header{"Content-Type": []string{"application/json"}},
			Query:    url.Values{},
			Body:     []byte(`{}`),
			ClientIP: "127.0.0.1",
		})
	}

	t.Run("passthrough on: хвост приклеен к target", func(t *testing.T) {
		gotPath = ""
		out, err := do("demo/svc/v1/GetParcelsInfo")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", out.StatusCode)
		}
		if gotPath != "/api/v1/GetParcelsInfo" {
			t.Fatalf("upstream path: %q, want /api/v1/GetParcelsInfo", gotPath)
		}
	})

	t.Run("passthrough off: лишний хвост → 404", func(t *testing.T) {
		_, err := do("demo/exact/extra")
		if !errors.Is(err, domain.ErrNodeNotFound) {
			t.Fatalf("want ErrNodeNotFound, got %v", err)
		}
	})

	t.Run("exact узел приоритетнее prefix-passthrough", func(t *testing.T) {
		gotPath = ""
		out, err := do("demo/svc/user")
		if err != nil {
			t.Fatalf("route: %v", err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", out.StatusCode)
		}
		if gotPath != "/exact-user" {
			t.Fatalf("upstream path: %q, want /exact-user (точный узел, без приклеивания)", gotPath)
		}
	})
}
