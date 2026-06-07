// Общие типы строки лога узла (§7.4 / §21).
export type LogRow = {
  id: string;
  url: string;
  method: string;
  status: number;
  duration_ms: number;
  date_request: string;
  done: boolean;
  reason?: string;
};
export type LogsResp = { items: LogRow[] };

// LogDetail — полная запись лога с телами request/response. Тянется лениво
// по клику на строку через GET /api/nodes/:id/log/:logId (§7.4.1): списки
// (LogRow) отдают только метаданные, чтобы большие JSON-тела не грузились
// разом и не вешали фронт.
export type LogDetail = LogRow & {
  type?: string;
  parameters?: string;
  request?: string;
  response?: string;
  date_response?: string;
  checksum_request?: string;
  checksum_response?: string;
  host?: string;
  ip?: string;
  attempts?: number;
  attempts_details?: string;
};
