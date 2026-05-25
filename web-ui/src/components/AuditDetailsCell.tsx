import { useTranslation } from "react-i18next";

// AuditDetailsCell — рендер ячейки `details` в Audit log (§7.13).
// Для action="node.update" details имеет форму {field: {before, after}}
// (см. internal/web/usecase/node.go diffNodes) — показываем компактную
// двухколоночную таблицу. Для остальных action — JSON в раскрывающемся
// блоке (как раньше).
type Props = {
  action: string;
  details: Record<string, unknown> | null | undefined;
};

type Diff = { before: unknown; after: unknown };

function isDiff(v: unknown): v is Diff {
  return (
    typeof v === "object" &&
    v !== null &&
    "before" in (v as Record<string, unknown>) &&
    "after" in (v as Record<string, unknown>)
  );
}

function formatValue(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "string") return v === "" ? "(empty)" : v;
  if (typeof v === "boolean" || typeof v === "number") return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

function DiffTable({ details }: { details: Record<string, unknown> }) {
  const { t } = useTranslation();
  const fields = Object.entries(details);
  if (fields.length === 0) {
    return (
      <span className="text-fg-muted text-xs">
        {t("audit.diff.no_changes")}
      </span>
    );
  }
  return (
    <table className="w-full text-[11px] font-mono border-collapse">
      <thead>
        <tr className="text-fg-muted">
          <th className="text-left pr-2 pb-1 font-normal w-px whitespace-nowrap"></th>
          <th className="text-left pr-2 pb-1 font-normal">
            {t("audit.diff.before")}
          </th>
          <th className="text-left pb-1 font-normal">
            {t("audit.diff.after")}
          </th>
        </tr>
      </thead>
      <tbody>
        {fields.map(([k, v]) => {
          if (isDiff(v)) {
            return (
              <tr key={k} className="align-top">
                <td className="pr-2 py-0.5 text-fg-muted whitespace-nowrap">
                  {k}
                </td>
                <td className="pr-2 py-0.5 text-err break-all">
                  {formatValue(v.before)}
                </td>
                <td className="py-0.5 text-ok break-all">
                  {formatValue(v.after)}
                </td>
              </tr>
            );
          }
          // Спецслучай: "auth_credentials": "changed" — рендерим как маркер.
          return (
            <tr key={k} className="align-top">
              <td className="pr-2 py-0.5 text-fg-muted whitespace-nowrap">
                {k}
              </td>
              <td colSpan={2} className="py-0.5 text-fg-muted italic">
                {formatValue(v)}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function AuditDetailsCell({ action, details }: Props) {
  if (!details || Object.keys(details).length === 0) {
    return <span className="text-fg-muted text-xs">—</span>;
  }
  if (action === "node.update") {
    return <DiffTable details={details} />;
  }
  return (
    <details>
      <summary className="cursor-pointer text-fg-muted">json</summary>
      <pre className="overflow-auto bg-bg-muted/40 p-1 rounded mt-1 text-[10px]">
        {JSON.stringify(details, null, 2)}
      </pre>
    </details>
  );
}
