// Фильтры рабочего стола (§54): поиск / корневой метод / статус / живой период.
//
// Модель — гибрид: URL источник истины (конвенция AuditLog, §54.2), sessionStorage
// зеркало на случай перехода, теряющего query-строку (кнопка «Узлы» в сайдбаре —
// NavLink to="/" без параметров). Канонический формат в обоих местах один и тот же —
// сериализованная query-строка, поэтому восстановление сводится к
// parse → serialize → setParams и заодно санитизирует мусор из хранилища.
import {
  PRESET_RANGES,
  loadDefaultPeriod,
  periodKey,
  type Period,
  type PresetRange,
} from "./period";

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
// localStorage: иначе сохранённый живой период всегда перекрывал бы звёздочку
// §44.B, а фильтр «прилипал» бы на недели (непонятно пустой список при заходе).
const FILTERS_STORAGE: "session" | "local" = "session";
const FILTERS_KEY = "nexus.overview.filters";

function storage(): Storage {
  return FILTERS_STORAGE === "session" ? window.sessionStorage : window.localStorage;
}

// defaultFilters — дефолтное состояние фильтров. Период по умолчанию берётся из
// пользовательской «звёздочки» (§44.B), поэтому дефолтный период не попадает ни в
// URL, ни в зеркало — см. serializeFilters.
export function defaultFilters(): OverviewFilters {
  return { search: "", method: "", status: "all", period: loadDefaultPeriod() };
}

// parseFilters — фильтры из query-параметров. Толерантно к мусору: невалидное
// значение каждого параметра независимо откатывается к дефолту (?status=banana&q=foo
// → статус all, поиск foo). range приоритетнее from/to, если пришло и то, и другое.
export function parseFilters(params: URLSearchParams): OverviewFilters {
  const f = defaultFilters();

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
export function serializeFilters(f: OverviewFilters): URLSearchParams {
  const p = new URLSearchParams();
  const def = defaultFilters();

  if (f.search) p.set("q", f.search);
  if (f.method) p.set("method", f.method);
  if (f.status !== "all") p.set("status", f.status);
  if (periodKey(f.period) !== periodKey(def.period)) {
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
export function applyFilters(prev: URLSearchParams, f: OverviewFilters): URLSearchParams {
  const next = new URLSearchParams(prev);
  for (const k of FILTER_PARAM_KEYS) next.delete(k);
  for (const [k, v] of serializeFilters(f)) next.set(k, v);
  return next;
}

// saveFilters — записать зеркало. Всё-дефолт → ключ удаляется: «очистил фильтры»
// должно означать «восстанавливать нечего», иначе restore воскресит очищенное.
export function saveFilters(f: OverviewFilters): void {
  try {
    const s = serializeFilters(f).toString();
    if (s) storage().setItem(FILTERS_KEY, s);
    else storage().removeItem(FILTERS_KEY);
  } catch {
    // приватный режим / переполнение — игнорируем (как saveDefaultPeriod)
  }
}

// loadFilters — фильтры из зеркала; null, если восстанавливать нечего (пусто,
// мусор или хранилище недоступно).
export function loadFilters(): OverviewFilters | null {
  try {
    const raw = storage().getItem(FILTERS_KEY);
    if (!raw) return null;
    const f = parseFilters(new URLSearchParams(raw));
    // Мусор распарсился во всё-дефолт → нечего восстанавливать.
    return serializeFilters(f).toString() ? f : null;
  } catch {
    return null;
  }
}
