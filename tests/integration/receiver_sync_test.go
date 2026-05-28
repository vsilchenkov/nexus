//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	senderv1 "nexus/proto/sender/v1"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase"

	rcv "nexus/internal/receiver/usecase"
)

// TestReceiver_Sync_E2E: создаём узел через NodeUsecase (реальный Postgres
// через testcontainers), затем зовём RouteUsecase.Route с inline-stub'ом
// SenderClient, который сам делает HTTP-вызов на внешний mock-сервер.
// Проверяем, что mock получил запрос с правильным URL и заголовком.
func TestReceiver_Sync_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	// 1. Mock «внешний узел».
	var gotPath string
	var gotBody []byte
	var gotAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	// 2. Создаём узел.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := usecase.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := usecase.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, time.Minute, 0, defaultTeam, logger)

	n := &domain.Node{
		Path:                    "demo/sync",
		RootMethod:              domain.RootMethodRequest,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               mock.URL + "/upstream",
		AuthType:                domain.AuthTypeToken,
		AuthCredentials:         "supersecret",
		IncomingAuthType:        domain.IncomingAuthTypeNone,
		Status:                  domain.NodeStatusEnabled,
		ClickHouseTable:         "test.demo",
		ClickHouseRetentionDays: 30,
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}

	// 3. NodeReader для receiver — простой адаптер поверх web-репозитория.
	reader := &fakeReader{repo: nodeRepo}

	// 4. SenderClient stub — реально делает HTTP-вызов.
	sender := &httpSenderStub{client: http.DefaultClient}

	routeUC := rcv.NewRouteUsecase(reader, sender, logger)

	// 5. Sync-запрос.
	out, err := routeUC.Route(ctx, rcv.RouteInput{
		NodePath: "demo/sync",
		Method:   "POST",
		Header:   http.Header{"Content-Type": []string{"application/json"}},
		Query:    url.Values{},
		Body:     []byte(`{"hello":"world"}`),
		ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", out.StatusCode)
	}

	// 6. Verify.
	if gotPath != "/upstream" {
		t.Fatalf("upstream path: %q", gotPath)
	}
	if got := string(gotBody); got != `{"hello":"world"}` {
		t.Fatalf("upstream body: %q", got)
	}
	if gotAuth != "Bearer supersecret" {
		t.Fatalf("upstream auth: %q", gotAuth)
	}
}

// fakeReader — соединяет web-port NodeRepo с receiver-port NodeReader.
// teamSlug в Get игнорируется: интеграционные тесты pre-Phase 10 знают
// path как глобально уникальный, узлы создаются в default-team.
type fakeReader struct {
	repo interface {
		GetByPath(ctx context.Context, path string) (*domain.Node, error)
	}
}

func (r *fakeReader) Get(ctx context.Context, _ string, path string) (*domain.Node, error) {
	return r.repo.GetByPath(ctx, path)
}

// httpSenderStub — делает HTTP-вызов вместо gRPC, имитируя Sender Service.
type httpSenderStub struct {
	client *http.Client
}

func (s *httpSenderStub) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.TargetUrl, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if auth := req.GetAuth().GetAuthorizationHeader(); auth != "" {
		httpReq.Header.Set("Authorization", auth)
	}
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	headers := make(map[string]string, len(resp.Header))
	for k, vv := range resp.Header {
		if len(vv) > 0 {
			headers[k] = vv[0]
		}
	}
	return &senderv1.SendResponse{
		StatusCode: int32(resp.StatusCode),
		Body:       body,
		Headers:    headers,
	}, nil
}
