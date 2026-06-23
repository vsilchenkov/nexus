//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rcv "nexus/internal/receiver/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"
)

// TestReceiver_DynamicAuth_E2E (§41): полный путь Receiver→Sender(stub HTTP)→
// mock-upstream на реальном Postgres. Проверяет умный Bearer (без удвоения),
// пусто→без Authorization, «голый» токен→Bearer и входящий token из query.
func TestReceiver_DynamicAuth_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	var gotAuth, gotQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	mkNode := func(path string, mut func(*domain.Node)) {
		n := &domain.Node{
			Path: path, RootMethod: domain.RootMethodRequest,
			URLMode: domain.URLModeStatic, TargetURL: mock.URL + "/up",
			IncomingAuthType: domain.IncomingAuthTypeNone,
			Status:           domain.NodeStatusEnabled,
		}
		mut(n)
		if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}

	reader := &fakeReader{repo: nodeRepo}
	sender := &httpSenderStub{client: http.DefaultClient}
	routeUC := rcv.NewRouteUsecase(reader, sender, 5, logger)

	route := func(path string, q url.Values) (*rcv.RouteOutput, error) {
		return routeUC.Route(ctx, rcv.RouteInput{
			NodePath: path, Method: "POST", Header: http.Header{},
			Query: q, Body: []byte(`{}`), ClientIP: "127.0.0.1",
		})
	}

	// 1. token_from_request, source=query, field=Bearer, значение уже с "Bearer ".
	mkNode("dyn/token-bearer", func(n *domain.Node) {
		n.AuthType = domain.AuthTypeTokenFromRequest
		n.AuthDynamicSource = domain.AuthDynSourceQuery
		n.AuthDynamicField = "Bearer"
	})
	gotAuth, gotQuery = "", ""
	if out, err := route("dyn/token-bearer", url.Values{"Bearer": {"Bearer eyJ.jwt"}}); err != nil || out.StatusCode != 200 {
		t.Fatalf("token-bearer: err=%v status=%v", err, out)
	}
	if gotAuth != "Bearer eyJ.jwt" {
		t.Fatalf("ожидался один 'Bearer eyJ.jwt' (умный дедуп), got %q", gotAuth)
	}
	if strings.Contains(gotQuery, "Bearer") {
		t.Fatalf("служебный параметр Bearer должен быть вырезан, query=%q", gotQuery)
	}

	// 2. Тот же узел без параметра → без Authorization, 2xx.
	gotAuth = "sentinel"
	if out, err := route("dyn/token-bearer", url.Values{}); err != nil || out.StatusCode != 200 {
		t.Fatalf("token-bearer no-param: err=%v status=%v", err, out)
	}
	if gotAuth != "" {
		t.Fatalf("ожидался пустой Authorization (пусто→без auth), got %q", gotAuth)
	}

	// 3. token_from_request, source=query, «голый» токен → Bearer <jwt>.
	mkNode("dyn/token-raw", func(n *domain.Node) {
		n.AuthType = domain.AuthTypeTokenFromRequest
		n.AuthDynamicSource = domain.AuthDynSourceQuery
		n.AuthDynamicField = "tok"
	})
	gotAuth = ""
	if _, err := route("dyn/token-raw", url.Values{"tok": {"rawjwt"}}); err != nil {
		t.Fatalf("token-raw: %v", err)
	}
	if gotAuth != "Bearer rawjwt" {
		t.Fatalf("ожидался 'Bearer rawjwt', got %q", gotAuth)
	}

	// 4. Входящий token из query-параметра apikey: верный → 200, неверный → 401.
	mkNode("dyn/incoming-token", func(n *domain.Node) {
		n.IncomingAuthType = domain.IncomingAuthTypeToken
		n.IncomingAuthCredentials = "s3cr3t"
		n.IncomingAuthDynamicSource = domain.IncomingAuthSourceQuery
		n.IncomingAuthDynamicField = "apikey"
	})
	if _, err := route("dyn/incoming-token", url.Values{"apikey": {"s3cr3t"}}); err != nil {
		t.Fatalf("incoming valid: %v", err)
	}
	if _, err := route("dyn/incoming-token", url.Values{"apikey": {"wrong"}}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("incoming wrong: want ErrUnauthorized, got %v", err)
	}
}
