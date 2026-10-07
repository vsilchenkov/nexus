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
  /** Оценка текста ячеек для inFile записей по среднему размеру записи, байт. */
  bytes: number;
  /** Под фильтром записей больше предела строк — в файл попадут самые новые. */
  limitedByRows: boolean;
  /** Оценка объёма выше max_bytes — выгрузка МОЖЕТ остановиться раньше. */
  limitedByBytes: boolean;
};

/** exportPlan — что окажется в файле по оценке сервера. Тела считаются по
 * среднему на прогон: запись выгружается одним (новейшим) прогоном, поэтому
 * среднее на прогон — и среднее на запись.
 *
 * Сколько записей войдёт до упора в объём, отсюда НЕ следует: записи пишутся
 * от новых к старым, а среднее посчитано по всем найденным. Если тяжёлые тела
 * у старых записей, войдёт куда больше «среднего» — стенд §115 показал
 * «≈ 397» при фактических 1 579. Поэтому форма предупреждает «может
 * остановиться раньше», а не называет число. */
export function exportPlan(est: LogsExportEstimate, rowLimit: number, bodies: boolean): ExportPlan {
  const inFile = Math.max(0, Math.min(est.records, rowLimit));
  const bodyPerRow = bodies && est.rows > 0 ? (est.request_bytes + est.response_bytes) / est.rows : 0;
  const bytes = Math.round(inFile * (EXPORT_ROW_META_BYTES + bodyPerRow));
  return { inFile, bytes, limitedByRows: est.records > rowLimit, limitedByBytes: bytes > est.max_bytes };
}

// ExportFilterItem — строка сводки фильтров вкладки в форме выгрузки (§115.4).
// muted — фильтр не задан (значение — «любой»/«не задан»).
export type ExportFilterItem = { label: string; value: string; muted: boolean };

// localDateTime — значение поля даты (datetime-local, «2026-10-07T00:00») как
// «07.10.2026 00:00». Строкой, без Date: поле уже в локальной зоне.
function localDateTime(v: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}:\d{2})/.exec(v);
  return m ? `${m[3]}.${m[2]}.${m[1]} ${m[4]}` : v;
}

function periodText(f: LogsFilterState, t: TFunction): string {
  const from = f.from ? localDateTime(f.from) : "";
  const to = f.to ? localDateTime(f.to) : "";
  if (from && to) return `${from} — ${to}`;
  if (from) return t("logs.export.period_from", { from });
  if (to) return t("logs.export.period_to", { to });
  return "";
}

/** exportFilterSummary — фильтры вкладки строками для формы выгрузки. Форма
 * их не правит: выгружается ровно то, что показывает журнал. */
export function exportFilterSummary(f: LogsFilterState, t: TFunction): ExportFilterItem[] {
  const any = t("logs.export.any");
  const modes = [f.qCase && "Aa", f.qWord && "ab|", f.qRegex && ".*"].filter(Boolean).join(" ");
  const rows: { label: string; value: string; empty: string }[] = [
    { label: t("logs.advanced.q"), value: f.q && modes ? `${f.q}  [${modes}]` : f.q, empty: t("logs.export.none") },
    { label: t("logs.export.period"), value: periodText(f, t), empty: t("logs.export.period_none") },
    {
      label: t("logs.export.state"),
      value: f.status === "all" ? "" : t(`logs.filter.${f.status}`),
      empty: t("logs.filter.all"),
    },
    { label: t("logs.advanced.method"), value: f.method, empty: any },
    { label: t("logs.advanced.client"), value: f.clientIp || f.clientHost, empty: any },
  ];
  // done приходит только дип-линком «Очереди» (§77.4) — показывается, только
  // когда задан, как чип в шапке журнала.
  if (f.done !== "all") {
    const done = f.done === "done" ? t("logs.filter.done_yes") : t("logs.filter.done_no");
    rows.push({ label: t("logs.export.run"), value: done, empty: "" });
  }
  return rows.map((r) => ({ label: r.label, value: r.value || r.empty, muted: !r.value }));
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
