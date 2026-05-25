package usecase

import "time"

// Envelope — формат сообщения в databus.async (тот же, что Receiver
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
