package http

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/xlsx"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

const (
	// xlsxContentType — MIME книги Excel.
	xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	// exportFlushEveryRows — как часто выталкивать накопленное клиенту: браузер
	// видит прогресс, nginx (X-Accel-Buffering: no) не копит ответ.
	exportFlushEveryRows = 100
)

// exportColumn — колонка листа «Логи»: заголовок — имя колонки ClickHouse как
// есть (§115.3), значение — ячейка из прогона.
type exportColumn struct {
	name  string
	width float64
	body  bool // request/response — только при выгрузке тел
}

// exportColumns — все колонки таблицы журнала в порядке схемы ClickHouse.
func exportColumns() []exportColumn {
	return []exportColumn{
		{name: "ID", width: 38}, {name: "type", width: 14}, {name: "http_method", width: 8},
		{name: "url", width: 40}, {name: "method", width: 24}, {name: "parameters", width: 30},
		{name: "request", width: 50, body: true}, {name: "response", width: 50, body: true},
		{name: "status", width: 8}, {name: "reason", width: 30},
		{name: "date_create", width: 12}, {name: "date_request", width: 20}, {name: "date_response", width: 20},
		{name: "duration", width: 10}, {name: "done", width: 6},
		{name: "checksum_request", width: 34}, {name: "checksum_response", width: 34},
		{name: "Host", width: 20}, {name: "IP", width: 16}, {name: "client_host", width: 24},
		{name: "attempts", width: 9}, {name: "attempts_details", width: 30}, {name: "node_id", width: 38},
		{name: "request_size", width: 14}, {name: "response_size", width: 14},
	}
}

// xlsxLogSink пишет выгрузку журнала книгой XLSX прямо в HTTP-ответ
// (usecase.LogsExportSink). Память держит буфер текущей части, а не файл.
type xlsxLogSink struct {
	c       *gin.Context
	loc     *time.Location
	lang    i18n.Lang
	version string

	w     *xlsx.Writer
	meta  usecase.LogsExportMeta
	rows  int
	cells []xlsx.Cell
	// unitCut — тела, урезанные только по пределу ячейки в UTF-16 (символов
	// не больше BodyChars, но с эмодзи): usecase считает обрезку по символам
	// и их не видит.
	unitCut int
}

var _ usecase.LogsExportSink = (*xlsxLogSink)(nil)

func newXLSXLogSink(c *gin.Context, loc *time.Location, lang i18n.Lang, version string) *xlsxLogSink {
	return &xlsxLogSink{c: c, loc: loc, lang: lang, version: version}
}

func (s *xlsxLogSink) t(key string) string { return i18n.Translate(s.lang, key) }

// startedAt — момент начала выгрузки (нулевой, если поток не начинался).
func (s *xlsxLogSink) startedAt() time.Time { return s.meta.StartedAt }

// Begin отправляет заголовки ответа и открывает лист «Логи».
func (s *xlsxLogSink) Begin(_ context.Context, m usecase.LogsExportMeta) error {
	s.meta = m
	h := s.c.Writer.Header()
	h.Set("Content-Type", xlsxContentType)
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFileName(m, s.loc)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Nexus-Export-Rows", strconv.Itoa(m.Selected))
	s.c.Status(http.StatusOK)

	w, err := xlsx.NewWriter(s.c.Writer, s.t("logs_export.sheet_logs"), s.t("logs_export.sheet_params"))
	if err != nil {
		return fmt.Errorf("logs export: open workbook: %w", err)
	}
	s.w = w
	all := exportColumns()
	cols := make([]xlsx.Column, 0, len(all))
	for _, col := range all {
		if col.body && !m.Bodies {
			continue
		}
		cols = append(cols, xlsx.Column{Title: col.name, Width: col.width})
	}
	if err := w.StartSheet(cols, xlsx.SheetOptions{FreezeHeader: true, AutoFilter: true}); err != nil {
		return fmt.Errorf("logs export: start sheet: %w", err)
	}
	return s.flush()
}

// Row пишет одну запись журнала.
func (s *xlsxLogSink) Row(_ context.Context, r port.LogExportRow) error {
	if err := s.w.WriteRow(s.rowCells(r)...); err != nil {
		return fmt.Errorf("logs export: write row: %w", err)
	}
	s.rows++
	if s.rows%exportFlushEveryRows == 0 {
		return s.flush()
	}
	return nil
}

