import { type LogsInitialFilter } from "../components/node/LogsTab";
import { type LogsRange } from "../components/ui";
import { msToDatetimeLocal } from "./format";
import {
  isChartStep,
  periodKey,
  PRESET_RANGES,
  type ChartStep,
  type Period,
  type PresetRange,
} from "./period";

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

// §84.2: ключи вида вкладки «Метрики» — период и шаг графика.
//
// Почему mfrom/mto, а не from/to. Делить ключи окна с журналом нельзя, и обе
// причины конструктивные:
//   1. parseLogsInitialFilter признаёт окно только при ОБЕИХ границах, а
//      метрики после §84.4 умеют одностороннее — журнал молча проглотил бы
//      такой адрес и показал не то окно, которое обещает строка;
//   2. правило «при переходе остаются только ключи целевой вкладки» требует
//      однозначного владельца ключа; общий from принадлежал бы обеим вкладкам,
//      и правило стало бы неопределённым.
// Переход «Метрики → Логи» окно не переносит намеренно: для этого есть явный
// жест — клик по столбцу графика (withLogsWindow, §33.4).
const METRICS_VIEW_PARAMS = ["range", "step", "mfrom", "mto"] as const;

// TAB_PARAMS — какие ключи адреса принадлежат вкладке. Обобщение прежнего
// частного правила «уход с Логов чистит окно журнала»: при переходе остаются
// только ключи ЦЕЛЕВОЙ вкладки, чужие ключи (team, utm_*, будущие) не
// трогаются вовсе — правило §54.
const TAB_PARAMS: Record<NodeTab, readonly string[]> = {
  overview: [],
  logs: LOG_WINDOW_PARAMS,
  config: [],
  metrics: METRICS_VIEW_PARAMS,
  queue: [],
};

// SHAREABLE_TAB_PARAMS — что попадает в ссылку «Поделиться» (§58).
//
// Окно и фильтры журнала в неё НЕ попадают и после §84.2: это сиюминутное
// состояние клика по графику, а не то, чем делятся (правило §79.3). Период и
// шаг метрик — наоборот, ровно то, чем делятся («вот метрики этого узла за
// 14 суток по суткам»).
const SHAREABLE_TAB_PARAMS: Partial<Record<NodeTab, readonly string[]>> = {
  metrics: METRICS_VIEW_PARAMS,
};

/** shareableTabParams — ключи вкладки, которые несёт ссылка «Поделиться». */
export function shareableTabParams(tab: NodeTab): readonly string[] {
  return SHAREABLE_TAB_PARAMS[tab] ?? [];
}

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

// ownedParams — все ключи, которыми владеет хоть одна вкладка. Только они
// подлежат чистке при переходе; всё остальное в адресе — чужое.
const ownedParams: readonly string[] = [...new Set(Object.values(TAB_PARAMS).flat())];

/**
 * gotoTab — общая часть ВСЕХ переходов между вкладками: поставить вкладку и
 * убрать ключи чужих вкладок.
 *
 * Общая намеренно. Переходов три (withNodeTab, withLogsWindow, withFailedLogs),
 * и раньше чистку делал только первый — то есть клик по столбцу графика
 * оставлял в адресе период и шаг метрик, и адрес обещал вид вкладки, на
 * которой пользователь уже не находится. Правило владения обязано быть одно.
 */
function gotoTab(prev: URLSearchParams, tab: NodeTab): URLSearchParams {
  const next = new URLSearchParams(prev);
  next.set(TAB_PARAM, tab);
  const keep = new Set(TAB_PARAMS[tab]);
  for (const key of ownedParams) {
    if (!keep.has(key)) next.delete(key);
  }
  return next;
}

/**
 * withNodeTab — переключение вкладки. Остаются только ключи целевой вкладки:
 * уход с «Логов» чистит окно журнала (раньше это делал обработчик клика,
 * который при «Назад» не срабатывал вовсе), уход с «Метрик» — период и шаг.
 *
 * Адрес, обещающий окно или период, которых на текущей вкладке уже нет, —
 * это второй источник истины, и он разъедется на первой же кнопке «Назад».
 */
