package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Счётчики Sender'а, прирост которых за прогон обязан быть нулём (§114.L):
// под нагрузкой копится бэклог, и сторож зависшего consumer'а (§113.6) не
// должен принимать занятый экземпляр за зависший, а commit — отвергаться.
const (
	metricConsumerRestarts = "nexus_kafka_consumer_restarts_total"
	metricCommitErrors     = "nexus_kafka_commit_errors_total"
)

// senderMetricsReport — прирост счётчиков Sender'а за прогон. Error —
// снимок не снят: проверка включена, но не выполнена (это провал, а не
// «нарушений нет»).
type senderMetricsReport struct {
	RestartsDelta     float64 `json:"restarts_delta"`
	CommitErrorsDelta float64 `json:"commit_errors_delta"`
	Error             string  `json:"error,omitempty"`
}

// problems — нарушенные критерии; пусто — проверка пройдена.
func (r senderMetricsReport) problems() []string {
	var out []string
	if r.Error != "" {
		out = append(out, "sender metrics: "+r.Error)
	}
	if r.RestartsDelta > 0 {
		out = append(out, fmt.Sprintf("sender metrics: %s grew by %.0f", metricConsumerRestarts, r.RestartsDelta))
	}
	if r.CommitErrorsDelta > 0 {
		out = append(out, fmt.Sprintf("sender metrics: %s grew by %.0f", metricCommitErrors, r.CommitErrorsDelta))
	}
	return out
}

// counterSum — сумма всех рядов счётчика name в тексте /metrics (формат
// Prometheus text exposition). Ряда нет — 0: счётчик ещё не увеличивался.
func counterSum(text, name string) (float64, error) {
	var sum float64
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		rest, ok := strings.CutPrefix(line, name)
		if !ok || (rest != "" && rest[0] != '{' && rest[0] != ' ') {
			continue // комментарий, другая метрика или name_с_суффиксом
		}
		fields := strings.Fields(rest[strings.LastIndexByte(rest, '}')+1:])
		if len(fields) == 0 {
			return 0, fmt.Errorf("%s: no value in %q", name, line)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		sum += v
	}
	return sum, sc.Err()
}

// counterDelta — прирост счётчика; after < before — процесс перезапускался
// и счётчик начался заново, прирост — всё, что накопилось после рестарта.
func counterDelta(before, after float64) float64 {
	if after < before {
		return after
	}
	return after - before
}

// senderSnapshot — значения проверяемых счётчиков на момент снимка.
type senderSnapshot struct {
	restarts, commitErrors float64
}

// scrapeSender снимает /metrics Sender'а.
func scrapeSender(ctx context.Context, hc *http.Client, url string) (senderSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return senderSnapshot{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return senderSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return senderSnapshot{}, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var sb strings.Builder
	if _, err := bufio.NewReader(resp.Body).WriteTo(&sb); err != nil {
		return senderSnapshot{}, err
	}
	var s senderSnapshot
	if s.restarts, err = counterSum(sb.String(), metricConsumerRestarts); err != nil {
		return senderSnapshot{}, err
	}
	if s.commitErrors, err = counterSum(sb.String(), metricCommitErrors); err != nil {
		return senderSnapshot{}, err
	}
	return s, nil
}

// senderWatch — снимок счётчиков Sender'а до прогона и сравнение после.
type senderWatch struct {
	hc     *http.Client
	url    string
	before senderSnapshot
	err    error
}

// watchSender снимает счётчики до прогона; пустой url — проверка выключена.
func watchSender(ctx context.Context, hc *http.Client, url string) *senderWatch {
	w := &senderWatch{hc: hc, url: url}
	if url == "" {
		return w
	}
	w.before, w.err = scrapeSender(ctx, hc, url)
	return w
}

// finish снимает счётчики после прогона и возвращает прирост; nil —
// проверка выключена.
func (w *senderWatch) finish(ctx context.Context) *senderMetricsReport {
	if w.url == "" {
		return nil
	}
	if w.err != nil {
		return &senderMetricsReport{Error: "snapshot before load: " + w.err.Error()}
	}
	after, err := scrapeSender(ctx, w.hc, w.url)
	if err != nil {
		return &senderMetricsReport{Error: "snapshot after load: " + err.Error()}
	}
	return &senderMetricsReport{
		RestartsDelta:     counterDelta(w.before.restarts, after.restarts),
		CommitErrorsDelta: counterDelta(w.before.commitErrors, after.commitErrors),
	}
}
