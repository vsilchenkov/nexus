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
	Method    string
	URL       string
	Headers   map[string]string
	Body      []byte
	TimeoutMs int32
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
}
