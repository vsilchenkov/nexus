// Общие типы строки лога узла (§7.4 / §21).
export type LogRow = {
  id: string;
  url: string;
  http_method?: string; // §39: HTTP-глагол (GET/POST/…)
  method: string; // §39: подпуть запроса (хвост passthrough); пусто у обычных узлов
  status: number;
  duration_ms: number;
  date_request: string;
  done: boolean;
  reason?: string;
};
// logs_available=false — backend логов (ClickHouse) временно недоступен:
// бэкенд отдаёт 200 с пустым items + этот флаг (а не 500), чтобы поллинг UI
// не флудил ошибками. UI показывает индикатор «логи временно недоступны».
export type LogsResp = { items: LogRow[]; logs_available?: boolean };

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
