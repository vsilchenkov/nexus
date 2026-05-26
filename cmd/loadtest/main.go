// Loadtest — сценарный тест производительности (§10.2 ТЗ).
//
// Что делает:
//  1. Поднимает mock-сервер внешних узлов (HTTP).
//  2. Логинится в Web (POST /api/auth/login).
//  3. Создаёт N узлов через POST /api/nodes (с разными url_mode и auth_type).
//  4. Гонит target_rps в течение duration в Receiver (/v1/request/*).
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
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type flags struct {
	WebURL      string
	ReceiverURL string
	AdminLogin  string
	AdminPass   string
	TargetRPS   int
	Duration    time.Duration
	Nodes       int
	PayloadMin  int
	PayloadMax  int
	MockLatency time.Duration
	Cleanup     bool
	Report      string
}

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.WebURL, "web", "http://localhost:8000", "Web service base URL")
	flag.StringVar(&f.ReceiverURL, "receiver", "http://localhost:8080", "Receiver base URL")
	flag.StringVar(&f.AdminLogin, "admin-login", "admin", "Admin login")
	flag.StringVar(&f.AdminPass, "admin-password", "", "Admin password (required)")
	flag.IntVar(&f.TargetRPS, "target-rps", 500, "Target RPS")
	flag.DurationVar(&f.Duration, "duration", 10*time.Minute, "Duration of load")
	flag.IntVar(&f.Nodes, "nodes", 50, "Number of nodes to create")
	flag.IntVar(&f.PayloadMin, "payload-min", 100, "Min payload size (bytes)")
	flag.IntVar(&f.PayloadMax, "payload-max", 5120, "Max payload size")
	flag.DurationVar(&f.MockLatency, "mock-latency", 50*time.Millisecond, "Mock server response latency")
	flag.BoolVar(&f.Cleanup, "cleanup", true, "Delete created nodes after test")
	flag.StringVar(&f.Report, "report", "report.json", "Report file path")
	flag.Parse()
	return f
}

func main() {
	f := parseFlags()
	if f.AdminPass == "" {
		fmt.Fprintln(os.Stderr, "--admin-password is required")
		os.Exit(1)
	}

	mock := startMockServer(f.MockLatency)
	defer mock.Close()
	fmt.Printf("mock server: %s\n", mock.URL)

	ctx := context.Background()
	client := &client{baseWeb: f.WebURL, baseRecv: f.ReceiverURL,
		hc: &http.Client{Timeout: 30 * time.Second}}

	if err := client.login(ctx, f.AdminLogin, f.AdminPass); err != nil {
		fail("login: %v", err)
	}
	fmt.Println("logged in OK")

	nodes, err := client.createNodes(ctx, f.Nodes, mock.URL)
	if err != nil {
		fail("create nodes: %v", err)
	}
	fmt.Printf("created %d nodes\n", len(nodes))

	if f.Cleanup {
		defer client.deleteNodes(context.Background(), nodes)
	}

	report := runLoad(ctx, client, nodes, f)
	report.print()
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

func startMockServer(latency time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jitter := time.Duration(rand.Int63n(int64(latency / 2)))
		time.Sleep(latency - latency/4 + jitter)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"ok":true}`)
	}))
}

// ----- web client ----------------------------------------------------------

type client struct {
	baseWeb  string
	baseRecv string
	hc       *http.Client
	cookie   string
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
		if ck.Name == "databus_session" {
			c.cookie = ck.Value
			return nil
		}
	}
	return fmt.Errorf("session cookie not set")
}

func (c *client) createNodes(ctx context.Context, n int, targetURL string) ([]string, error) {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("loadtest/node-%d-%d", time.Now().UnixNano(), i)
		body, _ := json.Marshal(map[string]any{
			"path":               path,
			"root_method":        "request",
			"target_url":         targetURL,
			"auth_type":          "none",
			"incoming_auth_type": "none",
			"timeout_ms":         30000,
			"clickhouse_table":   "vika_logs.loadtest",
		})
		req, _ := http.NewRequestWithContext(ctx, "POST", c.baseWeb+"/api/nodes", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "databus_session", Value: c.cookie})
		resp, err := c.hc.Do(req)
		if err != nil {
			return paths, err
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			return paths, fmt.Errorf("create node %d status %d", i, resp.StatusCode)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (c *client) deleteNodes(ctx context.Context, paths []string) {
	// Узлы удаляются через /api/nodes/:id, но у нас нет id — пропускаем.
	// Реальная очистка — отдельная задача (использовать GET список + DELETE).
	_ = paths
}

// ----- load ----------------------------------------------------------------

type result struct {
	Latencies []time.Duration
	Errors    int64
	Sent      int64
	mu        sync.Mutex
}

func (r *result) add(d time.Duration, errored bool) {
	r.mu.Lock()
	r.Latencies = append(r.Latencies, d)
	r.mu.Unlock()
	atomic.AddInt64(&r.Sent, 1)
	if errored {
		atomic.AddInt64(&r.Errors, 1)
	}
}

type report struct {
	Sent        int64   `json:"sent"`
	Errors      int64   `json:"errors"`
	ErrorRate   float64 `json:"error_rate"`
	AchievedRPS float64 `json:"achieved_rps"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	Duration    string  `json:"duration"`
}

