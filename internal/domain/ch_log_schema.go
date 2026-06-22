package domain

// CHLogColumn — одна колонка обязательной схемы таблицы логов узла (§4.3).
type CHLogColumn struct {
	Name string
	Type string
}

// RequiredLogColumns — фиксированный набор колонок таблицы логов в порядке
// INSERT/SELECT. Должен совпадать с insertSQL в
// sender/adapter/out/chlog/writer.go и selectCols в
// web/adapter/out/clickhouse/log_reader.go — иначе batch INSERT/SELECT
// сломается. Это инвариант: шаблон CH-таблицы (§19) задаёт CODEC/индексы/TTL
// поверх этих колонок, но не меняет их состав, имена и типы.
var RequiredLogColumns = []CHLogColumn{
	{"ID", "String"},
	{"type", "String"},
	// §39: HTTP-глагол вызова (GET/POST/…). Рядом с type, ДО url. Заполняется
	// всегда. Раньше глагол писался в `method` — теперь `method` хранит подпуть
	// запроса (хвост path-passthrough), см. ниже.
	{"http_method", "String"},
	{"url", "String"},
	// §39: подпуть запроса после пути узла (хвост path-passthrough), напр.
	// "v1/GetParcelsInfo"; пусто у обычных (не-passthrough) узлов. Семантика
	// изменена: до §39 здесь был HTTP-глагол (теперь он в http_method).
	{"method", "String"},
	{"parameters", "String"},
	{"request", "String"},
	{"response", "String"},
	{"status", "Int32"},
	{"reason", "String"},
	{"date_create", "Date"},
	{"date_request", "DateTime"},
	{"date_response", "DateTime"},
	{"duration", "Int32"},
	{"done", "UInt8"},
	{"checksum_request", "FixedString(32)"},
	{"checksum_response", "FixedString(32)"},
	{"Host", "String"},
	{"IP", "String"},
	{"attempts", "Int32"},
	{"attempts_details", "String"},
	// §37: UUID узла-владельца записи. Различает узлы, делящие одну таблицу
	// (per-node атрибуция). Старые записи (до миграции) — пустая строка.
	{"node_id", "String"},
}

// IsRequiredLogColumn сообщает, входит ли колонка с таким именем в
// обязательную схему таблицы логов.
func IsRequiredLogColumn(name string) bool {
	for _, c := range RequiredLogColumns {
		if c.Name == name {
			return true
		}
	}
	return false
}
