package usecase

import (
	"context"
	"time"

	"nexus/internal/platform/logging"
)

// RMQProbeParams — параметры диагностического AMQP-подключения (§27.8,
// POST /api/nodes/test-rmq). Пароль приходит в открытом виде из формы UI.
type RMQProbeParams struct {
	Host     string
	Port     int
	VHost    string
	User     string
	Password string
	Queue    string
	UseTLS   bool
}

// RMQCheck — результат одного шага проверки (connect/auth/queue). MessageCount/
// ConsumerCount заполняются только для шага queue при успехе.
type RMQCheck struct {
	OK            bool   `json:"ok"`
	ElapsedMs     int64  `json:"elapsed_ms"`
	Error         string `json:"error,omitempty"`
	MessageCount  int    `json:"message_count,omitempty"`
	ConsumerCount int    `json:"consumer_count,omitempty"`
}

// RMQChecks — три шага диагностики в порядке выполнения.
type RMQChecks struct {
	Connect RMQCheck `json:"connect"`
	Auth    RMQCheck `json:"auth"`
	Queue   RMQCheck `json:"queue"`
}

// RMQTestResult — итог POST /api/nodes/test-rmq. Эндпоинт всегда отдаёт 200
// (диагностика, не функциональный вызов); OK=false при любом проваленном шаге.
type RMQTestResult struct {
	OK     bool      `json:"ok"`
	Checks RMQChecks `json:"checks"`
}

// RMQDialer — порт (consumer-side, CLAUDE.md §3): реальный AMQP-handshake.
// Реализуется в adapter/out/rabbitmq поверх amqp091-go; в unit-тестах
// подменяется fake'ом без сети.
type RMQDialer interface {
	Probe(ctx context.Context, p RMQProbeParams) RMQTestResult
}

// RMQTester — usecase «Проверить подключение» к RabbitMQ (§27.8). Не сохраняет
// узел; только проверяет, что с заданными кредами соединение, аутентификация
// и пассивное объявление очереди проходят. Дефолтит порт/vhost, остальное —
// дело диалера.
type RMQTester struct {
	dialer RMQDialer
	logger logging.Logger
}

func NewRMQTester(dialer RMQDialer, logger logging.Logger) *RMQTester {
	return &RMQTester{dialer: dialer, logger: logger}
}

// Test нормализует параметры и делегирует диалеру. Возвращает только
// RMQTestResult — инфраструктурных ошибок нет, любой провал — внутри Checks.
func (t *RMQTester) Test(ctx context.Context, p RMQProbeParams) RMQTestResult {
	if p.Port == 0 {
		if p.UseTLS {
			p.Port = 5671
		} else {
			p.Port = 5672
		}
	}
	if p.VHost == "" {
		p.VHost = "/"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return t.dialer.Probe(ctx, p)
}
