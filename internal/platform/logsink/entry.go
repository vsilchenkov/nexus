// Package logsink — ядро консоли служебных логов (§51 ТЗ): slog-хендлер с
// in-process кольцевым буфером, маскированием чувствительных атрибутов и
// неблокирующей отправкой записей фоновому шипперу, который батчами кладёт их
// в Redis (`nexus:logs:<service>`). Лог-путь никогда не блокируется: при
// переполнении канала записи дропаются со счётчиком.
package logsink

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// maxAttrValueBytes — кап длины строкового значения атрибута (зеркало
// maxSentryValueBytes из platform/sentry, §42): строки едут в Redis и в
// браузер, мегабайтные payload'ы раздули бы и то и другое.
const maxAttrValueBytes = 8 * 1024

// KeyPrefix — префикс Redis-ключей со служебными логами.
const KeyPrefix = "nexus:logs:"

// Key возвращает Redis-ключ списка логов сервиса: nexus:logs:<service>.
func Key(service string) string { return KeyPrefix + service }

// Entry — одна запись служебного лога, как она хранится в Redis (JSON-строка)
// и отдаётся в UI (§51.5).
type Entry struct {
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Service string    `json:"service"`
	// Instance — идентификатор ноды (§70.7). omitempty: у ноды без
	// идентификатора поле отсутствует, и записи, записанные до §70, читаются
	// без изменений.
	Instance string `json:"instance,omitempty"`
	// Replica — какая РЕПЛИКА сервиса записала строку (§93.6): имя хоста
	// контейнера, например web-1.
	//
	// Отдельно от Instance намеренно: Instance — это нода §70 (своя пара
	// PostgreSQL/Redis), и у двух реплик одной ноды он ОДИНАКОВЫЙ. Без этого
	// поля консоль §51 показывала бы вперемешку записи обеих реплик, ничем их
	// не различая, — а «на одной работает, на другой нет» это самый частый
	// вопрос при выкате.
	//
	// Заполняется всегда, когда hostname определился (в одиночной установке
	// это просто `receiver`/`web`/`sender`) — интерфейс сам решает, показывать
	// ли колонку. omitempty оставлен для записей, сделанных до §93, и для
	// окружений без hostname.
	Replica string         `json:"replica,omitempty"`
	Msg     string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// MarshalLine сериализует запись в одну JSON-строку для LPUSH.
func (e Entry) MarshalLine() ([]byte, error) {
	return json.Marshal(e)
}

// truncateValue обрезает строку до maxAttrValueBytes БАЙТ по границе руны
// (не рвёт UTF-8) и дописывает маркер с исходным размером.
func truncateValue(s string) string {
	if len(s) <= maxAttrValueBytes {
		return s
	}
	cut := maxAttrValueBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("…(truncated, %d bytes total)", len(s))
}
