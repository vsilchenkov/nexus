package usecase

import (
	"time"

	"nexus/internal/domain"
)

// Envelope — формат сообщения в nexus.async (тот же, что Receiver
// в internal/receiver/usecase/envelope.go; продублирован, чтобы
// Sender не импортировал Receiver — это нарушение Clean).
//
// Структуры идентичны по JSON-полям; при изменении любой из них
// обновлять обе. Phase 4 — выделить в shared envelope-пакет.
type Envelope struct {
	ID         string            `json:"id"`
	NodePath   string            `json:"node_path"`
	Method     string            `json:"method"`
	TargetURL  string            `json:"target_url"`
	AuthHeader string            `json:"auth_header,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       []byte            `json:"body,omitempty"`
	ClientIP   string            `json:"client_ip,omitempty"`
	ReceivedAt time.Time         `json:"received_at"`
}

// buildSendInput собирает SendInput из актуального узла и envelope — общий код
// основного async-consumer'а (async.go) и DLQ-репроцессора (dlq_reprocess.go).
// Authorization из env.AuthHeader подмешивается в headers (как в синхронном
// пути Receiver→Sender).
func buildSendInput(node *domain.Node, env Envelope) SendInput {
	headers := env.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	if env.AuthHeader != "" {
		headers["Authorization"] = env.AuthHeader
	}
	return SendInput{
		ID:                 env.ID,
		NodePath:           env.NodePath,
		NodeID:             node.ID,
		RootMethod:         domain.RootMethodRequestAsync,
		TargetURL:          env.TargetURL,
		Method:             env.Method,
		Headers:            headers,
		Body:               env.Body,
		TimeoutMs:          node.TimeoutMs,
		RetryCount:         node.RetryCount,
		RetryBackoffMs:     node.RetryBackoffMs,
		ClickHouseTable:    node.ClickHouseTable,
		LogRequestBody:     node.LogRequestBody,
		LogResponseBody:    node.LogResponseBody,
		LogHeaders:         node.LogHeaders,
		ClientIP:           env.ClientIP,
		LoggingEnabled:     node.LoggingEnabled,
		MaxBodySizeEnabled: node.MaxBodySizeEnabled,
		MaxBodySize:        node.MaxBodySize,
	}
}
