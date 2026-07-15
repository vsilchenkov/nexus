// Package port — интерфейсы зависимостей usecase-слоя Sender Service.
package port

import (
	"context"

	"nexus/internal/domain"
)

// LogWriter — асинхронная запись логов в ClickHouse-таблицу узла (§4.3 ТЗ).
// Реализация буферизует и батчит, file-fallback при недоступности CH (§9.4).
// Возвращает быстро (неблокирующая отдача в канал) — основной поток
// обработки запроса не должен ждать flush в БД.
type LogWriter interface {
	Write(ctx context.Context, table string, rec *domain.LogRecord)
	Flush(ctx context.Context) error
}

// HTTPCaller — outbound HTTP-вызов внешнего узла.
// Внутри: пул соединений (keep-alive), таймаут, метрики попыток.
type HTTPCaller interface {
	Do(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error)
}

// HTTPRequest — параметры исходящего запроса.
type HTTPRequest struct {
	Method string
	URL    string
	// NodePath — путь узла (§50): только для служебного лога редиректов
	// (поле node=...); на сам HTTP-вызов не влияет.
	NodePath  string
	Headers   map[string]string
	Body      []byte
	TimeoutMs int32
}

// RedirectHop — один шаг 3xx-редиректа, за которым последовал httpclient (§50).
// URL уже отредачены (без query — там бывают токены).
type RedirectHop struct {
	Status       int    // 3xx-код, породивший переход
	From         string // scheme://host/path (query отрезан)
	To           string // scheme://host/path (query отрезан)
	FromMethod   string
	ToMethod     string // отличается от FromMethod при 301/302/303 POST→GET
	SchemeChange string // "upgrade" (http→https) | "downgrade" | "same"
}

// HTTPResponse — то, что вернул внешний узел.
type HTTPResponse struct {
	StatusCode int32
	Headers    map[string]string
	Body       []byte
	// TooLarge — тело ответа превысило транспортный лимит (config
	// sender.grpc_max_message_bytes): чтение оборвано на лимите (memory-safe,
	// Body не дочитан), вызывающая сторона отдаёт клиенту 502 (§43-rev).
	TooLarge bool
	// Redirects — цепочка 3xx-редиректов, за которыми последовал клиент (§50).
	// Пустая, если редиректов не было. Sender дописывает их в reason лога, чтобы
	// в UI было видно, что фактический адрес отличается от target_url.
	Redirects []RedirectHop
}
