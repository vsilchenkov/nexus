package http

import (
	"fmt"
	"strings"
	"time"

	"nexus/internal/platform/xlsx"
	"nexus/internal/web/usecase"
)

// paramRow — строка листа «Параметры»: подпись и значение.
type paramRow struct {
	label string
	value xlsx.Cell
}

// summaryRows — лист «Параметры» (§115.3): что выгружено, по каким фильтрам,
// с какими лимитами и насколько полно.
func (s *xlsxLogSink) summaryRows(sum usecase.LogsExportSummary) []paramRow {
	rows := make([]paramRow, 0, 32)
	rows = append(rows, s.resultRows(sum)...)
	rows = append(rows, s.filterRows()...)
	return append(rows, s.contextRows(sum)...)
}

// resultRows — исход: статус, объём, обрезки.
func (s *xlsxLogSink) resultRows(sum usecase.LogsExportSummary) []paramRow {
	m := s.meta
	status := s.t(exportStatusKey(sum.Status))
	if sum.Reason != "" && sum.Status == usecase.LogsExportIncomplete {
		status += " — " + s.t("logs_export.reason."+sum.Reason)
	}
	bodies := s.t("logs_export.bodies_off")
	if m.Bodies {
		bodies = fmt.Sprintf(s.t("logs_export.bodies_on"), m.BodyChars)
	}
	return []paramRow{
		{s.t("logs_export.p.status"), xlsx.Str(status)},
		{s.t("logs_export.p.written"), xlsx.Int(int64(sum.Written))},
		{s.t("logs_export.p.selected"), xlsx.Int(int64(m.Selected))},
		{s.t("logs_export.p.more"), xlsx.Str(s.yesNo(m.More))},
		{s.t("logs_export.p.bytes"), xlsx.Int(sum.Bytes)},
		{s.t("logs_export.p.bodies"), xlsx.Str(bodies)},
		{s.t("logs_export.p.truncated_bodies"), xlsx.Int(int64(sum.TruncatedBodies))},
		{s.t("logs_export.p.truncated_cells"), xlsx.Int(int64(s.w.TruncatedCells()))},
		{s.t("logs_export.p.vanished"), xlsx.Int(int64(sum.Vanished))},
		{s.t("logs_export.p.max_rows"), xlsx.Int(int64(m.Rows))},
		{s.t("logs_export.p.max_bytes"), xlsx.Int(m.Limits.MaxBytes)},
	}
}

// filterRows — узел и фильтры журнала, под которыми шла выгрузка.
func (s *xlsxLogSink) filterRows() []paramRow {
	q := s.meta.Query
	search := q.Q
	if modes := exportSearchModes(q.QCase, q.QWord, q.QRegex); search != "" && len(modes) > 0 {
		search += "  [" + strings.Join(modes, " ") + "]"
	}
	table := ""
	if s.meta.Node != nil {
		table = s.meta.Node.ClickHouseTable
	}
	return []paramRow{
		{s.t("logs_export.p.node"), xlsx.Str(nodeLabel(s.meta.Node))},
		{s.t("logs_export.p.table"), xlsx.Str(table)},
		{s.t("logs_export.p.from"), s.boundCell(q.SinceMs)},
		{s.t("logs_export.p.to"), s.boundCell(q.UntilMs)},
		{s.t("logs_export.p.search"), xlsx.Str(search)},
		{s.t("logs_export.p.state"), xlsx.Str(s.t("logs_export.state." + orAll(q.Status)))},
		{s.t("logs_export.p.done"), xlsx.Str(s.t("logs_export.done." + orAll(q.Done)))},
		{s.t("logs_export.p.method"), xlsx.Str(q.Method)},
		{s.t("logs_export.p.client"), xlsx.Str(strings.TrimSpace(q.ClientHost + " " + q.IP + " " + q.Host))},
	}
}

// contextRows — кто, когда, в каком поясе и какой версией.
func (s *xlsxLogSink) contextRows(sum usecase.LogsExportSummary) []paramRow {
	return []paramRow{
		{s.t("logs_export.p.user"), xlsx.Str(s.meta.Actor.UserLogin)},
		{s.t("logs_export.p.started"), s.timeCell(s.meta.StartedAt)},
		{s.t("logs_export.p.finished"), s.timeCell(sum.FinishedAt)},
		{s.t("logs_export.p.tz"), xlsx.Str(s.loc.String())},
		{s.t("logs_export.p.version"), xlsx.Str(s.version)},
	}
}

// boundCell — граница периода: дата-время или «не задана».
func (s *xlsxLogSink) boundCell(ms int64) xlsx.Cell {
	if ms <= 0 {
		return xlsx.Str(s.t("logs_export.unbounded"))
	}
	return xlsx.DateTime(time.UnixMilli(ms).In(s.loc))
}

func (s *xlsxLogSink) yesNo(b bool) string {
	if b {
		return s.t("logs_export.yes")
	}
	return s.t("logs_export.no")
}

// exportSearchModes — кнопки режима поиска (§48.2) в порядке интерфейса.
func exportSearchModes(caseSensitive, word, regex bool) []string {
	var out []string
	for _, m := range []struct {
		on   bool
		name string
	}{{caseSensitive, "Aa"}, {word, "ab|"}, {regex, ".*"}} {
		if m.on {
			out = append(out, m.name)
		}
	}
	return out
}

// orAll — пустой фильтр значит «все».
func orAll(v string) string {
	if v == "" {
		return "all"
	}
	return v
}
