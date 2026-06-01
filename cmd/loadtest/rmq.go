package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// rmqLoad — параллельная RabbitMQAsync-нагрузка (§27.12): создаёт N узлов
// RabbitMQAsync, объявляет их очереди и публикует в них payload'ы в течение
// f.Duration. Критерий «нет потерь» проверяется сравнением published с числом
// записей в ClickHouse-логе (см. TESTING.md) — здесь печатается published.
type rmqLoad struct {
	enabled   bool
	published atomic.Int64
	paths     []string
	cleanup   func()
	cancel    context.CancelFunc
	done      chan struct{}
}

func startRMQLoad(ctx context.Context, c *client, f flags, targetURL string, n int) *rmqLoad {
	rl := &rmqLoad{done: make(chan struct{})}
	if n <= 0 || f.RMQURL == "" {
		close(rl.done)
		return rl
	}
	uri, err := amqp.ParseURI(f.RMQURL)
	if err != nil {
		fmt.Printf("rmq load disabled: parse rmq-url: %v\n", err)
		close(rl.done)
		return rl
	}

	conn, err := amqp.Dial(f.RMQURL)
	if err != nil {
		fmt.Printf("rmq load disabled: dial: %v\n", err)
		close(rl.done)
		return rl
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		fmt.Printf("rmq load disabled: channel: %v\n", err)
		close(rl.done)
		return rl
	}

	queues := make([]string, 0, n)
	for i := range n {
		queue := fmt.Sprintf("loadtest.rmq.%d.%d", time.Now().UnixNano(), i)
		if _, derr := ch.QueueDeclare(queue, true, false, false, false, nil); derr != nil {
			fmt.Printf("rmq load: declare queue: %v\n", derr)
			continue
		}
		path, cerr := c.createRMQNode(ctx, uri, queue, targetURL)
		if cerr != nil {
			fmt.Printf("rmq load: create node: %v\n", cerr)
			continue
		}
		queues = append(queues, queue)
		rl.paths = append(rl.paths, path)
	}
	if len(queues) == 0 {
		_ = conn.Close()
		close(rl.done)
		return rl
	}

	rl.enabled = true
	if f.Cleanup {
		rl.cleanup = func() { c.deleteNodes(context.Background(), rl.paths) }
	}
	fmt.Printf("created %d RabbitMQAsync nodes, publishing load\n", len(queues))

	pubCtx, cancel := context.WithTimeout(ctx, f.Duration)
	rl.cancel = cancel
	// Доля RPS, идущая в RMQ, пропорциональна доле узлов.
	rmqRPS := max(1, int(float64(f.TargetRPS)*f.RatioRMQ+0.5))
	interval := max(time.Second/time.Duration(rmqRPS), time.Microsecond)

	go func() {
		defer close(rl.done)
		defer conn.Close()
		t := time.NewTicker(interval)
		defer t.Stop()
		var i int
		for {
			select {
			case <-pubCtx.Done():
				return
			case <-t.C:
				queue := queues[i%len(queues)]
				i++
				payload := randomPayload(f.PayloadMin, f.PayloadMax)
				err := ch.PublishWithContext(pubCtx, "", queue, false, false, amqp.Publishing{
					ContentType: "application/json",
					Body:        payload,
				})
				if err == nil {
					rl.published.Add(1)
				}
			}
		}
	}()
	return rl
}

func (rl *rmqLoad) stop() {
	if rl.cancel != nil {
		rl.cancel()
	}
	<-rl.done
	if rl.cleanup != nil {
		rl.cleanup()
	}
}

func (rl *rmqLoad) report() {
	if !rl.enabled {
		return
	}
	fmt.Printf("rmq_published=%d (no-loss check: compare to ClickHouse log rows, see TESTING.md)\n",
		rl.published.Load())
}

// createRMQNode создаёт узел RabbitMQAsync через Web API, нацеленный на очередь.
func (c *client) createRMQNode(ctx context.Context, uri amqp.URI, queue, targetURL string) (string, error) {
	path := fmt.Sprintf("loadtest/rmq-%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{
		"path":              path,
		"root_method":       "RabbitMQAsync",
		"target_url":        targetURL,
		"auth_type":         "none",
		"clickhouse_table":  "nexus_default.loadtest",
		"rmq_host":          uri.Host,
		"rmq_port":          uri.Port,
		"rmq_vhost":         uri.Vhost,
		"rmq_user":          uri.Username,
		"rmq_password":      uri.Password,
		"rmq_queue":         queue,
		"pull_interval_sec": 1,
		"pull_batch_size":   200,
		"pull_prefetch":     200,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseWeb+"/api/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "nexus_session", Value: c.cookie})
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("create rmq node status %d", resp.StatusCode)
	}
	return path, nil
}

// randomPayload — случайный JSON-ish payload заданного диапазона размеров.
func randomPayload(minN, maxN int) []byte {
	n := minN
	if maxN > minN {
		n += rand.Intn(maxN - minN)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rand.Intn(26))
	}
	return b
}
