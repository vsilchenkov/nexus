package domain

import "time"

// ServiceLogEntry — запись служебного лога сервиса (§51): то, что RingHandler
// кладёт в Redis (`nexus:logs:<service>`) и что консоль «Логи» показывает в UI.
// НЕ путать с логами запросов узлов (ClickHouse, §7.4).
type ServiceLogEntry struct {
	TS      time.Time      `json:"ts"`
	Level   string         `json:"level"`
	Service string         `json:"service"`
	Msg     string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// Имена сервисов шины — теги записей и суффиксы Redis-ключей (§51).
const (
	ServiceLogReceiver = "receiver"
	ServiceLogSender   = "sender"
	ServiceLogWeb      = "web"
	// ServiceLogAll — псевдо-значение параметра service: все три сервиса.
	ServiceLogAll = "all"
)

// ServiceLogServices возвращает список реальных сервисов (без "all").
func ServiceLogServices() []string {
	return []string{ServiceLogReceiver, ServiceLogSender, ServiceLogWeb}
}

// ValidServiceLogService — true для receiver|sender|web (без "all").
func ValidServiceLogService(s string) bool {
	return s == ServiceLogReceiver || s == ServiceLogSender || s == ServiceLogWeb
}

// serviceLogLevelRank — порядок уровней для фильтра min_level.
var serviceLogLevelRank = map[string]int{
	"debug": 0,
	"info":  1,
	"warn":  2,
	"error": 3,
}

// ServiceLogLevelRank возвращает ранг уровня (debug=0..error=3).
// ok=false для неизвестной строки (например, slog-офсеты вида "debug-4").
func ServiceLogLevelRank(level string) (int, bool) {
	r, ok := serviceLogLevelRank[level]
	return r, ok
}
