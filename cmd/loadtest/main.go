// Loadtest — сценарный тест производительности (§10.2 ТЗ).
//
// Что делает:
//  1. Поднимает mock-сервер внешних узлов (HTTP).
//  2. Логинится в Web (POST /api/auth/login).
//  3. Создаёт N узлов через POST /api/nodes (с разными url_mode и auth_type).
//  4. Гонит target_rps в течение duration в Receiver (/api/v1/request/*).
//  5. Считает p50/p95/p99, error rate; печатает отчёт.
//  6. Сохраняет JSON-отчёт в --report и выходит с кодом 1 при нарушении
//     критериев приёма (rps >= 95% target, p95 <= 200ms, error rate < 0.1%).
//
// В Phase 4 — базовая реализация: только static-URL + auth=none.
// Расширение под все комбинации url_mode/auth_type — последующая итерация.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"time"

	extlog "github.com/vsilchenkov/logging"

	"nexus/internal/platform/safego"
)

type flags struct {
	WebURL        string
	ReceiverURL   string
	TeamSlug      string
	AdminLogin    string
	AdminPass     string
	TargetRPS     int
	Duration      time.Duration
	Nodes         int
	PayloadMin    int
	PayloadMax    int
	MockLatency   time.Duration
	MockBind      string
	MockPublicURL string
	Cleanup       bool
	Report        string
	RatioRMQ      float64 // §27.12: доля узлов RabbitMQAsync
	RMQURL        string  // amqp://user:pass@host:port/vhost для RMQ-нагрузки

	// §10.2: микс трафика. Доли — независимые корзины узлов (см. planNodes),
	// сумма ≤ 1, остаток — plain-sync. Дефолт 0 сохраняет старое поведение
	// (`make loadtest` без флагов = чистый sync-smoke); микс задаётся профилем.
	RatioAsync      float64
	RatioDynamicURL float64
	RatioAuthToken  float64
	RatioAuthBasic  float64
	MockJitter      time.Duration
	RandomHeaders   bool

	// §10.2: no-loss проверка через ClickHouse (async + rmq). Пустой CHAddr =
	// проверка пропускается (локальный `make loadtest` без CH работает как раньше).
	CHAddr          string
	CHUser          string
	CHPassword      string
	CHTable         string
	CHFlushGrace    time.Duration
	CHNoLossMaxWait time.Duration
}

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.WebURL, "web", "http://localhost:8000", "Web service base URL")
	flag.StringVar(&f.ReceiverURL, "receiver", "http://localhost:8080", "Receiver base URL")
	flag.StringVar(&f.TeamSlug, "team-slug", "default",
		"Team slug used to address nodes in the request URL: /api/v1/request/<team_slug>/<node_path>. "+
			"Must match the team the nodes are created in (Phase 10.E.1 multi-tenancy). Узлы с "+
			"многосегментным path (loadtest/node-…) недостижимы по legacy-URL без слога — первый "+
			"сегмент трактуется как team_slug.")
	flag.StringVar(&f.AdminLogin, "admin-login", "admin", "Admin login")
	flag.StringVar(&f.AdminPass, "admin-password", "", "Admin password (required)")
	flag.IntVar(&f.TargetRPS, "target-rps", 500, "Target RPS")
	flag.DurationVar(&f.Duration, "duration", 10*time.Minute, "Duration of load")
	flag.IntVar(&f.Nodes, "nodes", 50, "Number of nodes to create")
	flag.IntVar(&f.PayloadMin, "payload-min", 100, "Min payload size (bytes)")
	flag.IntVar(&f.PayloadMax, "payload-max", 5120, "Max payload size")
	flag.DurationVar(&f.MockLatency, "mock-latency", 50*time.Millisecond, "Mock server response latency")
	flag.StringVar(&f.MockBind, "mock-bind", "127.0.0.1:0",
		"Mock server bind address (host:port). Use 0.0.0.0:<port> when running inside docker/compose so Receiver/Sender can reach the mock from neighbour containers.")
	flag.StringVar(&f.MockPublicURL, "mock-public-url", "",
		"Public base URL of the mock server as seen by Receiver/Sender (e.g. http://loadtest:9999). If empty, the listener URL is used — works only when loadtest, Receiver and Sender share the same network namespace.")
	flag.BoolVar(&f.Cleanup, "cleanup", true, "Delete created nodes after test")
	flag.StringVar(&f.Report, "report", "report.json", "Report file path")
	flag.Float64Var(&f.RatioRMQ, "ratio-rmq", 0,
		"§27: fraction of nodes created as RabbitMQAsync (0..1). Requires --rmq-url.")
	flag.StringVar(&f.RMQURL, "rmq-url", "",
		"AMQP URL for RabbitMQAsync load (amqp://user:pass@host:port/vhost). Queues are declared and published to during the run.")
	flag.Float64Var(&f.RatioAsync, "ratio-async", 0,
		"§10.2: fraction of nodes created as requestAsync (Kafka path). 0..1.")
	flag.Float64Var(&f.RatioDynamicURL, "ratio-dynamic-url", 0,
		"§10.2: fraction of nodes with url_mode=from_request (target passed via ?url_base=). 0..1.")
	flag.Float64Var(&f.RatioAuthToken, "ratio-auth-token", 0,
		"§10.2: fraction of nodes with auth_type=token_from_request (Bearer header). 0..1.")
	flag.Float64Var(&f.RatioAuthBasic, "ratio-auth-basic", 0,
		"§10.2: fraction of nodes with auth_type=basic_from_request (Basic header). 0..1.")
	flag.DurationVar(&f.MockJitter, "mock-latency-jitter", 30*time.Millisecond,
		"Mock server response latency jitter (±). §10.2.")
	flag.BoolVar(&f.RandomHeaders, "random-headers", true,
		"Send 1–3 random X-Lt-* headers per request (§10.2 header proxying/masking).")
	flag.StringVar(&f.CHAddr, "ch-addr", "",
		"ClickHouse native addr host:port for §10.2 no-loss check (e.g. clickhouse:9000). Empty = skip.")
	flag.StringVar(&f.CHUser, "ch-user", "default", "ClickHouse user for no-loss check")
	flag.StringVar(&f.CHPassword, "ch-password", "", "ClickHouse password for no-loss check")
	flag.StringVar(&f.CHTable, "ch-table", "nexus_default.loadtest",
		"ClickHouse log table for created nodes and the no-loss check; "+
			"on an instance with instance.id use nexus_<id>_default.loadtest (§70.2)")
	flag.DurationVar(&f.CHFlushGrace, "ch-flush-grace", 10*time.Second,
		"Poll interval between CH row counts (also the initial settle before the first count; sender batch flush window)")
	flag.DurationVar(&f.CHNoLossMaxWait, "ch-noloss-max-wait", 120*time.Second,
		"Max total wait for the async/rmq backlog to drain before declaring loss (§10.2). "+
			"The check polls every --ch-flush-grace and exits early once rows>=expected (no loss) "+
			"or the count plateaus below expected (real loss).")
	flag.Parse()
	return f
}

