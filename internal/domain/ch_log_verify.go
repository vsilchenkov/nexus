package domain

import "strings"

// LogColumnMismatch — колонка есть, но её тип отличается от требуемого (§64).
type LogColumnMismatch struct {
	Name string `json:"name"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// LogTableVerifyResult — итог сверки существующей таблицы с обязательной схемой
// логов (§64). Пустые Missing и Mismatched означают, что таблица пригодна:
// Sender сможет писать в неё INSERT, а UI — читать логи и считать метрики.
type LogTableVerifyResult struct {
	Missing    []string            `json:"missing"`
	Mismatched []LogColumnMismatch `json:"mismatched"`
}

// OK сообщает, пригодна ли таблица для логов узла.
func (r LogTableVerifyResult) OK() bool {
	return len(r.Missing) == 0 && len(r.Mismatched) == 0
}

// VerifyLogTableColumns сверяет фактические колонки таблицы с RequiredLogColumns.
//
// ЛИШНИЕ колонки допускаются: таблицей владеет оператор или посторонний
// сервис-писатель, и свои поля он держать вправе — INSERT и SELECT Nexus'а
// перечисляют колонки явно, так что чужие им не мешают.
func VerifyLogTableColumns(actual []CHLogColumn) LogTableVerifyResult {
	have := make(map[string]string, len(actual))
	for _, c := range actual {
		have[c.Name] = c.Type
	}

	var res LogTableVerifyResult
	for _, want := range RequiredLogColumns {
		got, ok := have[want.Name]
		if !ok {
			res.Missing = append(res.Missing, want.Name)
			continue
		}
		if !chTypesEqual(want.Type, got) {
			res.Mismatched = append(res.Mismatched, LogColumnMismatch{Name: want.Name, Want: want.Type, Got: got})
		}
	}
	return res
}

// chTypesEqual сравнивает типы колонок ClickHouse с минимальной нормализацией.
//
// Убираем пробелы (`FixedString( 32 )`) и параметр таймзоны у DateTime:
// `DateTime('UTC')` бинарно совместим с `DateTime` — это лишь атрибут
// отображения, менять его владельцу таблицы незачем.
//
// `Bool` считается равным `UInt8`: в ClickHouse Bool — алиас UInt8 с тем же
// физическим представлением, а поле `LogRecord.Done` объявлено в Go как `bool`,
// поэтому драйвер одинаково принимает обе формы и на запись (Append), и на
// чтение (Scan). Без этого таблицы с `done Bool` (так объявляли схему до §19)
// помечались непригодными, хотя Nexus читает и пишет их без ошибок.
//
// Nullable(...)/LowCardinality(...) НЕ разворачиваем: это честное расхождение
// контракта (Nullable-колонка меняет представление и поведение вставки), и
// оператор должен увидеть его в отчёте, а не получить молчаливое «ок».
func chTypesEqual(want, got string) bool {
	return normalizeCHType(want) == normalizeCHType(got)
}

func normalizeCHType(t string) string {
	t = strings.ReplaceAll(t, " ", "")
	if strings.HasPrefix(t, "DateTime(") && !strings.HasPrefix(t, "DateTime64(") {
		return "DateTime"
	}
	if t == "Bool" {
		return "UInt8"
	}
	return t
}
