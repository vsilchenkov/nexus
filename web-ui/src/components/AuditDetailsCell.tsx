import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Info, RotateCcw } from "lucide-react";

import { cn } from "../lib/cn";
import { formatAuditValue as formatValue } from "./audit/revert";

// AuditDetailsCell — рендер ячейки `details` в Audit log (§7.13).
// Для action="node.update" и "node.revert" (§108) details имеет форму
// {field: {before, after}} (см. internal/web/usecase/node.go diffNodes) —
// показываем компактный diff: короткая пара «было → стало» в строку, длинная —
// строками «Было»/«Стало» под именем поля. Для остальных action — JSON в
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
  // Сетка «поле | значение», а не таблица «поле | Было | Стало». В колонке
  // «Детали» журнала на экране ~1200px остаётся ~290px: неразрывные соседние
  // колонки занимают остальное, а длинное имя поля — ещё ~170px из этих 290.
  // Две колонки значений в оставшемся месте шли по две-три буквы в строке
  // (break-all), а минимальная ширина давала горизонтальную прокрутку журнала.
  // Поэтому короткая пара — одной строкой «было → стало», длинная — строками
  // «Было»/«Стало» под именем поля на всю ширину колонки.
  return (
    <div className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-2 font-mono text-[11px]">
      {fields.map(([k, v]) => (
        <DiffRow key={k} name={k} value={v} overwritten={overwritten} />
      ))}
    </div>
  );
}

// INLINE_MAX — предел суммарной длины «было» и «стало» для записи одной
// строкой: «paused → enabled», «none → basic». Длиннее — строками под полем.
const INLINE_MAX = 32;

// wrap — перенос по пробелам; слово рвётся, только когда не влезает в строку
// (URL, JSON без пробелов). break-all рвал бы и там, где есть пробел.
const wrap = "[overflow-wrap:anywhere]";

// DiffRow — поле DiffTable: короткая пара одной строкой, длинная — строками
// «Было»/«Стало» под именем поля; маркер вида "auth_credentials": "changed" —
// курсивом рядом с полем.
function DiffRow({
  name,
  value,
  overwritten,
}: {
  name: string;
  value: unknown;
  overwritten?: Record<string, unknown>;
}) {
  const { t } = useTranslation();
  const key = <span className="whitespace-nowrap py-0.5 text-fg-muted">{name}</span>;
  if (!isDiff(value)) {
    return (
      <>
        {key}
        <span className={cn("py-0.5 italic text-fg-muted", wrap)}>{formatValue(value)}</span>
      </>
    );
  }
  const before = formatValue(value.before);
  const after = <AfterValue name={name} value={value.after} overwritten={overwritten} />;
  if (before.length + formatValue(value.after).length <= INLINE_MAX) {
    return (
      <>
        {key}
        <span className={cn("py-0.5", wrap)}>
          <span className="text-err">{before}</span>
          <span className="text-fg-muted"> → </span>
          {after}
        </span>
      </>
    );
  }
  return (
    <>
      <span className="col-span-2 whitespace-nowrap pt-0.5 text-fg-muted">{name}</span>
      <div className="col-span-2 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-2 pb-0.5 pl-3">
        <span className="text-fg-muted">{t("audit.diff.before")}</span>
        <span className={cn("text-err", wrap)}>{before}</span>
        <span className="text-fg-muted">{t("audit.diff.after")}</span>
        {after}
      </div>
    </>
  );
}

// AfterValue — «Стало»; перебитое после записи (§108.7) зачёркнуто, текущее
// значение — в подсказке.
function AfterValue({
  name,
  value,
  overwritten,
}: {
  name: string;
  value: unknown;
  overwritten?: Record<string, unknown>;
}) {
  const { t } = useTranslation();
  if (overwritten && name in overwritten) {
    return (
      <span
        className={cn("text-fg-muted", wrap)}
        title={t("audit.diff.now", { value: formatValue(overwritten[name]) })}
      >
        <s>{formatValue(value)}</s>
        <Info className="ml-1 inline h-3 w-3 text-warn" />
      </span>
    );
  }
  return <span className={cn("text-ok", wrap)}>{formatValue(value)}</span>;
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
