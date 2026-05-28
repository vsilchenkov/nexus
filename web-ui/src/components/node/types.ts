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
