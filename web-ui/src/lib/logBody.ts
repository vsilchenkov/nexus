// §42: помощники отображения тел логов (preview / «показать весь»).
// Вынесены из LogsTab для тестируемости и переиспользования.

// FETCH_CHUNK — сколько рун тянем за один запрос /body при «показать весь».
export const FETCH_CHUNK = 1024 * 1024;
// PRETTY_MAX — выше этого размера (символы) НЕ делаем JSON.parse/stringify:
// синхронный pretty-print большого тела вешает главный поток.
export const PRETTY_MAX = 256 * 1024;
// LARGE_WARN_RUNES — выше этого размера «показать весь» сначала предупреждает
// (рендер десятков МБ строки в DOM тяжёлый).
export const LARGE_WARN_RUNES = 2 * 1024 * 1024;

// prettyMaybe — JSON pretty-print только для тел до PRETTY_MAX символов (иначе
// синхронный parse/stringify вешает фронт); крупные/не-JSON — как есть.
export function prettyMaybe(s: string): string {
  if (s.length > PRETTY_MAX) return s;
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// formatRunes — короткая длина: 512 → "512", 65536 → "64K", 2_500_000 → "2.4M".
export function formatRunes(n: number): string {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)}M`;
  if (n >= 1024) return `${Math.round(n / 1024)}K`;
  return String(n);
}