func main() {
	defer safego.Recover(extlog.NewLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))), "loadtest.main")

	f := parseFlags()
	if f.AdminPass == "" {
		fmt.Fprintln(os.Stderr, "--admin-password is required")
		os.Exit(1)
	}

	mock, err := startMockServer(f.MockBind, f.MockLatency, f.MockJitter)
	if err != nil {
		fail("start mock server: %v", err)
	}
	defer mock.Close()
	targetURL := f.MockPublicURL
	if targetURL == "" {
		targetURL = mock.URL
	}
	fmt.Printf("mock server: listen=%s target=%s\n", mock.URL, targetURL)

	ctx := context.Background()
	// Keep-alive-пул: при 200 воркерах дефолтный Transport (MaxIdleConnsPerHost=2)
	// не переиспользует соединения → шквал новых TCP → исчерпание эфемерных
	// портов (особенно на Windows: ~16k портов, TIME_WAIT 120с). Явный Transport
	// с большим пулом держит соединения открытыми. Тело ответа в doRequest
	// дочитывается до EOF перед Close — иначе соединение не вернётся в пул.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 1024
	tr.MaxIdleConnsPerHost = 1024
	tr.IdleConnTimeout = 90 * time.Second
	client := &client{baseWeb: f.WebURL, baseRecv: f.ReceiverURL,
		hc:      &http.Client{Timeout: 30 * time.Second, Transport: tr},
		chTable: f.CHTable}

	if err := client.login(ctx, f.AdminLogin, f.AdminPass); err != nil {
		fail("login: %v", err)
	}
	fmt.Println("logged in OK")

	// §10.2: создаём CH-таблицу логов из канонной схемы ДО старта трафика —
	// иначе первый batch INSERT Sender'а упадёт. Пустой --ch-addr = пропуск.
	if err := ensureCHTable(ctx, f); err != nil {
		fail("ensure clickhouse table: %v", err)
	}

	// §27.12: часть узлов — RabbitMQAsync (нагрузка публикуется в их очереди).
	httpNodes := f.Nodes
	rmqNodes := 0
	if f.RatioRMQ > 0 && f.RMQURL != "" {
		rmqNodes = int(float64(f.Nodes)*f.RatioRMQ + 0.5)
		httpNodes = f.Nodes - rmqNodes
	} else if f.RatioRMQ > 0 {
		fmt.Fprintln(os.Stderr, "--ratio-rmq requires --rmq-url; ignoring RMQ load")
	}

	modes := planNodes(httpNodes, f.Nodes, f)
	nodes, err := client.createNodes(ctx, modes, targetURL)
	if err != nil {
		fail("create nodes: %v", err)
	}
	fmt.Printf("created %d http nodes (mix: %v)\n", len(nodes), modeCounts(modes))

	if f.Cleanup {
		defer client.deleteNodes(context.Background(), nodePaths(nodes))
	}

	// RMQ-нагрузка идёт параллельно HTTP-нагрузке.
	rmqLoad := startRMQLoad(ctx, client, f, targetURL, rmqNodes)
	defer rmqLoad.stop()

	report := runLoad(ctx, client, nodes, f)

	// §10.2 no-loss: сверяем async/rmq-строки в CH-логе с числом отправленных.
	// rmqLoad.stop() (deferred) дождётся завершения publisher'а; здесь его
	// счётчик уже финален (publisher живёт ту же f.Duration, что и runLoad).
	var asyncSent int64
	if m := report.Modes[string(modeAsync)]; m != nil {
		asyncSent = m.Sent
	}
	report.applyNoLoss(checkNoLoss(ctx, f, asyncSent+rmqLoad.published.Load()))

	report.print()
	rmqLoad.report()
	if err := report.save(f.Report); err != nil {
		fmt.Fprintf(os.Stderr, "save report: %v\n", err)
	}
	if !report.passed(f.TargetRPS) {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// ----- mock server ---------------------------------------------------------

func startMockServer(bind string, latency, jitter time.Duration) (*httptest.Server, error) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Задержка = latency ± uniform(jitter). Имитирует реальный внешний API.
		d := latency
		if jitter > 0 {
			d += time.Duration(rand.Int63n(int64(2*jitter))) - jitter
		}
		if d > 0 {
			time.Sleep(d)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"ok":true}`)
	})
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("listen %q: %w", bind, err)
	}
	srv := httptest.NewUnstartedServer(handler)
	// httptest по умолчанию создаёт собственный listener на 127.0.0.1 —
	// заменяем нашим, чтобы можно было биндить на 0.0.0.0 (контейнер) и/или
	// фиксированный порт.
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	return srv, nil
}

// ----- web client ----------------------------------------------------------

type client struct {
	baseWeb  string
	baseRecv string
	hc       *http.Client
	cookie   string
	// chTable — имя лог-таблицы создаваемых узлов. Берётся из --ch-table
	// (§70.2: на ноде с идентификатором БД называется nexus_<id>_default, и
	// прежний хардкод "nexus_default.loadtest" вёл бы в чужую или несуществующую
	// базу). Тот же флаг использует проверка no-loss и ensureCHTable.
	chTable string
}

func (c *client) login(ctx context.Context, login, password string) error {
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseWeb+"/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("login status %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "nexus_session" {
			c.cookie = ck.Value
			return nil
		}
	}
	return fmt.Errorf("session cookie not set")
}

func (c *client) deleteNodes(ctx context.Context, paths []string) {
	// Узлы удаляются через /api/nodes/:id, но у нас нет id — пропускаем.
	// Реальная очистка — отдельная задача (использовать GET список + DELETE).
	_ = paths
}

// ----- load ----------------------------------------------------------------

// modeAccum — накопитель по одному режиму узла. Под одним мьютексом result.mu.
type modeAccum struct {
	latencies []time.Duration
	sent      int64
	errors    int64
}

type result struct {
	mu    sync.Mutex
	modes map[nodeMode]*modeAccum
}

func newResult() *result {
	return &result{modes: make(map[nodeMode]*modeAccum)}
}

func (r *result) add(d time.Duration, errored bool, mode nodeMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.modes[mode]
	if a == nil {
		a = &modeAccum{}
		r.modes[mode] = a
	}
	a.latencies = append(a.latencies, d)
	a.sent++
	if errored {
		a.errors++
	}
}

// modeReport — per-mode срез отчёта (§10.2: реалистичный микс трафика).
type modeReport struct {
	Sent      int64   `json:"sent"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

type report struct {
	Sent        int64                  `json:"sent"`
	Errors      int64                  `json:"errors"`
	ErrorRate   float64                `json:"error_rate"`
	AchievedRPS float64                `json:"achieved_rps"`
	P50Ms       float64                `json:"p50_ms"`
	P95Ms       float64                `json:"p95_ms"`
	P99Ms       float64                `json:"p99_ms"`
	Duration    string                 `json:"duration"`
	Modes       map[string]*modeReport `json:"modes,omitempty"`

	// §10.2 no-loss (async + rmq). NoLossChecked=false => CH-сверка не гонялась.
	NoLossChecked  bool  `json:"no_loss_checked"`
	NoLoss         bool  `json:"no_loss"`
	CHRows         int64 `json:"ch_rows"`
	NoLossExpected int64 `json:"no_loss_expected"`
	// NoLossInconclusive — rows<expected, но бэклог ещё дренировался на момент
	// maxWait/отмены (не подтверждённая потеря). passed() трактует как WARN.
	NoLossInconclusive bool `json:"no_loss_inconclusive"`
}

// applyNoLoss переносит результат CH-сверки в отчёт.
func (rep *report) applyNoLoss(nl noLossResult) {
	rep.NoLossChecked = nl.enabled
	if !nl.enabled {
		return
	}
	rep.CHRows = nl.rows
	rep.NoLossExpected = nl.expected
	rep.NoLoss = nl.ok
	rep.NoLossInconclusive = nl.inconclusive
}

// buildReport агрегирует per-mode накопители в отчёт (общий + по режимам).
func buildReport(res *result, elapsed time.Duration) *report {
	res.mu.Lock()
	defer res.mu.Unlock()

	rep := &report{Duration: elapsed.String(), Modes: make(map[string]*modeReport, len(res.modes))}
	var all []time.Duration
	for mode, a := range res.modes {
		rep.Sent += a.sent
		rep.Errors += a.errors
		all = append(all, a.latencies...)
		mr := &modeReport{
			Sent:   a.sent,
			Errors: a.errors,
			P50Ms:  percentileMs(a.latencies, 0.50),
			P95Ms:  percentileMs(a.latencies, 0.95),
			P99Ms:  percentileMs(a.latencies, 0.99),
		}
		if a.sent > 0 {
			mr.ErrorRate = float64(a.errors) / float64(a.sent)
		}
		rep.Modes[string(mode)] = mr
	}
	if elapsed > 0 {
		rep.AchievedRPS = float64(rep.Sent) / elapsed.Seconds()
	}
	if rep.Sent > 0 {
		rep.ErrorRate = float64(rep.Errors) / float64(rep.Sent)
	}
	rep.P50Ms = percentileMs(all, 0.50)
	rep.P95Ms = percentileMs(all, 0.95)
	rep.P99Ms = percentileMs(all, 0.99)
	return rep
}

func (rep *report) print() {
	fmt.Println("============ load test report ============")
	fmt.Printf("sent:         %d\n", rep.Sent)
	fmt.Printf("errors:       %d (%.4f%%)\n", rep.Errors, rep.ErrorRate*100)
	fmt.Printf("achieved rps: %.1f\n", rep.AchievedRPS)
	fmt.Printf("p50:          %.1f ms\n", rep.P50Ms)
	fmt.Printf("p95:          %.1f ms\n", rep.P95Ms)
	fmt.Printf("p99:          %.1f ms\n", rep.P99Ms)
	for mode, m := range rep.Modes {
		fmt.Printf("  [%-10s] sent=%-7d err=%.4f%% p50=%.1f p95=%.1f p99=%.1f\n",
			mode, m.Sent, m.ErrorRate*100, m.P50Ms, m.P95Ms, m.P99Ms)
	}
	if rep.NoLossChecked {
		verdict := "loss"
		switch {
		case rep.NoLoss:
			verdict = "ok"
		case rep.NoLossInconclusive:
			verdict = "inconclusive (backlog still draining at max-wait; raise --ch-noloss-max-wait)"
		}
		fmt.Printf("no-loss:      ch_rows=%d expected>=%d -> %s\n", rep.CHRows, rep.NoLossExpected, verdict)
	}
	fmt.Println("==========================================")
}

func (rep *report) save(path string) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (rep *report) passed(targetRPS int) bool {
	if rep.AchievedRPS < 0.95*float64(targetRPS) {
		fmt.Fprintf(os.Stderr, "FAIL: achieved rps %.1f < 95%% of target %d\n", rep.AchievedRPS, targetRPS)
		return false
	}
	if rep.P95Ms > 200 {
		fmt.Fprintf(os.Stderr, "FAIL: p95 %.1f ms > 200 ms\n", rep.P95Ms)
		return false
	}
	if rep.ErrorRate >= 0.001 {
		fmt.Fprintf(os.Stderr, "FAIL: error rate %.4f >= 0.001\n", rep.ErrorRate)
		return false
	}
	if rep.NoLossChecked && !rep.NoLoss {
		// Потеря засчитывается только при подтверждённом плато. Inconclusive
		// (бэклог ещё дренировался на момент maxWait) — WARN, а не FAIL: at-least-once
		// + Kafka хранит непрочитанное, сообщения не потеряны. Чтобы получить
		// чистый PASS — увеличь --ch-noloss-max-wait, чтобы бэклог успел слиться.
		if rep.NoLossInconclusive {
			fmt.Fprintf(os.Stderr, "WARN: no-loss inconclusive — ch_rows %d < expected %d, "+
				"но count ещё рос на момент max-wait (бэклог не дослит, не потеря); "+
				"увеличь --ch-noloss-max-wait\n", rep.CHRows, rep.NoLossExpected)
		} else {
			fmt.Fprintf(os.Stderr, "FAIL: message loss — ch_rows %d < expected %d (async+rmq)\n",
				rep.CHRows, rep.NoLossExpected)
			return false
		}
	}
	fmt.Println("PASS: all criteria met")
	return true
}

func runLoad(ctx context.Context, c *client, nodes []node, f flags) *report {
	ctx, cancel := context.WithTimeout(ctx, f.Duration)
	defer cancel()

	res := newResult()
	t0 := time.Now()

	// Простейший pacing: один тикёр на target_rps, рассылающий задания
	// пулу из N воркеров. Не идеален для high-rps (>10k), для 500
	// достаточно.
	interval := max(time.Second/time.Duration(f.TargetRPS), time.Microsecond)

	jobs := make(chan struct{}, f.TargetRPS*2)
	workerCount := 200
	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			for range jobs {
				doRequest(c, nodes, f, res)
			}
		})
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			select {
			case jobs <- struct{}{}:
			default:
			}
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(t0)

	return buildReport(res, elapsed)
}

