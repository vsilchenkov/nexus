// Package kafka — работа с Kafka поверх segmentio/kafka-go: producer
// (Receiver, а в Sender — DLQ и топик паузы), consumer-reader'ы Sender'а,
// служебные операции (топики, offset'ы) и healthcheck подключения к брокеру.
//
// Producer не идемпотентен (§118.10): kafka-go этого не умеет, повтор записи
// может задвоить сообщение в топике — с тем же ID конверта.
package kafka

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"nexus/internal/platform/healthcheck"
)

// Dialer — реализация healthcheck.Checker через TCP-dial к одному из broker'ов.
// Это минимальный chec без зависимости от kafka-клиента; настоящий
// AdminClient придёт с реализацией producer/consumer.
type Dialer struct {
	Brokers []string
	Timeout time.Duration
}

func NewDialer(brokers string) *Dialer {
	parts := strings.Split(brokers, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return &Dialer{Brokers: parts, Timeout: 2 * time.Second}
}

func (d *Dialer) Dial(ctx context.Context) error {
	if len(d.Brokers) == 0 {
		return fmt.Errorf("no brokers configured")
	}
	dialer := &net.Dialer{Timeout: d.Timeout}
	var lastErr error
	for _, addr := range d.Brokers {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		return nil
	}
	return fmt.Errorf("all kafka brokers unreachable: %w", lastErr)
}

// HealthChecker — healthcheck.Checker для Kafka.
func HealthChecker(name string, d *Dialer) healthcheck.Checker {
	return healthcheck.CheckerFunc{
		N: name,
		F: d.Dial,
	}
}
