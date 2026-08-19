//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rcvhttp "nexus/internal/receiver/adapter/in/http"
	rcv "nexus/internal/receiver/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"
)

// TestReceiver_IncomingAuth_E2E поднимает реальный Receiver HTTP-сервер (Gin)
// + узлы в PG c разными incoming_auth_type. Через httptest.NewServer ходит
// настоящий http.Client с заголовками Authorization: Basic/Bearer и проверяет
// что Receiver возвращает 401 при невалидных кредах и 200 при валидных.
//
// Внешний "upstream" — отдельный httptest.NewServer; Sender stub HTTP-вызов
// делает напрямую (как в TestReceiver_Sync_E2E), чтобы не тащить gRPC-стэк.
//
// Покрывает §3.3 ТЗ (incoming auth: none/basic/token) в полной HTTP-обвязке.
func TestReceiver_IncomingAuth_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)

	// Upstream mock. Считаем число успешных доходов, чтобы убедиться что 401
	// не пропускает запросы дальше.
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	// Узлы.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	nodeUC := webuc.NewNodeUsecase(nodeRepo, nopCache{}, auditUC, uow, teamRepo, nil, nil, time.Minute, 0, defaultTeam, nil, logger)

	makeNode := func(path string, inAuth domain.IncomingAuthType, creds string) *domain.Node {
		n := &domain.Node{
			Path:                    path,
			RootMethod:              domain.RootMethodRequest,
			URLMode:                 domain.URLModeStatic,
			TargetURL:               upstream.URL + "/up",
			AuthType:                domain.AuthTypeNone,
			IncomingAuthType:        inAuth,
			IncomingAuthCredentials: creds,
			Status:                  domain.NodeStatusEnabled,
			ClickHouseTable:         "test.auth_e2e",
			ClickHouseRetentionDays: 30,
			TimeoutMs:               5000,
		}
		require.NoError(t, nodeUC.Create(ctx, webuc.SystemActor(), n))
		return n
	}

	makeNode("auth/none", domain.IncomingAuthTypeNone, "")
	makeNode("auth/basic", domain.IncomingAuthTypeBasic, "alice:s3cret")
	makeNode("auth/token", domain.IncomingAuthTypeToken, "supersecret")

	// Receiver HTTP-сервер.
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	routeUC := rcv.NewRouteUsecase(&fakeReader{repo: nodeRepo}, &httpSenderStub{client: http.DefaultClient}, 5, logger)
	// RouteAsyncUsecase: не используется в этом тесте (нет paused-узлов), но
	// Handler требует non-nil. Producer тоже nil — он будет вызван только если
	// мы попадём в async-path; в тестовых сценариях этого не происходит.
	routeAsyncUC := rcv.NewRouteAsyncUsecase(&fakeReader{repo: nodeRepo}, nopAsyncProducer{}, "test.async", 5, logger)
	rcvhttp.New(routeUC, routeAsyncUC, 5*1024*1024, 5*1024*1024, nil, logger).Register(engine)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	doPost := func(path string, headers map[string]string) (int, []byte) {
		// §18: первый сегмент URL — team_slug (splitTeamSlugAndPath). Узлы
		// созданы с многосегментным path ("auth/none" и т.п.), поэтому без
		// слага "auth" был бы съеден как команда → 404. Префиксуем default-слогом.
		req, err := http.NewRequestWithContext(ctx, "POST",
			srv.URL+"/api/v1/request/"+domain.DefaultTeamSlug+"/"+path, bytes.NewReader([]byte(`{"x":1}`)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	// --- auth_type=none ---
	status, _ := doPost("auth/none", nil)
	require.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 1, atomic.LoadInt32(&upstreamHits))

	// --- auth_type=basic ---
	// (a) без Authorization → 401 (ErrAuthHeaderMissing).
	status, _ = doPost("auth/basic", nil)
	require.Equal(t, http.StatusUnauthorized, status, "basic without header → 401")

	// (b) malformed prefix → 401 (ErrAuthHeaderMalformed).
	status, _ = doPost("auth/basic", map[string]string{"Authorization": "Bearer x"})
	require.Equal(t, http.StatusUnauthorized, status, "wrong scheme → 401")

	// (c) неверный пароль → 401 (ErrUnauthorized).
	bad := base64.StdEncoding.EncodeToString([]byte("alice:wrong"))
	status, _ = doPost("auth/basic", map[string]string{"Authorization": "Basic " + bad})
	require.Equal(t, http.StatusUnauthorized, status, "wrong basic creds → 401")

	// (d) правильные креды → 200.
	good := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	status, _ = doPost("auth/basic", map[string]string{"Authorization": "Basic " + good})
	require.Equal(t, http.StatusOK, status, "correct basic creds → 200")
	require.EqualValues(t, 2, atomic.LoadInt32(&upstreamHits))

	// --- auth_type=token ---
	status, _ = doPost("auth/token", nil)
	require.Equal(t, http.StatusUnauthorized, status, "token without header → 401")

	status, _ = doPost("auth/token", map[string]string{"Authorization": "Basic xxx"})
	require.Equal(t, http.StatusUnauthorized, status, "token wrong scheme → 401")

	status, _ = doPost("auth/token", map[string]string{"Authorization": "Bearer wrong"})
	require.Equal(t, http.StatusUnauthorized, status, "wrong token → 401")

	status, _ = doPost("auth/token", map[string]string{"Authorization": "Bearer supersecret"})
	require.Equal(t, http.StatusOK, status, "correct token → 200")
	require.EqualValues(t, 3, atomic.LoadInt32(&upstreamHits),
		"upstream must receive exactly 3 hits (none/basic/token happy paths)")
}

// nopAsyncProducer — stub для Producer-интерфейса в случаях, когда async-путь
// в тесте не используется, но Handler требует non-nil зависимость.
type nopAsyncProducer struct{}

func (nopAsyncProducer) Produce(_ context.Context, _, _ string, _ []byte, _ map[string]string) error {
	return nil
}
