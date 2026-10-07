package port

import (
	"context"

	"nexus/internal/domain"
)

// LogExportKey — ключ записи, отобранной в выгрузку (§115.7): ID, время её
// новейшего подходящего прогона (date_request, UnixMilli) и date_create этого
// прогона в текстовом виде ClickHouse.
//
// Время и date_create нужны, чтобы батч строк читал узкое окно, а не таблицу:
// ключ сортировки начинается с date_create, и без условия по нему ClickHouse
// не отсекает гранулы и по date_request. date_create берётся из самой строки,
// поэтому сужение верно и на внешней таблице §64, где он не связан с
// date_request.
type LogExportKey struct {
	ID         string
	TsMs       int64
	DateCreate string
}

// LogExportRow — строка выгрузки: прогон записи с телами, урезанными до
// запрошенного числа символов. ReqRunes/RespRunes — полные длины тел в
// символах (lengthUTF8); больше среза — тело в файле обрезано. Без тел оба 0.
type LogExportRow struct {
	Rec       domain.LogRecord
	ReqRunes  int64
	RespRunes int64
}

// LogExportStats — оценка выгрузки (§115.5 estimate). Records — записи
// (uniqExact(ID), та же единица, что у списка и Count), Rows — строки-прогоны.
// ReqBytes/RespBytes — суммы истинных размеров тел, каждый урезан сверху капом
// ячейки: верхняя граница, а не размер сохранённой копии.
type LogExportStats struct {
	Records   uint64
	Rows      uint64
	ReqBytes  int64
	RespBytes int64
}

// LogExportReader — чтение журнала узла для выгрузки в Excel (§115.7).
//
// Отдельный порт, а не расширение LogReader: выгрузке нужны три узких
// операции, а моки LogReader их не знают.
//
// Фильтры q — те же, что у Search, кроме Unresolved, BeforeID и Limit: их
// вызывающая сторона снимает (выгрузка идёт своим курсором и своим лимитом).
type LogExportReader interface {
	// ExportStats — оценка под фильтрами q. Тела с диска не читаются: суммы
	// считаются по request_size/response_size, урезанным до bodyCapBytes.
	ExportStats(ctx context.Context, q LogQuery, bodyCapBytes int64) (LogExportStats, error)

	// ExportKeys — до limit записей в порядке списка журнала (новые сверху),
	// каждая ровно один раз: ключ несёт время новейшего подходящего прогона.
	// Меньше limit — подходящих записей больше нет.
	ExportKeys(ctx context.Context, q LogQuery, limit int) ([]LogExportKey, error)

	// ExportRows — строки для батча ключей, в порядке keys. Для каждой записи —
	// тот же прогон, что выбрал ExportKeys. Запись, исчезнувшая между вызовами
	// (очистка неудачных §79.2, TTL), в ответе отсутствует — разницу считает
	// вызывающая сторона. bodyRunes > 0 — тела урезаны до bodyRunes символов,
	// 0 — тела не читаются (пустые). Батч дочитывается из ClickHouse целиком
	// до возврата: медленный получатель не держит соединение пула.
	ExportRows(ctx context.Context, q LogQuery, keys []LogExportKey, bodyRunes int) ([]LogExportRow, error)
}
