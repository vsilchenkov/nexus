package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
)

// kafkaPageMaxOverview — потолок ответа /api/kafka/overview под нагрузкой
// (§114.L): бюджет чтения брокера 3 с (§114.4) плюс Prometheus и запас.
const kafkaPageMaxOverview = 5 * time.Second

// kafkaPageReport — страница Kafka под нагрузкой (§114.L). Unavailable —
// ответы «брокер не ответил» (kafka_available=false или reason
// broker_unreachable) при живом брокере: бюджет чтения не уложился под
// нагрузкой, и админ увидел бы ложный баннер. Failures — ошибки HTTP и
// ответы не 200.
type kafkaPageReport struct {
	Polls         int     `json:"polls"`
	OverviewP95Ms float64 `json:"overview_p95_ms"`
	OverviewMaxMs float64 `json:"overview_max_ms"`
	TopicsP95Ms   float64 `json:"topics_p95_ms"`
	TopicsMaxMs   float64 `json:"topics_max_ms"`
	Unavailable   int     `json:"unavailable"`
	Failures      int     `json:"failures"`
}

// problems — нарушенные критерии; пусто — проверка пройдена.
func (r kafkaPageReport) problems() []string {
	var out []string
	if r.Polls == 0 {
		out = append(out, "kafka page: no polls completed")
	}
	if r.Failures > 0 {
		out = append(out, fmt.Sprintf("kafka page: %d failed requests", r.Failures))
	}
	if r.Unavailable > 0 {
		out = append(out, fmt.Sprintf("kafka page: %d responses said the broker did not answer", r.Unavailable))
	}
	if r.OverviewMaxMs > float64(kafkaPageMaxOverview.Milliseconds()) {
		out = append(out, fmt.Sprintf("kafka page: overview took %.0f ms > %d ms",
			r.OverviewMaxMs, kafkaPageMaxOverview.Milliseconds()))
	}
	return out
}

// kafkaPagePoll — один опрос страницы: оба запроса автообновления.
type kafkaPagePoll struct {
	overview, topics time.Duration
	unavailable      bool
	failed           bool
}

// getJSONFn — GET админского API с разбором JSON и замером времени.
type getJSONFn func(ctx context.Context, path string, dst any) (time.Duration, error)

// pollKafkaPage опрашивает overview и topics так, как это делает открытая
// вкладка страницы Kafka.
func pollKafkaPage(ctx context.Context, get getJSONFn) kafkaPagePoll {
	var ov struct {
		KafkaAvailable bool `json:"kafka_available"`
		Health         struct {
			Reason string `json:"reason"`
		} `json:"health"`
	}
	var tp struct {
		KafkaAvailable bool `json:"kafka_available"`
	}
	var p kafkaPagePoll
	var errOv, errTp error
	p.overview, errOv = get(ctx, "/api/kafka/overview?range=1h", &ov)
	p.topics, errTp = get(ctx, "/api/kafka/topics", &tp)
	p.failed = errOv != nil || errTp != nil
	if errOv == nil && (!ov.KafkaAvailable || ov.Health.Reason == "broker_unreachable") {
		p.unavailable = true
	}
	if errTp == nil && !tp.KafkaAvailable {
		p.unavailable = true
	}
	if p.failed {
		fmt.Printf("kafka page: request failed: overview=%v topics=%v\n", errOv, errTp)
	}
	return p
}

// kafkaPageAccum — накопитель опросов; пишет только горутина пробы.
type kafkaPageAccum struct {
	overview, topics []time.Duration
	unavailable      int
	failures         int
}

func (a *kafkaPageAccum) add(p kafkaPagePoll) {
	a.overview = append(a.overview, p.overview)
	a.topics = append(a.topics, p.topics)
	if p.unavailable {
		a.unavailable++
	}
	if p.failed {
		a.failures++
	}
}

func (a *kafkaPageAccum) report() kafkaPageReport {
	return kafkaPageReport{
		Polls:         len(a.overview),
		OverviewP95Ms: percentileMs(a.overview, 0.95),
		OverviewMaxMs: percentileMs(a.overview, 1),
		TopicsP95Ms:   percentileMs(a.topics, 0.95),
		TopicsMaxMs:   percentileMs(a.topics, 1),
		Unavailable:   a.unavailable,
		Failures:      a.failures,
	}
}

// kafkaPageProbe — опрос страницы Kafka раз в every, пока идёт нагрузка и
// слив бэклога (§114.L).
type kafkaPageProbe struct {
	cancel context.CancelFunc
	done   chan struct{}
	acc    kafkaPageAccum // читается только после done
}

// startKafkaPageProbe запускает опрос; every ≤ 0 — проверка выключена.
func startKafkaPageProbe(ctx context.Context, get getJSONFn, every time.Duration, logger logging.Logger) *kafkaPageProbe {
	p := &kafkaPageProbe{done: make(chan struct{})}
	if every <= 0 {
		close(p.done)
		return p
	}
	pctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go func() {
		defer close(p.done)
		defer safego.Recover(logger, "loadtest.kafkaPage")
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			poll := pollKafkaPage(pctx, get)
			if pctx.Err() != nil {
				return // опрос прервал stop() — это не ошибка страницы
			}
			p.acc.add(poll)
			select {
			case <-pctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return p
}

// stop останавливает опрос и возвращает отчёт; nil — проверка выключена.
func (p *kafkaPageProbe) stop() *kafkaPageReport {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	<-p.done
	r := p.acc.report()
	return &r
}

// getJSON — GET админского API Web от имени сессии loadtest'а. Время — до
// конца чтения тела: так его видит вкладка браузера.
func (c *client) getJSON(ctx context.Context, path string, dst any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseWeb+path, nil)
	if err != nil {
		return 0, err
	}
	req.AddCookie(&http.Cookie{Name: "nexus_session", Value: c.cookie})
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return time.Since(start), err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	if resp.StatusCode != http.StatusOK {
		return elapsed, fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return elapsed, json.Unmarshal(body, dst)
}
