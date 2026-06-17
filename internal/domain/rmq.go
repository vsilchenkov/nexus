package domain

import "time"

// RMQConnState — состояние AMQP-подключения Puller-воркера (§27.2, §27.9).
type RMQConnState string

const (
	RMQConnDown       RMQConnState = "down"
	RMQConnConnecting RMQConnState = "connecting"
	RMQConnUp         RMQConnState = "up"
)

// RMQHealth — runtime-снимок состояния Puller-воркера узла RabbitMQAsync
// (§27.4). НЕ персистится в node.status: это вычисляемое health-состояние,
// которым Receiver делится с Web через общий стор (Redis). degraded
// поднимается, когда RabbitMQ недоступен дольше DegradeAfter или очередь не
// найдена; снимается автоматически после восстановления.
type RMQHealth struct {
	NodePath      string       `json:"node_path"`
	ConnState     RMQConnState `json:"connection_state"`
	Degraded      bool         `json:"degraded"`
	Reason        string       `json:"reason,omitempty"`
	QueueDepth    int64        `json:"queue_depth"`
	ConsumerCount int64        `json:"consumer_count"`
	Attempts      int64        `json:"attempts"`
	Since         time.Time    `json:"since"`
	UpdatedAt     time.Time    `json:"updated_at"`
}
