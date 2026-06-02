// Период просмотра метрик (§28, Пункт 4): пресет 1h..30d ИЛИ произвольный
// календарный диапазон from/to (RFC3339). Вынесено из PeriodPicker, чтобы
// не ломать react-refresh (файл компонента экспортирует только компонент).
export type PresetRange = "1h" | "3h" | "24h" | "7d" | "14d" | "30d";
export type Period =
  | { kind: "preset"; range: PresetRange }
  | { kind: "custom"; from: string; to: string };

export const PRESET_RANGES: PresetRange[] = ["1h", "3h", "24h", "7d", "14d", "30d"];
export const defaultPeriod: Period = { kind: "preset", range: "1h" };

// periodParams — query-параметры для /api/metrics/* по выбранному периоду.
export function periodParams(p: Period): Record<string, string> {
  return p.kind === "preset" ? { range: p.range } : { from: p.from, to: p.to };
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
