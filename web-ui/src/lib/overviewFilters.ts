// Фильтры рабочего стола (§54): поиск / корневой метод / статус / живой период.
//
// Модель — гибрид: URL источник истины (конвенция AuditLog, §54.2), sessionStorage
// зеркало на случай перехода, теряющего query-строку (кнопка «Узлы» в сайдбаре —
// NavLink to="/" без параметров). Канонический формат в обоих местах один и тот же —
// сериализованная query-строка, поэтому восстановление сводится к
// parse → serialize → setParams и заодно санитизирует мусор из хранилища.
import { PRESET_RANGES, periodKey, type Period, type PresetRange } from "./period";

export const METHOD_FILTERS = ["request", "requestAsync", "RabbitMQAsync"] as const;
export type MethodFilter = "" | (typeof METHOD_FILTERS)[number];

export const STATUS_FILTERS = [
  "all",
  "ok",
  "warn",
  "degraded",
  "err",
  "paused",
  "disabled",
] as const;
export type StatusFilter = (typeof STATUS_FILTERS)[number];

export type OverviewFilters = {
  search: string;
  method: MethodFilter;
  status: StatusFilter;
  // period — «живой» выбор периода, не пользовательский дефолт-звёздочка (§44.B).
  period: Period;
};

// FILTER_PARAM_KEYS — query-параметры, которыми владеет Overview. Чужие ключи
// (напр. utm-метки) при обновлении фильтров не трогаются.
export const FILTER_PARAM_KEYS = ["q", "method", "status", "range", "from", "to"] as const;

// FILTERS_STORAGE — тип хранилища зеркала (§54.3). sessionStorage, а не
// localStorage: иначе сохранённый живой период всегда перекрывал бы дефолт
// «под себя» (§44.B/§71), а фильтр «прилипал» бы на недели (непонятно пустой
// список при заходе).
//
// Период в зеркало НЕ попадает вовсе (§71): он живёт ровно на текущем экране —
// уход со страницы и возврат по кнопке «Узлы» показывают дефолт команды, а не
// период, выбранный когда-то раньше. Поиск/метод/статус зеркалятся как прежде.
const FILTERS_STORAGE: "session" | "local" = "session";
const FILTERS_KEY = "nexus.overview.filters";

function storage(): Storage {
  return FILTERS_STORAGE === "session" ? window.sessionStorage : window.localStorage;
}

// defaultFilters — дефолтное состояние фильтров.
//
// Дефолтный период приходит ПАРАМЕТРОМ, а не читается здесь (§71): он живёт на
// сервере и свой у каждой команды, то есть загружается асинхронно. Модуль
// остаётся набором чистых функций, а вся асинхронность — в Overview. Параметр
// обязательный намеренно: опциональный со значением 24ч тихо сохранил бы старое
// поведение в забытом call-site, а так его отсутствие ловит tsc.
export function defaultFilters(defaultPeriod: Period): OverviewFilters {
  return { search: "", method: "", status: "all", period: defaultPeriod };
}

// parseFilters — фильтры из query-параметров. Толерантно к мусору: невалидное
// значение каждого параметра независимо откатывается к дефолту (?status=banana&q=foo
// → статус all, поиск foo). range приоритетнее from/to, если пришло и то, и другое.
export function parseFilters(params: URLSearchParams, defaultPeriod: Period): OverviewFilters {
  const f = defaultFilters(defaultPeriod);

  f.search = params.get("q") ?? "";

  const method = params.get("method") ?? "";
  if ((METHOD_FILTERS as readonly string[]).includes(method)) f.method = method as MethodFilter;

  const status = params.get("status") ?? "";
  if ((STATUS_FILTERS as readonly string[]).includes(status)) f.status = status as StatusFilter;

  const range = params.get("range");
  const from = params.get("from");
  const to = params.get("to");
  if (range && (PRESET_RANGES as readonly string[]).includes(range)) {
    f.period = { kind: "preset", range: range as PresetRange };
  } else if (from && to && !Number.isNaN(Date.parse(from)) && !Number.isNaN(Date.parse(to))) {
    f.period = { kind: "custom", from, to };
  }

  return f;
}

