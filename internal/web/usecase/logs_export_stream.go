package usecase

import (
	"context"
	"errors"
	"unicode/utf8"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

const (
	// exportBatchRows — записей в батче без тел: метаданные лёгкие, батч
	// ограничен длиной `ID IN (…)`, а не памятью.
	exportBatchRows = 100
	// exportBatchRowsBodies — записей в батче с телами: два тела по ≤ 32 767
	// символов на запись держат батч в пределах ~13 МиБ памяти Web.
	exportBatchRowsBodies = 50
	// exportRowOverhead — байт на числа, даты и флаги строки в учёте бюджета.
	exportRowOverhead = 64
	// exportAuditQueryChars — сколько символов поисковой строки идёт в аудит.
	exportAuditQueryChars = 200
)

// stream — чтение строк батчами и запись в sink (§115.7, шаг 2; §115.8).
func (u *LogsExportUsecase) stream(
	ctx context.Context, q port.LogQuery, keys []port.LogExportKey, meta LogsExportMeta, sink LogsExportSink,
) LogsExportSummary {
	var s LogsExportSummary
	if err := sink.Begin(ctx, meta); err != nil {
		return u.aborted(meta, s, "begin", err)
	}
	batch := exportBatchRows
	if meta.Bodies {
		batch = exportBatchRowsBodies
	}
	for i := 0; i < len(keys) && s.Status == ""; i += batch {
		chunk := keys[i:min(i+batch, len(keys))]
		rows, err := u.readBatch(ctx, q, chunk, meta.BodyChars)
		if err != nil {
			if ctx.Err() != nil {
				return u.aborted(meta, s, "read", err)
			}
			s.Status, s.Reason = LogsExportIncomplete, exportFailReason(err)
			u.d.Logger.Warn("logs export incomplete: clickhouse failed mid-stream",
				u.d.Logger.Str("node_id", meta.Node.ID),
				u.d.Logger.Int("written", s.Written),
				u.d.Logger.Err(err))
			break
		}
		s.Vanished += len(chunk) - len(rows)
		if err := u.writeRows(ctx, sink, rows, meta, &s); err != nil {
			return u.aborted(meta, s, "write", err)
		}
	}
	if s.Status == "" {
		s.Status = LogsExportComplete
		if meta.More {
			s.Status = LogsExportLimitedRows
		}
	}
	s.FinishedAt = u.d.Clock.Now()
	if err := sink.Finish(ctx, s); err != nil {
		return u.aborted(meta, s, "finish", err)
	}
	return s
}

// readBatch — строки одного батча ключей со своим таймаутом.
func (u *LogsExportUsecase) readBatch(
	ctx context.Context, q port.LogQuery, keys []port.LogExportKey, bodyRunes int,
) ([]port.LogExportRow, error) {
	cctx, cancel := u.queryCtx(ctx)
	defer cancel()
	return u.d.Reader.ExportRows(cctx, q, keys, bodyRunes)
}

// writeRows пишет строки батча, пока не кончится бюджет MaxBytes. Строка,
// которая бюджет превысила бы, не пишется: файл не больше обещанного.
func (u *LogsExportUsecase) writeRows(
	ctx context.Context, sink LogsExportSink, rows []port.LogExportRow, meta LogsExportMeta, s *LogsExportSummary,
) error {
	for _, r := range rows {
		size := exportRowBytes(&r.Rec)
		if s.Bytes+size > meta.Limits.MaxBytes {
			s.Status = LogsExportLimitedBytes
			u.d.Logger.Debug("logs export: byte budget exhausted",
				u.d.Logger.Str("node_id", meta.Node.ID),
				u.d.Logger.Int("written", s.Written),
				u.d.Logger.Any("bytes", s.Bytes),
				u.d.Logger.Any("next_row_bytes", size))
			return nil
		}
		if err := sink.Row(ctx, r); err != nil {
			return err
		}
		s.Written++
		s.Bytes += size
		if meta.BodyChars > 0 {
			s.TruncatedBodies += countCut(r.ReqRunes, meta.BodyChars) + countCut(r.RespRunes, meta.BodyChars)
		}
	}
	return nil
}

// countCut — 1, если тело длиннее среза ячейки.
func countCut(fullRunes int64, limit int) int {
	if fullRunes > int64(limit) {
		return 1
	}
	return 0
}

// aborted — получатель ушёл: файл не дописывается, итог идёт только в аудит.
func (u *LogsExportUsecase) aborted(meta LogsExportMeta, s LogsExportSummary, stage string, err error) LogsExportSummary {
	s.Status, s.Reason, s.FinishedAt = LogsExportAborted, stage, u.d.Clock.Now()
	u.d.Logger.Debug("logs export aborted",
		u.d.Logger.Str("node_id", meta.Node.ID),
		u.d.Logger.Str("stage", stage),
		u.d.Logger.Int("written", s.Written),
		u.d.Logger.Err(err))
	return s
}

// exportFailReason — код причины неполной выгрузки для листа «Параметры» и
// аудита. Текст ошибки туда не идёт: в нём бывает SQL с аргументами поиска.
func exportFailReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, domain.ErrLogsBackendUnavailable):
		return "unavailable"
	default:
		return "query_failed"
	}
}

// exportRowBytes — вклад строки в бюджет MaxBytes: байты UTF-8 текстовых
// ячеек плюс постоянная надбавка на числа и даты.
func exportRowBytes(r *domain.LogRecord) int64 {
	n := len(r.ID) + len(r.Type) + len(r.HTTPMethod) + len(r.URL) + len(r.Method) + len(r.Parameters) +
		len(r.Request) + len(r.Response) + len(r.Reason) + len(r.ChecksumRequest) + len(r.ChecksumResponse) +
		len(r.Host) + len(r.IP) + len(r.ClientHost) + len(r.AttemptsDetails) + len(r.NodeID)
	return int64(n + exportRowOverhead)
}

// exportAuditDetails — детали записи аудита (§115.9): фильтры, объём, исход.
// Тела и значения ячеек сюда не попадают; поисковая строка урезана.
func exportAuditDetails(meta LogsExportMeta, s LogsExportSummary) map[string]any {
	q := meta.Query
	d := map[string]any{
		"result":   string(s.Status),
		"rows":     s.Written,
		"bytes":    s.Bytes,
		"selected": meta.Selected,
		"bodies":   meta.Bodies,
		"from":     q.SinceMs,
		"to":       q.UntilMs,
	}
	if s.Reason != "" {
		d["reason"] = s.Reason
	}
	for k, v := range map[string]string{
		"q": truncateRunes(q.Q, exportAuditQueryChars), "status": q.Status, "done": q.Done,
		"method": q.Method, "ip": q.IP, "host": q.Host, "client_host": q.ClientHost,
	} {
		if v != "" {
			d[k] = v
		}
	}
	if q.Q != "" {
		d["q_case"], d["q_word"], d["q_regex"] = q.QCase, q.QWord, q.QRegex
	}
	return d
}

// truncateRunes — не больше n символов, без разрыва UTF-8.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
