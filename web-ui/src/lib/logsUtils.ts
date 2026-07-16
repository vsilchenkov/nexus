// Чистые утилиты консоли служебных логов (§51.6): маппинг уровней,
// фильтрация, форматирование строк, стабильный queryKey. Вынесены из
// компонентов, чтобы покрыть unit-тестами (vitest) без DOM.

export type ServiceLogEntry = {
  ts: string;
  level: string;
  service: string;
  msg: string;
  attrs?: Record<string, unknown>;
};

export type ServiceName = "receiver" | "sender" | "web";
export const SERVICES: ServiceName[] = ["receiver", "sender", "web"];

export type LevelName = "error" | "warn" | "info" | "debug";
export const LEVELS: LevelName[] = ["error", "warn", "info", "debug"];

// Шкала backend'а (§51): 2=error, 3=warn, 4=info, 5=debug.
const levelByInt: Record<number, LevelName> = { 2: "error", 3: "warn", 4: "info", 5: "debug" };
const intByLevel: Record<LevelName, number> = { error: 2, warn: 3, info: 4, debug: 5 };

// levelFromInt — уровень из app_settings.logging.level; вне 2..5 → info
// (поведение backend-fallback'а).
export function levelFromInt(l: number | undefined | null): LevelName {
  if (l == null) return "info";
  return levelByInt[l] ?? "info";
}

// levelToInt — значение для PUT /api/settings/app {logging:{level}}.
export function levelToInt(level: LevelName): number {
  return intByLevel[level];
}

// levelBadgeClass — семантические токены окраски бейджа уровня.
export function levelBadgeClass(level: string): string {
  switch (level) {
    case "error":
      return "text-err";
    case "warn":
      return "text-warn";
    case "debug":
      return "text-fg-subtle";
    default:
      return "text-fg-muted";
  }
}

// stableStringify — детерминированная сериализация attrs (ключи отсортированы),
// чтобы поиск и форматирование строк не зависели от порядка ключей JSON.
export function stableStringify(v: unknown): string {
  if (v === null || typeof v !== "object") return JSON.stringify(v) ?? "";
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(",")}]`;
  const obj = v as Record<string, unknown>;
  const keys = Object.keys(obj).sort();
  return `{${keys.map((k) => `${JSON.stringify(k)}:${stableStringify(obj[k])}`).join(",")}}`;
}

// filterEntries — клиентский фильтр консоли: подстрочный поиск
// (case-insensitive) по msg и атрибутам. Фильтр по сервисам сюда НЕ входит:
// он серверный (см. serviceParam) — иначе один «болтливый» сервис выбирает
// весь limit при мерже, и строки остальных до браузера не доезжают вовсе.
export function filterEntries(
  entries: ServiceLogEntry[],
  opts: { query: string },
): ServiceLogEntry[] {
  const q = opts.query.trim().toLowerCase();
  if (q === "") return entries;
  return entries.filter((e) => {
    if (e.msg.toLowerCase().includes(q)) return true;
    return e.attrs != null && stableStringify(e.attrs).toLowerCase().includes(q);
  });
}

// serviceParam — значение query-параметра `service` для GET /api/logs:
// пусто/все три → undefined (сервер отдаёт мерж всех), иначе csv в
// стабильном порядке SERVICES (ключ кеша не зависит от порядка кликов).
export function serviceParam(services: ServiceName[]): string | undefined {
  if (services.length === 0 || services.length === SERVICES.length) return undefined;
  return SERVICES.filter((s) => services.includes(s)).join(",");
}

// formatTs — компактное время строки консоли (локальное, с мс).
export function formatTs(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `${hh}:${mm}:${ss}.${ms}`;
}

// formatEntryLine — плоская строка записи (время, уровень, сервис, msg,
// детерминированный JSON атрибутов).
export function formatEntryLine(e: ServiceLogEntry): string {
  const attrs = e.attrs && Object.keys(e.attrs).length > 0 ? ` ${stableStringify(e.attrs)}` : "";
  return `${formatTs(e.ts)} ${e.level.toUpperCase()} [${e.service}] ${e.msg}${attrs}`;
}

// logsQueryKey — СТАБИЛЬНЫЙ ключ react-query для GET /api/logs: limit +
// серверный фильтр сервисов (поиск — клиентский и в ключе не участвует).
// Никаких волатильных частей (Date.now и т.п.) — иначе кеш промахивается на
// каждом рендере и консоль вечно пуста (грабли §44).
export function logsQueryKey(
  services: ServiceName[],
  limit: number,
): readonly [string, string, number] {
  return ["service-logs", serviceParam(services) ?? "all", limit] as const;
}
