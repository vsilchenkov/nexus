package usecase

import (
	"net/url"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// Envelope — формат сообщения в nexus.async (тот же, что Receiver
// в internal/receiver/usecase/envelope.go; продублирован, чтобы
// Sender не импортировал Receiver — это нарушение Clean).
//
// Структуры обязаны совпадать по JSON-полям: при изменении любой из них
// обновлять обе. Совпадение держит контрактный тест tests/contract — до него
// копии молча разошлись (у Sender не было блока RMQ), и происхождение
// RabbitMQAsync-сообщений терялось при доставке. Phase 4 — выделить в shared
// envelope-пакет.
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
	// RequestPath — §39: подпуть запроса (хвост path-passthrough), для колонки
	// `method` лог-таблицы. Держать синхронным с Receiver-копией Envelope
	// (internal/receiver/usecase/envelope.go).
	RequestPath string `json:"request_path,omitempty"`

	// RMQ — происхождение сообщения, вычитанного Puller'ом из RabbitMQ (§27.3).
	// nil для request/requestAsync. На доставку не влияет (узел перечитывается
	// из PostgreSQL), но это единственный след, по которому сообщение в очереди
	// сопоставляется с сообщением в исходном брокере.
	RMQ *RMQMeta `json:"rmq,omitempty"`
}

// RMQMeta — происхождение сообщения из RabbitMQ (§27.3). Копия
// receiver/usecase.RMQMeta, см. комментарий к Envelope.
type RMQMeta struct {
	Exchange    string    `json:"exchange,omitempty"`
	RoutingKey  string    `json:"routing_key,omitempty"`
	DeliveryTag uint64    `json:"delivery_tag,omitempty"`
	MessageID   string    `json:"message_id,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// resolveTargetURL — адрес доставки async-сообщения: для static-узла собирается
// заново по АКТУАЛЬНОМУ конфигу, иначе берётся зафиксированный в конверте (§69.3).
//
// Receiver резолвит URL один раз, на приёме, и кладёт готовую строку в конверт.
// Из-за этого исправление target_url узла не действовало на уже принятые
// сообщения: боевой инцидент 2026-07-27 — узел приняли с адресом без схемы,
// конфиг починили через 4 минуты, а шесть сообщений продолжали падать
// «unsupported protocol scheme» каждые 5 минут ещё сутки, до истечения DLQ TTL.
//
// Собираем как Receiver: база (без своей query) + хвост path-passthrough + query
// конверта. Query берём из конверта, а не из свежего target_url: там она уже
// объединена с query входящего запроса. Следствие (документированное
// ограничение): правка схемы/хоста/пути в target_url на застрявшие сообщения
// действует, правка query внутри target_url — нет.
//
// Для url_mode=from_request пересборка невозможна: исходное значение
// url-параметра вырезано из query на приёме и живёт только внутри
// env.TargetURL. Любой сбой разбора → адрес из конверта (как раньше).
func resolveTargetURL(node *domain.Node, env Envelope) string {
	if node.URLMode != domain.URLModeStatic {
		return env.TargetURL
	}
	base, ok := domain.AbsoluteHTTPURL(node.TargetURL)
	if !ok {
		return env.TargetURL
	}
	base.RawQuery, base.Fragment = "", ""
	if env.RequestPath != "" {
		base = base.JoinPath(env.RequestPath)
	}
	if envURL, err := url.Parse(env.TargetURL); err == nil {
		base.RawQuery = envURL.RawQuery
	}
	return base.String()
}

// logRebuiltTarget — Debug о том, что адрес доставки отличается от записанного
// в конверте (§51.9): именно этот факт объясняет, почему сообщение вдруг поехало
// по другому адресу после правки узла. Молчит, когда адрес не изменился.
// URL печатаем без query — в ней бывают токены (§7.5).
func logRebuiltTarget(logger logging.Logger, op string, env Envelope, resolved string) {
	if resolved == env.TargetURL {
		return
	}
	logger.Debug(op+": target url rebuilt from node config",
		logger.Str("id", env.ID),
		logger.Str("node_path", env.NodePath),
		logger.Str("was", urlWithoutQuery(env.TargetURL)),
		logger.Str("now", urlWithoutQuery(resolved)))
}

// logRMQOrigin — Debug о происхождении сообщения, пришедшего из RabbitMQ
// (§27.3, §51.9). В логе узла такое сообщение неотличимо от обычного
// requestAsync, поэтому связать доставку с сообщением в брокере можно только
// по этим полям. Молчит для request/requestAsync (блока нет).
//
// Тело и заголовки сюда не попадают: печатаются только координаты сообщения
// в брокере.
func logRMQOrigin(logger logging.Logger, op string, env Envelope) {
	if env.RMQ == nil {
		return
	}
	logger.Debug(op+": message originates from rabbitmq",
		logger.Str("id", env.ID),
		logger.Str("node_path", env.NodePath),
		logger.Str("exchange", env.RMQ.Exchange),
		logger.Str("routing_key", env.RMQ.RoutingKey),
		logger.Str("message_id", env.RMQ.MessageID))
}

// urlWithoutQuery отрезает query и fragment: адрес без чувствительных значений.
// Работает и для строк без схемы (боевой случай «host/path?x=1»).
func urlWithoutQuery(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
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
		TargetURL:          resolveTargetURL(node, env),
		Method:             env.Method,
		RequestPath:        env.RequestPath,
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
