//go:build integration

package integration

import (
	"context"
	"io"
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

// TestReceiver_GetToPost_QueryForwarded_E2E (§78.7) — боевой сценарий команды
// webhook: клиент шлёт GET с параметрами, узел настроен отправлять POST.
// Параметры обязаны доехать до получателя в URL.
//
// До §78 связка «входящий GET с непустой query + outgoing_method=POST» не была
// покрыта ничем: тесты подмены метода (§40) шли с пустой query, а тесты query —
// с методом POST. Работающее поведение ничем не защищено от регресса.
func TestReceiver_GetToPost_QueryForwarded_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	var (
		gotMethod      string
		gotQuery       url.Values
		gotBody        []byte
		gotContentType string
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
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

	// Конфигурация боевого узла sbp-qr: принимаем GET, отправляем POST.
	// В target_url — собственный параметр узла: он обязан ужиться с клиентскими.
	n := &domain.Node{
		Path:             "webhook/sbp-qr",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        mock.URL + "/hs/webhook/request/sbp-qr?src=nexus",
		IncomingMethod:   domain.HTTPMethodGET,
		OutgoingMethod:   domain.HTTPMethodPOST,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
	if err := nodeUC.Create(ctx, usecase.SystemActor(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}

	routeUC := rcv.NewRouteUsecase(&fakeReader{repo: nodeRepo}, &httpSenderStub{client: http.DefaultClient}, 5, logger)

	// Параметры боевого вызова + повторяющийся ключ (Add, а не Set).
	in := rcv.RouteInput{
		NodePath: "webhook/sbp-qr",
		Method:   http.MethodGet,
		Header:   http.Header{}, // «голый» GET — Content-Type клиент не прислал
		Query: url.Values{
			"mdOrder":     []string{"e83a0ce3-ce53-7d1d-933e-cbfe044012e9"},
			"operation":   []string{"deposited"},
			"orderNumber": []string{"26073931363921449160534"},
			"status":      []string{"1"},
			"tag":         []string{"a", "b"},
		},
		Body:     nil,
		ClientIP: "127.0.0.1",
	}
	out, err := routeUC.Route(ctx, in)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", out.StatusCode)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("метод получателя: %q, want POST (из настройки узла, а не метод клиента)", gotMethod)
	}
	for k, want := range map[string]string{
		"mdOrder":     "e83a0ce3-ce53-7d1d-933e-cbfe044012e9",
		"operation":   "deposited",
		"orderNumber": "26073931363921449160534",
		"status":      "1",
		"src":         "nexus", // параметр самого узла из target_url
	} {
		if got := gotQuery.Get(k); got != want {
			t.Fatalf("параметр %q у получателя: %q, want %q (query GET обязана доехать до POST)", k, got, want)
		}
	}
	if got := gotQuery["tag"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("повторяющийся ключ tag: %v, want [a b] (параметры складываются, а не затираются)", got)
	}
	if len(gotBody) != 0 {
		t.Fatalf("тело POST: %q, want пустое — тело исходящего равно телу входящего, query в тело не переносится (§78.7)", gotBody)
	}
	// Известное ограничение §78.7, зафиксированное намеренно: Content-Type берётся
	// из входящего запроса, поэтому голый GET даёт исходящий POST без него.
	if gotContentType != "" {
		t.Fatalf("Content-Type у получателя: %q, want пустой — узлу нечем его задать (§78.7)", gotContentType)
	}
}
