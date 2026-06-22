package domain

import "time"

// LogRecord — одна запись лога вызова, попадающая в ClickHouse-таблицу
// узла (§4.3 ТЗ). Поля совпадают с CREATE TABLE из спеки.
type LogRecord struct {
	ID         string
	Type       RootMethod
	HTTPMethod string // §39: HTTP-глагол (GET/POST/…) → колонка http_method
	URL        string
	// Method → колонка `method`. §39: подпуть запроса (хвост path-passthrough),
	// напр. "v1/GetParcelsInfo"; пусто у обычных узлов. До §39 здесь был
	// HTTP-глагол (теперь он в HTTPMethod).
	Method           string
	Parameters       string
	Request          string
	Response         string
	Status           int32
	Reason           string
	DateCreate       time.Time
	DateRequest      time.Time
	DateResponse     time.Time
	Duration         int32
	Done             bool
	ChecksumRequest  string // MD5 hex, 32 символа
	ChecksumResponse string
	Host             string
	IP               string
	Attempts         int32
	AttemptsDetails  string // JSON-массив попыток или пустая строка
	NodeID           string // §37: UUID узла-владельца (различает узлы в общей таблице); "" у legacy-записей
}
