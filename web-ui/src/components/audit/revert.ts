// Типы и хелперы отмены изменения узла из журнала аудита (§108).

// RevertMark — разметка строки выдачи /api/audit (только admin-сессия).
// overwritten — текущие значения полей, отличающихся от «Стало».
export type RevertMark = {
  state: "actual" | "stale" | "gone";
  revertible: boolean;
  overwritten?: Record<string, unknown>;
};

export type RevertFieldStatus =
  | "actual"
  | "overwritten"
  | "already"
  | "inapplicable"
  | "unrecoverable"
  | "secret";

export type RevertField = {
  name: string;
  before: unknown;
  after: unknown;
  current: unknown;
  status: RevertFieldStatus;
  warning: string;
};

// RevertPreview — ответ GET /api/audit/revert/:id. node.updated_at уходит
// обратно в POST строкой как есть: Date в JavaScript отбрасывает микросекунды
// PostgreSQL, и сервер ответил бы 409 всегда (§108.5).
export type RevertPreview = {
  entry: { id: string; action: string; created_at: string; user_login: string };
  node: { id: string; path: string; team_id: string; updated_at: string } | null;
  state: "actual" | "stale" | "gone" | "none";
  moved: boolean;
  fields: RevertField[];
  dry_run: { code: string; field: string; not_held: string[]; error?: string };
};

// isRevertible — поле можно выбрать для отмены.
export function isRevertible(f: RevertField): boolean {
  return f.status === "actual" || f.status === "overwritten";
}

// defaultSelection — выбор по умолчанию: поля, где сейчас «Стало», без
// предупреждения о кредах. Перебитые позже поля не отмечаются (§108.3).
export function defaultSelection(fields: RevertField[]): Set<string> {
  return new Set(fields.filter((f) => f.status === "actual" && !f.warning).map((f) => f.name));
}

// formatAuditValue — значение поля журнала одной строкой.
export function formatAuditValue(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "string") return v === "" ? "(empty)" : v;
  if (typeof v === "boolean" || typeof v === "number") return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}