// serializeFilters — query-параметры фильтров. Пишутся только отличия от дефолта:
// всё дефолтное → пустая строка → чистый URL «/» и пустое зеркало (на этом же
// свойстве держится замкнутость restore-эффекта, §54.4).
//
// defaultPeriod = null означает «дефолт команды ещё не загружен» (§71). Тогда
// период пишется ВСЕГДА: иначе правка любого другого фильтра в это окно решала
// бы «период дефолтный» по системным 24ч и стирала бы из URL явно выбранный
// пользователем `range=24h` — после прихода префов он молча сменился бы на
// дефолт команды.
export function serializeFilters(
  f: OverviewFilters,
  defaultPeriod: Period | null,
): URLSearchParams {
  const p = new URLSearchParams();

  if (f.search) p.set("q", f.search);
  if (f.method) p.set("method", f.method);
  if (f.status !== "all") p.set("status", f.status);
  if (defaultPeriod === null || periodKey(f.period) !== periodKey(defaultPeriod)) {
    if (f.period.kind === "preset") {
      p.set("range", f.period.range);
    } else {
      p.set("from", f.period.from);
      p.set("to", f.period.to);
    }
  }

  return p;
}

// hasFilterParams — есть ли в URL хоть один наш параметр (URL «непустой» → он и
// есть истина, восстанавливать из зеркала нечего).
export function hasFilterParams(params: URLSearchParams): boolean {
  return FILTER_PARAM_KEYS.some((k) => params.has(k));
}

// applyFilters — влить фильтры в существующие параметры, сохранив чужие ключи.
export function applyFilters(
  prev: URLSearchParams,
  f: OverviewFilters,
  defaultPeriod: Period | null,
): URLSearchParams {
  const next = new URLSearchParams(prev);
  for (const k of FILTER_PARAM_KEYS) next.delete(k);
  for (const [k, v] of serializeFilters(f, defaultPeriod)) next.set(k, v);
  return next;
}

// PERIOD_PARAM_KEYS — параметры, описывающие период. Выделены из
// FILTER_PARAM_KEYS, потому что в зеркало они не пишутся (§71).
const PERIOD_PARAM_KEYS = ["range", "from", "to"] as const;

// serializeMirror — то же, что serializeFilters, но БЕЗ периода: зеркало хранит
// только поиск/метод/статус. Период — состояние текущего экрана, а не фильтр,
// который стоит воскрешать при возврате: после ухода со страницы «Узлы» и
// возврата пользователь ждёт период своей команды, а не выбранный когда-то
// раньше (и, возможно, в другой команде).
function serializeMirror(f: OverviewFilters, defaultPeriod: Period | null): URLSearchParams {
  const p = serializeFilters(f, defaultPeriod);
  for (const k of PERIOD_PARAM_KEYS) p.delete(k);
  return p;
}

// saveFilters — записать зеркало. Всё-дефолт → ключ удаляется: «очистил фильтры»
// должно означать «восстанавливать нечего», иначе restore воскресит очищенное.
export function saveFilters(f: OverviewFilters, defaultPeriod: Period | null): void {
  try {
    const s = serializeMirror(f, defaultPeriod).toString();
    if (s) storage().setItem(FILTERS_KEY, s);
    else storage().removeItem(FILTERS_KEY);
  } catch {
    // приватный режим / переполнение — игнорируем
  }
}

// loadFilters — фильтры из зеркала; null, если восстанавливать нечего (пусто,
// мусор или хранилище недоступно). Период всегда дефолтный: в зеркале его нет,
// а зеркала старых версий (где он был) намеренно игнорируются.
export function loadFilters(defaultPeriod: Period): OverviewFilters | null {
  try {
    const raw = storage().getItem(FILTERS_KEY);
    if (!raw) return null;
    const params = new URLSearchParams(raw);
    for (const k of PERIOD_PARAM_KEYS) params.delete(k);
    const f = parseFilters(params, defaultPeriod);
    // Мусор распарсился во всё-дефолт → нечего восстанавливать.
    return serializeMirror(f, defaultPeriod).toString() ? f : null;
  } catch {
    return null;
  }
}
