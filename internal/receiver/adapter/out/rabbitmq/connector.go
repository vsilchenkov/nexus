// Package rabbitmq — AMQP-адаптеры Receiver поверх github.com/rabbitmq/
// amqp091-go (§27). Connector открывает один канал на узел для Puller-воркера;
// Lister отдаёт узлы RabbitMQAsync из PG; HealthSink публикует health в Redis.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"nexus/internal/domain"
	"nexus/internal/receiver/usecase"
)

// Connector реализует usecase.RMQConnector. dialTimeout наследуется из
// amqp.DefaultDial. Один Connector переиспользуется всеми воркерами.
type Connector struct{}

var _ usecase.RMQConnector = (*Connector)(nil)

func NewConnector() *Connector { return &Connector{} }

// Connect открывает соединение+канал к очереди узла, ставит prefetch и
// проверяет существование очереди (passive declare). Ошибка «очередь не
// найдена» поднимется наверх — воркер уйдёт в backoff/degraded (§27.2).
func (c *Connector) Connect(ctx context.Context, n *domain.Node) (usecase.RMQConsumer, error) {
	scheme := "amqp"
	if n.RMQUseTLS {
		scheme = "amqps"
	}
	uri := amqp.URI{
		Scheme:   scheme,
		Host:     n.RMQHost,
		Port:     int(n.RMQPort),
		Username: n.RMQUser,
		Password: n.RMQPassword,
		Vhost:    n.RMQVHost,
	}
	cfg := amqp.Config{}
	if n.RMQUseTLS {
		cfg.TLSClientConfig = &tls.Config{ServerName: n.RMQHost, MinVersion: tls.VersionTLS12}
	}

	conn, err := amqp.DialConfig(uri.String(), cfg)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("channel: %w", err)
	}
	if err := ch.Qos(int(n.PullPrefetch), 0, false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("qos: %w", err)
	}
	// Проверяем существование очереди, не создавая её.
	if _, err := ch.QueueDeclarePassive(n.RMQQueue, true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("queue %q not found: %w", n.RMQQueue, err)
	}

	return &consumer{conn: conn, ch: ch, queue: n.RMQQueue}, nil
}

// consumer — открытый канал к очереди одного узла.
type consumer struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	queue string
}

var _ usecase.RMQConsumer = (*consumer)(nil)

func (c *consumer) GetBatch(_ context.Context, max int) ([]usecase.RMQDelivery, error) {
	// Цикл ограничен max (≤ pull_batch_size); basic.get — быстрый локальный
	// вызов. Отмену ctx обрабатывает воркер: pullOnce/handleDelivery делают
	// nack(requeue) уже собранных сообщений при shutdown (graceful, §27.5).
	out := make([]usecase.RMQDelivery, 0, max)
	for range max {
		d, ok, err := c.ch.Get(c.queue, false) // autoAck=false → manual ack
		if err != nil {
			return out, fmt.Errorf("basic.get: %w", err)
		}
		if !ok {
			break // очередь пуста
		}
		out = append(out, toDelivery(d))
	}
	return out, nil
}

func (c *consumer) Ack(tag uint64) error             { return c.ch.Ack(tag, false) }
func (c *consumer) Nack(tag uint64, rq bool) error   { return c.ch.Nack(tag, false, rq) }
func (c *consumer) Reject(tag uint64, rq bool) error { return c.ch.Reject(tag, rq) }

func (c *consumer) QueueStats() (int, int, error) {
	q, err := c.ch.QueueDeclarePassive(c.queue, true, false, false, false, nil)
	if err != nil {
		return 0, 0, err
	}
	return q.Messages, q.Consumers, nil
}

func (c *consumer) Close() error {
	// Закрытие соединения закрывает и канал.
	return c.conn.Close()
}

// toDelivery конвертирует amqp.Delivery в доменно-нейтральный RMQDelivery.
func toDelivery(d amqp.Delivery) usecase.RMQDelivery {
	headers := make(map[string]string, len(d.Headers))
	for k, v := range d.Headers {
		headers[k] = stringifyHeader(v)
	}
	return usecase.RMQDelivery{
		DeliveryTag: d.DeliveryTag,
		Body:        d.Body,
		ContentType: d.ContentType,
		MessageID:   d.MessageId,
		Exchange:    d.Exchange,
		RoutingKey:  d.RoutingKey,
		Timestamp:   d.Timestamp.UTC(),
		Headers:     headers,
	}
}

func stringifyHeader(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}
