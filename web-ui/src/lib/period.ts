// Период просмотра метрик (§28, Пункт 4): пресет 1h..30d ИЛИ произвольный
// календарный диапазон from/to (RFC3339). Вынесено из PeriodPicker, чтобы
// не ломать react-refresh (файл компонента экспортирует только компонент).
export type PresetRange = "1h" | "3h" | "24h" | "7d" | "14d" | "30d";
export type Period =
  | { kind: "preset"; range: PresetRange }
  | { kind: "custom"; from: string; to: string };

export const PRESET_RANGES: PresetRange[] = ["1h", "3h", "24h", "7d", "14d", "30d"];

// defaultPeriod — дефолт периода метрик (§44.B): 24ч (раньше 1ч). На рабочем
// столе перекрывается пользовательским дефолтом из localStorage (loadDefaultPeriod).
export const defaultPeriod: Period = { kind: "preset", range: "24h" };

// PERIOD_DEFAULT_KEY — localStorage-ключ пользовательского дефолтного периода
// рабочего стола («под себя», §44.B). Хранится только пресет.
const PERIOD_DEFAULT_KEY = "nexus.overview.period";

// loadDefaultPeriod — пользовательский дефолтный период из localStorage, иначе
// defaultPeriod (24ч). Толерантно к мусору/недоступному хранилищу.
export function loadDefaultPeriod(): Period {
  try {
    const raw = localStorage.getItem(PERIOD_DEFAULT_KEY);
    if (!raw) return defaultPeriod;
    const p = JSON.parse(raw) as Period;
    if (p && p.kind === "preset" && PRESET_RANGES.includes(p.range)) return p;
    return defaultPeriod;
  } catch {
    return defaultPeriod;
  }
}

// saveDefaultPeriod — сохранить выбранный период как дефолтный рабочего стола.
// Только пресет: произвольный календарный диапазон дефолтом не имеет смысла.
export function saveDefaultPeriod(p: Period): void {
  if (p.kind !== "preset") return;
  try {
    localStorage.setItem(PERIOD_DEFAULT_KEY, JSON.stringify(p));
  } catch {
    // приватный режим / переполнение — игнорируем
  }
}

// periodParams — query-параметры для /api/metrics/* по выбранному периоду.
export function periodParams(p: Period): Record<string, string> {
  return p.kind === "preset" ? { range: p.range } : { from: p.from, to: p.to };
}

// PRESET_MS — длительность пресета в миллисекундах.
const PRESET_MS: Record<PresetRange, number> = {
  "1h": 3_600_000,
  "3h": 10_800_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
  "14d": 1_209_600_000,
  "30d": 2_592_000_000,
};

// periodWindow — границы окна [since, until] в мс (для расчёта временных
// бакетов спарклайна). Пресет: until = «сейчас» (бакеты считаются на сервере
// в момент запроса, клиентский дрейф в пределах секунд — для тултипа ок).
// Произвольный период: явные from/to.
export function periodWindow(p: Period): { since: number; until: number } {
  if (p.kind === "custom") {
    return { since: Date.parse(p.from), until: Date.parse(p.to) };
  }
  const until = Date.now();
  return { since: until - PRESET_MS[p.range], until };
}

// periodKey — стабильный ключ периода для queryKey TanStack Query.
export function periodKey(p: Period): string {
  return p.kind === "preset" ? `r:${p.range}` : `c:${p.from}:${p.to}`;
}

// periodLabel — короткая метка периода для заголовков (§28 Пункт 4): пресет →
// локализованное "1ч"/"24ч"/…, произвольный → "Произвольный". t — i18n-функция.
export function periodLabel(p: Period, t: (k: string) => string): string {
  return p.kind === "preset" ? t(`metrics.range.${p.range}`) : t("metrics.range.custom");
}
