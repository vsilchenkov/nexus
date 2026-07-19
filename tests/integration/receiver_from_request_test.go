//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webuc "nexus/internal/web/usecase"

	rcv "nexus/internal/receiver/usecase"
)

// TestReceiver_FromRequest_Allowlist_E2E — сквозное покрытие url_mode=from_request
// (§3.4) через реальный каталог разрешённых хостов (§23) и реальный Postgres:
//
//	catalog.Create → Attach (пересборка снимка nodes.url_allowed_hosts) →
//	Receiver.Route читает снимок и применяет allowlist.
//
// Что раньше не покрывалось: существующие тесты проверяли ЛИБО матчер в изоляции
// (domain.HostAllowed), ЛИБО пересборку снимка (SnapshotRebuild), но не то, что
// Receiver end-to-end пускает разрешённый хост к внешнему узлу и блокирует
// остальные ДО вызова Sender, читая снимок, собранный usecase'ом каталога.
//
// Запрещённые хосты используют TLD .invalid (RFC 2606, гарантированно не
// резолвится): блокировка allowlist'ом происходит в ResolveURL до сетевого
// вызова, поэтому реального DNS-обращения быть не должно вовсе.
func TestReceiver_FromRequest_Allowlist_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, _ := crypto.NewCipher(testEncryptionKey)
	logger := logging.NewNoop()

	// 1. Mock «разрешённого внешнего узла» (127.0.0.1 — попадёт в allowlist).
	//    Счётчик hits доказывает, что запрещённые хосты не доходят до Sender.
	var hits int32
	var gotPath string
	var gotBody []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	allowedHost := mustHost(t, mock.URL) // "127.0.0.1"

	// 2. Проводка web-слоя: узел + каталог хостов + usecase каталога.
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	hostRepo := pgrepo.NewHostAllowlistRepoPg(pool, logger)
	uow := pgrepo.NewUnitOfWorkPg(pool, cipher, logger)
	auditUC := webuc.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logger), logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)
	hostUC := webuc.NewHostAllowlistUsecase(hostRepo, nodeRepo, nopCache{}, teamRepo, uow, auditUC, time.Minute, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	node := makeFromRequestNode(t, ctx, nodeRepo, teamID, "svc/from-request")

	// Свежесозданный from_request-узел стартует с пустым снимком (§23.3:
	// «Create стартует с пустым allowlist»).
	if snap, _ := nodeRepo.Get(ctx, node.ID); len(snap.URLAllowedHosts) != 0 {
		t.Fatalf("новый узел должен стартовать с пустым allowlist, got %v", snap.URLAllowedHosts)
	}

	reader := &fakeReader{repo: nodeRepo}
	sender := &httpSenderStub{client: http.DefaultClient}
	routeUC := rcv.NewRouteUsecase(reader, sender, 5, logger)

	// 3. Наполняем каталог и привязываем exact-паттерн разрешённого хоста —
	//    usecase пересобирает снимок nodes.url_allowed_hosts.
	entry := &domain.HostAllowlistEntry{Pattern: allowedHost, Kind: domain.HostKindExact}
	if err := hostUC.Create(ctx, webuc.SystemActor(), entry); err != nil {
		t.Fatalf("create host entry: %v", err)
	}
	if err := hostUC.Attach(ctx, webuc.SystemActor(), node.ID, entry.ID, teamID); err != nil {
		t.Fatalf("attach host: %v", err)
	}
	if snap, _ := nodeRepo.Get(ctx, node.ID); len(snap.URLAllowedHosts) != 1 || snap.URLAllowedHosts[0] != allowedHost {
		t.Fatalf("snapshot = %v, want [%s]", snap.URLAllowedHosts, allowedHost)
	}

	// 4. Разрешённый хост из query — доходит до внешнего узла с path-passthrough
	//    из url_base и вырезанным служебным параметром.
	out, err := routeUC.Route(ctx, fromRequestInput("svc/from-request", mock.URL+"/hook"))
	if err != nil {
		t.Fatalf("разрешённый хост должен пройти: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", out.StatusCode)
	}
	if gotPath != "/hook" {
		t.Fatalf("upstream path = %q, want /hook (URL из запроса)", gotPath)
	}
	if string(gotBody) != `{"hello":"world"}` {
		t.Fatalf("upstream body = %q", string(gotBody))
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("разрешённый хост: hits = %d, want 1", got)
	}

	// 5. Хост вне allowlist — 403-эквивалент (ErrURLNotAllowed), в Sender не
	//    уходит (счётчик hits не растёт — блокировка в ResolveURL до сети).
	_, err = routeUC.Route(ctx, fromRequestInput("svc/from-request", "https://evil.invalid/hook"))
	if !errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("запрещённый хост: want ErrURLNotAllowed, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("запрещённый хост не должен дойти до Sender: hits = %d, want 1", got)
	}

	// 6. Отсутствие url_base — 400-эквивалент (ErrURLParamRequired), без fallback
	//    на target_url и без вызова Sender.
	_, err = routeUC.Route(ctx, rcv.RouteInput{
		NodePath: "svc/from-request",
		Method:   "POST",
		Header:   http.Header{"Content-Type": []string{"application/json"}},
		Query:    url.Values{},
		Body:     []byte(`{}`),
		ClientIP: "127.0.0.1",
	})
	if !errors.Is(err, domain.ErrURLParamRequired) {
		t.Fatalf("без url_base: want ErrURLParamRequired, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("запрос без url_base не должен дойти до Sender: hits = %d, want 1", got)
	}

	// 7. Detach снимает разрешение: снимок снова пуст ⇒ allowlist пуст ⇒
	//    «разрешено всё» (§3.4). Тот же ранее запрещённый хост больше НЕ режется
	//    allowlist'ом — он проходит проверку и падает уже на сетевом слое
	//    (evil.invalid не резолвится), т.е. ошибка не ErrURLNotAllowed.
	if err := hostUC.Detach(ctx, webuc.SystemActor(), node.ID, entry.ID, teamID); err != nil {
		t.Fatalf("detach host: %v", err)
	}
	if snap, _ := nodeRepo.Get(ctx, node.ID); len(snap.URLAllowedHosts) != 0 {
		t.Fatalf("snapshot после detach = %v, want []", snap.URLAllowedHosts)
	}
	_, err = routeUC.Route(ctx, fromRequestInput("svc/from-request", "https://evil.invalid/hook"))
	if err == nil || errors.Is(err, domain.ErrURLNotAllowed) {
		t.Fatalf("после detach allowlist пуст ⇒ хост не должен блокироваться allowlist'ом; got %v", err)
	}
}

// fromRequestInput — sync RouteInput с url_base, указывающим на переданный URL.
func fromRequestInput(nodePath, urlBase string) rcv.RouteInput {
	return rcv.RouteInput{
		NodePath: nodePath,
		Method:   "POST",
		Header:   http.Header{"Content-Type": []string{"application/json"}},
		Query:    url.Values{"url_base": {urlBase}},
		Body:     []byte(`{"hello":"world"}`),
		ClientIP: "127.0.0.1",
	}
}

// mustHost извлекает hostname (без порта) из URL для exact-паттерна каталога.
func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse mock url %q: %v", raw, err)
	}
	if h := u.Hostname(); h != "" {
		return h
	}
	// Фолбэк на случай нестандартного формата.
	host := u.Host
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}