// doRequest шлёт один запрос к случайному узлу. URL и заголовки зависят от
// режима узла (sync/async/dyn-url/auth-token/auth-basic) — см. requestURL и
// контракт в internal/receiver/usecase. Узлы адресуются с явным team_slug:
// без слога Receiver съедает первый сегмент пути (loadtest/...) как team_slug
// и отвечает 404 (Phase 10.E.1 multi-tenancy).
func doRequest(c *client, nodes []node, f flags, res *result) {
	n := nodes[rand.Intn(len(nodes))]
	size := f.PayloadMin + rand.Intn(f.PayloadMax-f.PayloadMin+1)
	body := make([]byte, size)
	for i := range body {
		body[i] = 'a'
	}

	t0 := time.Now()
	req, _ := http.NewRequest("POST", requestURL(c.baseRecv, f.TeamSlug, n), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	switch n.mode {
	case modeAuthToken:
		req.Header.Set("Authorization", "Bearer "+randomHex(32))
	case modeAuthBasic:
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("lt-user:lt-pass")))
	}
	if f.RandomHeaders {
		addRandomHeaders(req)
	}
	resp, err := c.hc.Do(req)
	d := time.Since(t0)
	errored := err != nil || (resp != nil && resp.StatusCode >= 400)
	if resp != nil {
		// Дочитываем тело до EOF перед Close — обязательное условие
		// возврата соединения в keep-alive-пул (иначе churn соединений).
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	res.add(d, errored, n.mode)
}

func percentileMs(values []time.Duration, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	idx := max(int(float64(len(values))*p)-1, 0)
	return float64(values[idx].Microseconds()) / 1000.0
}
