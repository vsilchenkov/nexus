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
// Ключи окна и фильтров журнала. Живут только на вкладке «Логи»: уход с неё их
// удаляет, иначе адрес продолжает обещать окно, которого уже нет.
//
// §102.6: к окну добавились фильтры — метод, полнотекстовый запрос с режимами и
// хост клиента. Раньше переход «Метрики → Журнал» переносил только from/to/
// status/done, а расширенные фильтры вкладки «Метрики» (§79.4) терялись: тултип
// обещал число, посчитанное ПОД ними, а журнал открывался без них и показывал
// другое. Ключи названы так же, как параметры API (logsFilterParams), чтобы
// адрес читался рядом с сетевым запросом без словаря соответствий.
//
// §105.4: ip — значение фильтра «Клиент». client_host остаётся: его ставит
// значение без IP (внешние таблицы §64) и несут старые ссылки.
const LOG_WINDOW_PARAMS = [
  "from",
  "to",
  "status",
  "done",
  "method",
  "q",
  "q_case",
  "q_word",
  "q_regex",
  "client_host",
  "ip",
] as const;

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

// LogsLinkFilter — фильтры журнала, переносимые ссылкой вместе с окном (§102.6).
// Ровно то же множество, что панель расширенных фильтров (§79.4/§48/§67):
// рейтинг методов и клик по столбцу графика считаются ПОД ними, и ссылка обязана
// нести их целиком, иначе обещанное число в тултипе расходится с журналом.
export type LogsLinkFilter = {
  method?: string;
  q?: string;
  qCase?: boolean;
  qWord?: boolean;
  qRegex?: boolean;
  clientIp?: string;
  clientHost?: string;
};

// applyLogsFilter — записать фильтры ссылки, ЯВНО удалив незаданные.
//
// Удаление обязательно и не является перестраховкой: ключи живут на вкладке
// «Логи», и переход, который их не задаёт (клик по столбцу графика §33.4,
// «Очередь → Логи» §35), унёс бы в пересланном адресе чужой фильтр — журнал
// показал бы подмножество под видом целого.
function applyLogsFilter(next: URLSearchParams, f?: LogsLinkFilter): URLSearchParams {
  const setOrDelete = (key: string, value: string) => {
    if (value) next.set(key, value);
    else next.delete(key);
  };
  setOrDelete("method", f?.method ?? "");
  setOrDelete("ip", f?.clientIp ?? "");
  setOrDelete("client_host", f?.clientHost ?? "");
  // Режимы поиска без самого запроса бессмысленны: «регистрозависимо» без
  // строки — это фильтр ни по чему, и в адресе он только вводит в заблуждение.
  const q = f?.q ?? "";
  setOrDelete("q", q);
  setOrDelete("q_case", q && f?.qCase ? "1" : "");
  setOrDelete("q_word", q && f?.qWord ? "1" : "");
  setOrDelete("q_regex", q && f?.qRegex ? "1" : "");
  return next;
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
  // §102.6: фильтры вкладки «Метрики» переезжают вместе с окном.
  const method = params.get("method") ?? "";
  const q = params.get("q") ?? "";
  const clientHost = params.get("client_host") ?? "";
  const clientIp = params.get("ip") ?? ""; // §105.4
  if (!hasWindow && !status && !done && !method && !q && !clientHost && !clientIp) return null;
  return {
    ...(hasWindow ? { from: msToDatetimeLocal(from), to: msToDatetimeLocal(to) } : {}),
    status: status === "err" || status === "ok" ? status : "all",
    ...(done === "no" || done === "yes" ? { done } : {}),
    ...(method ? { method } : {}),
    ...(clientHost ? { clientHost } : {}),
    ...(clientIp ? { clientIp } : {}),
    // Режимы поиска читаются только вместе с запросом — см. applyLogsFilter.
    ...(q
      ? {
          q,
          qCase: params.get("q_case") === "1",
          qWord: params.get("q_word") === "1",
          qRegex: params.get("q_regex") === "1",
        }
      : {}),
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
 *
 * §102.6: filter — фильтры вкладки-источника. Столбец посчитан ПОД ними, и без
 * переноса журнал открывался шире, чем обещала высота столбца. Не задан —
 * ключи фильтров явно удаляются (см. applyLogsFilter).
 */
export function withLogsWindow(
  prev: URLSearchParams,
  r: LogsRange,
  filter?: LogsLinkFilter,
  status: "all" | "ok" | "err" = "all",
): URLSearchParams {
  const next = gotoTab(prev, "logs");
  next.set("from", String(Math.round(r.from)));
  next.set("to", String(Math.round(r.to)));
  next.delete("done");
  // Клик по красному сегменту всегда значит «только ошибки»; в остальном
  // действует быстрый фильтр вкладки-источника — под ним столбец и посчитан.
  const eff = r.onlyErrors ? "err" : status;
  if (eff === "all") next.delete("status");
  else next.set("status", eff);
  return applyLogsFilter(next, filter);
}

/**
 * withLogsMethod — переход «Метрики → Логи» по строке рейтинга методов (§102.6).
 *
 * Окно берётся из периода метрик, фильтры — из применённых на вкладке, метод
 * дописывается поверх: тултип строки посчитан ровно под этим набором.
 * status="all" выражается отсутствием ключа — так же, как у окна столбца.
 */
export function withLogsMethod(
  prev: URLSearchParams,
  win: { from: number; to: number },
  method: string,
  filter?: LogsLinkFilter,
  status: "all" | "ok" | "err" = "all",
): URLSearchParams {
  const next = gotoTab(prev, "logs");
  next.set("from", String(Math.round(win.from)));
  next.set("to", String(Math.round(win.to)));
  next.delete("done");
  if (status === "all") next.delete("status");
  else next.set("status", status);
  return applyLogsFilter(next, { ...filter, method });
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
  // «Очередь → Логи» своих фильтров не имеет: ключи §102.6 здесь ОБЯЗАНЫ
  // очиститься, иначе пересланный адрес утащит в журнал чужой фильтр.
  return applyLogsFilter(next, f);
}