export function withNodeTab(prev: URLSearchParams, tab: NodeTab): URLSearchParams {
  return gotoTab(prev, tab);
}

/**
 * parseMetricsView — период и шаг вкладки «Метрики» из адреса (§84.2).
 *
 * Возвращает только то, что в адресе ЕСТЬ: недостающее вызывающая сторона
 * добирает по своей цепочке приоритетов (адрес → преф узла → системный
 * дефолт, §84.3). Мусор игнорируется молча — на графике не то место, где
 * стоит показывать ошибку разбора необязательного параметра.
 *
 * mfrom/mto — миллисекунды, как и у окна журнала на этом же маршруте (§47.4):
 * два формата времени в одной строке адреса читать невозможно.
 */
export function parseMetricsView(params: URLSearchParams): {
  period?: Period;
  step?: ChartStep;
} {
  const out: { period?: Period; step?: ChartStep } = {};

  const rawFrom = params.get("mfrom");
  const rawTo = params.get("mto");
  const from = Number(rawFrom);
  const to = Number(rawTo);
  if (rawFrom && rawTo && Number.isFinite(from) && Number.isFinite(to) && from < to) {
    out.period = { kind: "custom", from: new Date(from).toISOString(), to: new Date(to).toISOString() };
  } else {
    const range = params.get("range");
    if (range && (PRESET_RANGES as string[]).includes(range)) {
      out.period = { kind: "preset", range: range as PresetRange };
    }
  }

  const step = params.get("step");
  if (isChartStep(step)) out.step = step;
  return out;
}

/**
 * withMetricsView — записать период и шаг метрик в адрес (§84.2).
 *
 * В адрес идут только ОТЛИЧИЯ от действующего дефолта: ссылка на дефолтный вид
 * обязана оставаться короткой, иначе «Поделиться» отдаёт мусор из параметров,
 * которые и так подразумеваются. Тот же приём, что у фильтров рабочего стола
 * (§54.serializeFilters). Без defaults период пишется всегда.
 *
 * step НЕОБЯЗАТЕЛЕН, и это существенно: отсутствие ключа означает «шаг
 * неявный, берётся дефолтом периода». Различать неявный шаг и явно выбранный
 * обязательно — иначе дефолт одного периода (скажем, 6 ч у семи суток) при
 * переключении на сутки переезжает туда УЖЕ КАК ВЫБОР пользователя и
 * закрепляется в адресе, хотя тот его никогда не выбирал.
 *
 * Форма функциональная, над предыдущими параметрами: чужие ключи выживают.
 */
export function withMetricsView(
  prev: URLSearchParams,
  period: Period,
  step?: ChartStep,
  defaults?: { period: Period },
): URLSearchParams {
  const next = new URLSearchParams(prev);
  next.set(TAB_PARAM, "metrics");

  for (const key of METRICS_VIEW_PARAMS) next.delete(key);

  if (!defaults || periodKey(defaults.period) !== periodKey(period)) {
    if (period.kind === "preset") {
      next.set("range", period.range);
    } else {
      const from = Date.parse(period.from);
      const to = Date.parse(period.to);
      if (Number.isFinite(from) && Number.isFinite(to)) {
        next.set("mfrom", String(from));
        next.set("mto", String(to));
      }
    }
  }
  if (step !== undefined) next.set("step", step);
  return next;
}

/**
 * withLogsWindow — переход на «Логи» с временным окном столбца графика (§33.4):
 * клик по красному сегменту открывает только ошибки.
 */
export function withLogsWindow(prev: URLSearchParams, r: LogsRange): URLSearchParams {
  const next = gotoTab(prev, "logs");
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
  const next = gotoTab(prev, "logs");
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