// rowCells — ячейки записи в порядке exportColumns. Срез переиспользуется
// между строками: WriteRow пишет ячейки сразу и ссылок на них не держит.
func (s *xlsxLogSink) rowCells(r port.LogExportRow) []xlsx.Cell {
	rec := &r.Rec
	c := append(s.cells[:0],
		xlsx.Str(rec.ID), xlsx.Str(string(rec.Type)), xlsx.Str(rec.HTTPMethod),
		xlsx.Str(rec.URL), xlsx.Str(rec.Method), xlsx.Str(rec.Parameters))
	if s.meta.Bodies {
		c = append(c, s.bodyCell(rec.Request, r.ReqRunes), s.bodyCell(rec.Response, r.RespRunes))
	}
	c = append(c,
		xlsx.Int(int64(rec.Status)), xlsx.Str(rec.Reason),
		xlsx.Date(rec.DateCreate), s.timeCell(rec.DateRequest), s.timeCell(rec.DateResponse),
		xlsx.Int(int64(rec.Duration)), xlsx.Int(boolInt(rec.Done)),
		xlsx.Str(rec.ChecksumRequest), xlsx.Str(rec.ChecksumResponse),
		xlsx.Str(rec.Host), xlsx.Str(rec.IP), xlsx.Str(rec.ClientHost),
		xlsx.Int(int64(rec.Attempts)), xlsx.Str(rec.AttemptsDetails), xlsx.Str(rec.NodeID),
		xlsx.Int(rec.RequestSize), xlsx.Int(rec.ResponseSize))
	s.cells = c
	return c
}

// bodyCell — тело, урезанное в ClickHouse до BodyChars символов. Урезанное
// получает видимый маркер с полной длиной: иначе по ячейке не понять, что
// тело неполное. Маркер помещается в предел ячейки за счёт тела.
//
// Предел ячейки Excel считается в единицах UTF-16, а срез ClickHouse — в
// символах: тело из BodyChars символов с эмодзи (две единицы на символ) в
// ячейку не влезает, и без проверки писатель обрезал бы его молча, без маркера.
// Длина в байтах не меньше длины в UTF-16, поэтому короткие тела не считаются.
func (s *xlsxLogSink) bodyCell(body string, fullRunes int64) xlsx.Cell {
	fits := len(body) <= xlsx.MaxCellChars || xlsx.UTF16Len(body) <= xlsx.MaxCellChars
	if fullRunes <= int64(s.meta.BodyChars) {
		if fits {
			return xlsx.Str(body)
		}
		s.unitCut++
	}
	marker := fmt.Sprintf(s.t("logs_export.cell_truncated"), fullRunes)
	limit := min(s.meta.BodyChars, xlsx.MaxCellChars)
	cut, _ := xlsx.Truncate(body, max(limit-xlsx.UTF16Len(marker), 0), "")
	return xlsx.Str(cut + marker)
}

// timeCell — дата-время в поясе браузера. Нулевое и эпоха (date_response
// незавершённого прогона) — пустая ячейка, а не 01.01.1970.
func (s *xlsxLogSink) timeCell(t time.Time) xlsx.Cell {
	if t.IsZero() || t.Unix() <= 0 {
		return xlsx.Empty()
	}
	return xlsx.DateTime(t.In(s.loc))
}

// Finish закрывает лист «Логи», пишет лист «Параметры» и завершает книгу.
// Неполная выгрузка открывается на листе «Параметры» (§115.8).
func (s *xlsxLogSink) Finish(_ context.Context, sum usecase.LogsExportSummary) error {
	if err := s.w.EndSheet(); err != nil {
		return fmt.Errorf("logs export: end sheet: %w", err)
	}
	err := s.w.StartSheet([]xlsx.Column{
		{Title: s.t("logs_export.param"), Width: 36}, {Title: s.t("logs_export.value"), Width: 80},
	}, xlsx.SheetOptions{FreezeHeader: true})
	if err != nil {
		return fmt.Errorf("logs export: start params: %w", err)
	}
	for _, p := range s.summaryRows(sum) {
		if err := s.w.WriteRow(xlsx.Str(p.label), p.value); err != nil {
			return fmt.Errorf("logs export: write params: %w", err)
		}
	}
	if sum.Status == usecase.LogsExportIncomplete {
		if err := s.w.SetActiveSheet(1); err != nil {
			return fmt.Errorf("logs export: activate params: %w", err)
		}
	}
	if err := s.w.Close(); err != nil {
		return fmt.Errorf("logs export: close workbook: %w", err)
	}
	s.c.Writer.Flush()
	return nil
}

// flush выталкивает накопленное из книги и HTTP-буфера клиенту.
func (s *xlsxLogSink) flush() error {
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("logs export: flush: %w", err)
	}
	s.c.Writer.Flush()
	return nil
}

// exportFileName — nexus-logs-<путь узла>-<YYYYMMDD-HHMMSS>.xlsx; только
// безопасные для заголовка символы.
func exportFileName(m usecase.LogsExportMeta, loc *time.Location) string {
	path := "node"
	if m.Node != nil {
		path = safeFilePart(strings.ReplaceAll(m.Node.Path, "/", "_"))
	}
	return fmt.Sprintf("nexus-logs-%s-%s.xlsx", path, m.StartedAt.In(loc).Format("20060102-150405"))
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// exportStatusKey — ключ i18n статуса полноты.
func exportStatusKey(st usecase.LogsExportStatus) string { return "logs_export.status." + string(st) }

// nodeLabel — путь узла и его ID для листа «Параметры».
func nodeLabel(n *domain.Node) string {
	if n == nil {
		return ""
	}
	return n.Path + " (" + n.ID + ")"
}