func (rep *report) print() {
	fmt.Println("============ load test report ============")
	fmt.Printf("sent:         %d\n", rep.Sent)
	fmt.Printf("errors:       %d (%.4f%%)\n", rep.Errors, rep.ErrorRate*100)
	fmt.Printf("achieved rps: %.1f\n", rep.AchievedRPS)
	fmt.Printf("p50:          %.1f ms\n", rep.P50Ms)
	fmt.Printf("p95:          %.1f ms\n", rep.P95Ms)
	fmt.Printf("p99:          %.1f ms\n", rep.P99Ms)
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
	fmt.Println("PASS: all criteria met")
	return true
}

func runLoad(ctx context.Context, c *client, paths []string, f flags) *report {
	ctx, cancel := context.WithTimeout(ctx, f.Duration)
	defer cancel()

	res := &result{}
	t0 := time.Now()

	// Простейший pacing: один тикёр на target_rps, рассылающий задания
	// пулу из N воркеров. Не идеален для high-rps (>10k), для 500
	// достаточно.
	interval := time.Second / time.Duration(f.TargetRPS)
	if interval < time.Microsecond {
		interval = time.Microsecond
	}

	jobs := make(chan struct{}, f.TargetRPS*2)
	workerCount := 200
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				doRequest(c, paths, f, res)
			}
		}()
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

	rep := &report{
		Sent:        atomic.LoadInt64(&res.Sent),
		Errors:      atomic.LoadInt64(&res.Errors),
		AchievedRPS: float64(res.Sent) / elapsed.Seconds(),
		Duration:    elapsed.String(),
	}
	if rep.Sent > 0 {
		rep.ErrorRate = float64(rep.Errors) / float64(rep.Sent)
	}
	rep.P50Ms = percentileMs(res.Latencies, 0.50)
	rep.P95Ms = percentileMs(res.Latencies, 0.95)
	rep.P99Ms = percentileMs(res.Latencies, 0.99)
	return rep
}

func doRequest(c *client, paths []string, f flags, res *result) {
	p := paths[rand.Intn(len(paths))]
	size := f.PayloadMin + rand.Intn(f.PayloadMax-f.PayloadMin+1)
	body := make([]byte, size)
	for i := range body {
		body[i] = 'a'
	}

	url := c.baseRecv + "/v1/request/" + p
	t0 := time.Now()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.hc.Do(req)
	d := time.Since(t0)
	errored := err != nil || (resp != nil && resp.StatusCode >= 400)
	if resp != nil {
		resp.Body.Close()
	}
	res.add(d, errored)
}

func percentileMs(values []time.Duration, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	idx := int(float64(len(values))*p) - 1
	if idx < 0 {
		idx = 0
	}
	return float64(values[idx].Microseconds()) / 1000.0
}
