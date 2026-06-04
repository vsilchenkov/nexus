// Package kafkaadmin — read-only адаптер к Kafka-кластеру для экрана
// мониторинга (§4.3/§4.5 spec). Поверх высокоуровневого segmentio/kafka-go
// (*kafka.Client): Metadata (топики/брокеры/ISR), ListOffsets (watermarks →
// оценка числа сообщений), ListGroups/OffsetFetch (consumer lag),
// DescribeGroups (число участников). Размер топика на диске недоступен
// (DescribeLogDirs не экспонирован high-level API) — приходит 0 (best-effort).
//
// Реализует port.KafkaAdmin. Создаётся в app.go только при заданном
// cfg.Kafka.Brokers; при недоступности кластера методы возвращают ошибку, а
// usecase отдаёт деградацию (kafka_available=false), не роняя экран.
package kafkaadmin

import (
	"strings"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Client — обёртка над *kafka.Client с таймаутом и порогом ping-warn.
type Client struct {
	kc         *kafka.Client
	brokers    []string
	timeout    time.Duration
	warnPingMs int64
	logger     logging.Logger
}

var _ port.KafkaAdmin = (*Client)(nil)

// New создаёт admin-клиент к брокерам (CSV host:port). warnPingMs — порог
// «повышенной задержки» для §4.5 (0 → дефолт 30мс). Пустой brokers даст клиент
// без адресов — вызывающая сторона (app.go) не должна его создавать.
func New(brokers string, timeout time.Duration, warnPingMs int64, logger logging.Logger) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if warnPingMs <= 0 {
		warnPingMs = 30
	}
	addrs := splitBrokers(brokers)
	return &Client{
		kc:         &kafka.Client{Addr: kafka.TCP(addrs...), Timeout: timeout},
		brokers:    addrs,
		timeout:    timeout,
		warnPingMs: warnPingMs,
		logger:     logger,
	}
}

// splitBrokers разбирает CSV-список брокеров (host:port,host:port).
func splitBrokers(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// brokerIDSet — множество ID живых брокеров из ответа Metadata (брокер,
// присутствующий в списке, считается online).
func brokerIDSet(brokers []kafka.Broker) map[int]struct{} {
	set := make(map[int]struct{}, len(brokers))
	for _, b := range brokers {
		set[b.ID] = struct{}{}
	}
	return set
}

// partitionIDs — список индексов партиций топика.
func partitionIDs(t kafka.Topic) []int {
	ids := make([]int, 0, len(t.Partitions))
	for _, p := range t.Partitions {
		ids = append(ids, p.ID)
	}
	return ids
}
