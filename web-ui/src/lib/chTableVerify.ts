import type { CHTableVerifyResult } from "../api/client";

// §64: сообщение под кнопкой «Проверить» — чистая функция, чтобы её поведение
// (какой из исходов побеждает и как перечисляются расхождения) проверялось
// тестом, а не глазами на стенде.

export type VerifyMessage = { ok: boolean; text: string };

type Translate = (key: string, opts?: Record<string, unknown>) => string;

// buildVerifyMessage возвращает null, пока проверка не запускалась.
//
// Порядок исходов: ошибка вызова (проверка не состоялась) → таблицы нет →
// расхождения структуры → всё в порядке. Missing и mismatched показываются
// вместе: чинить их оператору всё равно одним заходом.
export function buildVerifyMessage(
  callError: string | null,
  res: CHTableVerifyResult | undefined,
  t: Translate,
): VerifyMessage | null {
  if (callError) return { ok: false, text: callError };
  if (!res) return null;
  if (res.table_missing) return { ok: false, text: t("node.verify.table_missing") };
  if (res.ok) return { ok: true, text: t("node.verify.ok") };

  const parts: string[] = [];
  if (res.missing?.length) {
    parts.push(t("node.verify.missing_columns", { cols: res.missing.join(", ") }));
  }
  if (res.mismatched?.length) {
    const list = res.mismatched.map((m) => `${m.name}: ${m.got} → ${m.want}`).join(", ");
    parts.push(t("node.verify.type_mismatch", { list }));
  }
  return { ok: false, text: parts.join("; ") };
}
