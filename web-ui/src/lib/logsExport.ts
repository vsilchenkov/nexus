// logsExport — чистые функции формы выгрузки журнала узла в Excel (§115.4):
// параметры запроса, выбор предела строк, клиентская оценка «в файл/объём» и
// тексты отказов. Компонент диалога держит только состояние и разметку.

import type { TFunction } from "i18next";

import { logsFilterParams, type LogsFilterState } from "./logsQuery";

// LogsExportEstimate — ответ GET /api/nodes/{id}/logs/export/estimate (§115.5).
// request_bytes/response_bytes — суммы размеров тел по ВСЕМ прогонам под
// фильтром, каждый урезан капом ячейки: верхняя граница, а не точный вес.
export type LogsExportEstimate = {
  records: number;
  rows: number;
  request_bytes: number;
  response_bytes: number;
  max_rows: number;
  max_bytes: number;
  max_body_chars: number;
  logs_configured: boolean;
  logs_available: boolean;
};

// ExportRowsChoice — выбор предела записей: два пресета или потолок сервера.
export type ExportRowsChoice = "1000" | "10000" | "max";

const ROW_PRESETS: ExportRowsChoice[] = ["1000", "10000"];

/** exportRowLimit — сколько записей просить у сервера. Пресет выше потолка
 * сервера урезается до потолка: сервер сделал бы то же, но форма показала бы
 * неправду. Потолок неизвестен (maxRows ≤ 0: оценка не пришла) — пресет как
 * есть, «все» — 0, то есть «потолок сервера». */
export function exportRowLimit(choice: ExportRowsChoice, maxRows: number): number {
  if (choice === "max") return Math.max(maxRows, 0);
  return maxRows > 0 ? Math.min(Number(choice), maxRows) : Number(choice);
}

/** exportRowChoices — пресеты меньше потолка плюс «все». Пресет не меньше
 * потолка совпал бы с «все» — две одинаковые кнопки сбивают с толку. */
export function exportRowChoices(maxRows: number): ExportRowsChoice[] {
  return [...ROW_PRESETS.filter((c) => Number(c) < maxRows), "max"];
}

// EXPORT_ROW_META_BYTES — оценка вклада всех колонок, кроме тел, в бюджет
// объёма: ID и node_id по 36 байт, две контрольные суммы, url, method,
// parameters, адреса клиента и служебные 64 байта строки сервера
// (exportRowOverhead). Грубо: точный вес строк знает только ClickHouse.
export const EXPORT_ROW_META_BYTES = 400;

export type ExportPlan = {
  /** Сколько записей уйдёт в файл при данном пределе строк. */
  inFile: number;
  /** Оценка текста ячеек для inFile записей, байт. */
  bytes: number;
  /** Под фильтром записей больше предела строк — в файл попадут самые новые. */
  limitedByRows: boolean;
  /** Оценка объёма выше max_bytes — сервер остановится раньше. */
  limitedByBytes: boolean;
  /** Сколько записей примерно поместится в max_bytes (= inFile без упора). */
  fitRows: number;
};

/** exportPlan — что окажется в файле по оценке сервера. Тела считаются по
 * среднему на прогон: запись выгружается одним (новейшим) прогоном, поэтому
 * среднее на прогон — и среднее на запись. */
export function exportPlan(est: LogsExportEstimate, rowLimit: number, bodies: boolean): ExportPlan {
  const inFile = Math.max(0, Math.min(est.records, rowLimit));
  const bodyPerRow = bodies && est.rows > 0 ? (est.request_bytes + est.response_bytes) / est.rows : 0;
  const perRow = EXPORT_ROW_META_BYTES + bodyPerRow;
  const bytes = Math.round(inFile * perRow);
  const limitedByBytes = bytes > est.max_bytes;
  return {
    inFile,
    bytes,
    limitedByRows: est.records > rowLimit,
    limitedByBytes,
    fitRows: limitedByBytes ? Math.floor(est.max_bytes / perRow) : inFile,
  };
}

/** exportParams — query-параметры выгрузки: фильтр журнала (тот же
 * logsFilterParams, что у списка и счётчика, §72.2) плюс предел, тела и
 * часовой пояс дат файла. rows ≤ 0 не отправляется — сервер берёт max_rows. */
export function exportParams(
  f: LogsFilterState,
  o: { rows: number; bodies: boolean; tz: string },
): Record<string, string> {
  const p = logsFilterParams(f);
  if (o.rows > 0) p.rows = String(o.rows);
  if (o.bodies) p.bodies = "1";
  if (o.tz) p.tz = o.tz;
  return p;
}

/** browserTimeZone — IANA-пояс браузера: даты в файле — в нём, как на
 * вкладке. Недоступен (старый движок) → "" → сервер пишет UTC. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

/** isExportCanceled — запрос оборван «Остановить» или закрытием диалога. */
export function isExportCanceled(e: unknown): boolean {
  return (e as { code?: string } | null)?.code === "ERR_CANCELED";
}

/** exportErrorText — отказ сервера до начала файла (§115.5). 503 приходит в
 * двух смыслах (слоты реплики заняты / ClickHouse недоступен) — их различает
 * текст сервера, переведённый на язык интерфейса (Accept-Language запроса). */
export function exportErrorText(e: unknown, t: TFunction): string {
  const res = (e as { response?: { status?: number; data?: { error?: string } } } | null)?.response;
  switch (res?.status) {
    case 400:
      return t("logs.advanced.bad_query");
    case 429:
      return t("logs.export.err_rate");
    default:
      return res?.data?.error ?? t("logs.export.err_generic");
  }
}
