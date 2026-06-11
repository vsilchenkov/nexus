// parseNumInput — безопасный парсер значений <input type="number"> для
// controlled-форм (Phase AUD.7): пустая строка → fallback (поле очищено),
// нечисловой ввод (NaN/Infinity) → прежнее значение. Раньше формы делали
// Number(e.target.value) напрямую и при очистке поля отправляли NaN в API.
export function parseNumInput(raw: string, prev: number, fallback = 0): number {
  if (raw.trim() === "") return fallback;
  const n = Number(raw);
  return Number.isFinite(n) ? n : prev;
}
