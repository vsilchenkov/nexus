import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Info, RotateCcw } from "lucide-react";

import { cn } from "../lib/cn";
import { formatAuditValue as formatValue } from "./audit/revert";

// AuditDetailsCell — рендер ячейки `details` в Audit log (§7.13).
// Для action="node.update" и "node.revert" (§108) details имеет форму
// {field: {before, after}} (см. internal/web/usecase/node.go diffNodes) —
// показываем компактную двухколоночную таблицу. Для остальных action — JSON в
// раскрывающемся блоке (как раньше).
//
// overwritten — текущие значения полей, перебитых после записи (разметка
// §108.7, только у admin): их «Стало» зачёркнуто, текущее — в подсказке.
// muted — строка неактуальна, diff приглушается.
type Props = {
  action: string;
  details: Record<string, unknown> | null | undefined;
  overwritten?: Record<string, unknown>;
  muted?: boolean;
};

// DIFF_ACTIONS — действия, чьи details — diff полей узла.
const DIFF_ACTIONS = new Set(["node.update", "node.revert"]);

// REVERT_META_KEYS — служебные ключи строки node.revert (§108.6): полями узла
// не являются, вместо них показывается строка «отмена записи от …».
const REVERT_META_KEYS = new Set(["reverted_from", "reverted_from_at"]);

type Diff = { before: unknown; after: unknown };

function isDiff(v: unknown): v is Diff {
  return (
    typeof v === "object" &&
    v !== null &&
    "before" in (v as Record<string, unknown>) &&
    "after" in (v as Record<string, unknown>)
  );
}

function DiffTable({
  details,
  overwritten,
}: {
  details: Record<string, unknown>;
  overwritten?: Record<string, unknown>;
}) {
  const { t } = useTranslation();
  const fields = Object.entries(details).filter(([k]) => !REVERT_META_KEYS.has(k));
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
                  {overwritten && k in overwritten ? (
                    <span
                      className="text-fg-muted"
                      title={t("audit.diff.now", { value: formatValue(overwritten[k]) })}
                    >
                      <s>{formatValue(v.after)}</s>
                      <Info className="ml-1 inline h-3 w-3 text-warn" />
                    </span>
                  ) : (
                    formatValue(v.after)
                  )}
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

export function AuditDetailsCell({ action, details, overwritten, muted }: Props) {
  if (!details || Object.keys(details).length === 0) {
    return <span className="text-fg-muted text-xs">—</span>;
  }
  if (DIFF_ACTIONS.has(action)) {
    return (
      <div className={cn(muted && "opacity-60")}>
        {action === "node.revert" && <RevertSource at={details.reverted_from_at} />}
        <DiffTable details={details} overwritten={overwritten} />
      </div>
    );
  }
  return <LazyJson details={details} />;
}

// RevertSource — «отмена записи от …» над diff строки node.revert (§108.6).
function RevertSource({ at }: { at: unknown }) {
  const { t } = useTranslation();
  const when = typeof at === "string" ? new Date(at).toLocaleString() : "—";
  return (
    <div className="mb-1 flex items-center gap-1 text-[11px] text-fg-muted">
      <RotateCcw className="h-3 w-3" />
      {t("audit.revert_source", { at: when })}
    </div>
  );
}

// LazyJson — сериализует и рендерит JSON-тело деталей ТОЛЬКО при раскрытии
// строки. У свёрнутого <details> контент всё равно попадает в DOM, поэтому
// эагерный JSON.stringify по всем строкам аудита раздувает дерево и вешает
// фронт на больших объёмах — здесь <pre> монтируется по onToggle.
function LazyJson({ details }: { details: Record<string, unknown> }) {
  const [open, setOpen] = useState(false);
  return (
    <details onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}>
      <summary className="cursor-pointer text-fg-muted">json</summary>
      {open && (
        <pre className="overflow-auto bg-bg-muted/40 p-1 rounded mt-1 text-[10px]">
          {JSON.stringify(details, null, 2)}
        </pre>
      )}
    </details>
  );
}
