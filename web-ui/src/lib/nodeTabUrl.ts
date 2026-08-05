import { type LogsInitialFilter } from "../components/node/LogsTab";
import { type LogsRange } from "../components/ui";
import { msToDatetimeLocal } from "./format";

// §79.3: активная вкладка узла живёт в адресе, а не в useState.
//
// Зачем: ссылку на конкретную вкладку («метрики этого узла») нельзя было ни
// переслать, ни открыть в новом окне — параметр ?tab= читался только как "logs"
// (§47.4) и никогда не писался. Вместе с вкладкой в адрес переезжает окно
// журнала (from/to/status/done): держать его в состоянии родителя, когда вкладка
// производна от URL, значит завести второй источник истины и разъехаться на
// «Назад».
//
// Все функции — чистые и работают НАД предыдущими параметрами: чужие ключи
// обязаны выживать (правило §54: модуль владеет только своими).

export type NodeTab = "overview" | "logs" | "config" | "metrics" | "queue";

export const NODE_TABS: NodeTab[] = ["overview", "logs", "config", "metrics", "queue"];

const TAB_PARAM = "tab";
// Ключи окна журнала. Живут только на вкладке «Логи»: уход с неё их удаляет,
// иначе адрес продолжает обещать окно, которого уже нет.
const LOG_WINDOW_PARAMS = ["from", "to", "status", "done"] as const;

const DEFAULT_TAB: NodeTab = "overview";

/** parseNodeTab — вкладка из адреса; мусор и пусто → «Обзор». */
export function parseNodeTab(raw: string | null): NodeTab {
  const tab = NODE_TABS.find((t) => t === raw);
  return tab ?? DEFAULT_TAB;
}

/** isKnownNodeTab — распознан ли параметр (нужно для нормализации мусора). */
export function isKnownNodeTab(raw: string | null): boolean {
  return raw !== null && NODE_TABS.some((t) => t === raw);
}

/**
 * parseLogsInitialFilter — окно и быстрые фильтры вкладки «Логи» из адреса.
 * from/to здесь в МИЛЛИСЕКУНДАХ (формат дип-линка §47.4 этого маршрута; на
 * рабочем столе те же имена значат RFC3339 — разные экраны, разные контракты).
 * Без окна возвращается null: LogsTab тогда открывается со своими дефолтами.
 */
export function parseLogsInitialFilter(params: URLSearchParams): LogsInitialFilter | null {
  const from = Number(params.get("from"));
  const to = Number(params.get("to"));
  const hasWindow =
    !!params.get("from") && !!params.get("to") && Number.isFinite(from) && Number.isFinite(to);
  const status = params.get("status");
  const done = params.get("done");
  if (!hasWindow && !status && !done) return null;
  return {
    ...(hasWindow ? { from: msToDatetimeLocal(from), to: msToDatetimeLocal(to) } : {}),
    status: status === "err" || status === "ok" ? status : "all",
    ...(done === "no" || done === "yes" ? { done } : {}),
  };
}

function dropLogWindow(next: URLSearchParams) {
  for (const key of LOG_WINDOW_PARAMS) next.delete(key);
}

/**
 * withNodeTab — переключение вкладки. Уход с «Логов» чистит окно журнала:
 * раньше это делал обработчик клика (setLogsFilter(null)), который при «Назад»
 * не срабатывал вовсе.
 */
export function withNodeTab(prev: URLSearchParams, tab: NodeTab): URLSearchParams {
  const next = new URLSearchParams(prev);
  next.set(TAB_PARAM, tab);
  if (tab !== "logs") dropLogWindow(next);
  return next;
}

/**
 * withLogsWindow — переход на «Логи» с временным окном столбца графика (§33.4):
 * клик по красному сегменту открывает только ошибки.
 */
export function withLogsWindow(prev: URLSearchParams, r: LogsRange): URLSearchParams {
  const next = new URLSearchParams(prev);
  next.set(TAB_PARAM, "logs");
  next.set("from", String(Math.round(r.from)));
  next.set("to", String(Math.round(r.to)));
  next.delete("done");
  if (r.onlyErrors) next.set("status", "err");
  else next.delete("status");
  return next;
}

/**
 * withFailedLogs — переход «Очередь → Логи» с фильтром недоставленных (§35).
 * from/to приходят в формате datetime-local, поэтому переводятся в мс.
 */
export function withFailedLogs(prev: URLSearchParams, f: LogsInitialFilter): URLSearchParams {
  const next = new URLSearchParams(prev);
  next.set(TAB_PARAM, "logs");
  const from = f.from ? Date.parse(f.from) : NaN;
  const to = f.to ? Date.parse(f.to) : NaN;
  if (Number.isFinite(from)) next.set("from", String(from));
  else next.delete("from");
  if (Number.isFinite(to)) next.set("to", String(to));
  else next.delete("to");
  if (f.status && f.status !== "all") next.set("status", f.status);
  else next.delete("status");
  if (f.done) next.set("done", f.done);
  else next.delete("done");
  return next;
}
