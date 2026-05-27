package usecase

import (
	"net/http"
	"net/url"
	"time"

	"nexus/internal/domain"
)

// Envelope — формат сообщения в nexus.async.
// Содержит всё, что нужно Sender-consumer'у для повторного выполнения
// запроса: фактический target URL (уже разрешённый), готовый заголовок
// Authorization (если был), отфильтрованные forward-headers, тело.
//
// Узел в момент обработки на стороне Sender может уже измениться (status,
// timeout и т.п.) — поэтому consumer перечитывает actual node-конфиг
// и применяет его поверх envelope. Resolved URL/Auth — фиксированный
// факт момента приёма, чтобы повторная отправка дала тот же эффект.
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

// BuildEnvelope формирует Envelope из входящего запроса узла,
// уже прошедшего incoming-auth + resolveURL + dynamic-auth.
func BuildEnvelope(
	id string,
	node *domain.Node,
	method, targetURL, authHeader, clientIP string,
	headers http.Header,
	cleanQuery url.Values,
	body []byte,
) *Envelope {
	finalURL := appendQuery(targetURL, cleanQuery)
	picked := pickForwardHeaders(headers, node.ForwardHeaders)
	if ct := headers.Get("Content-Type"); ct != "" {
		picked["Content-Type"] = ct
	}
	return &Envelope{
		ID:         id,
		NodePath:   node.Path,
		Method:     method,
		TargetURL:  finalURL,
		AuthHeader: authHeader,
		Headers:    picked,
		Body:       body,
		ClientIP:   clientIP,
		ReceivedAt: time.Now().UTC(),
	}
}
