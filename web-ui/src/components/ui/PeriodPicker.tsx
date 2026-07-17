import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { PRESET_RANGES, type Period, type PresetRange } from "../../lib/period";
import { Seg } from "./data";

// toLocalInput — RFC3339 → значение <input type="datetime-local">
// (YYYY-MM-DDTHH:mm в локальной зоне; конструктор datetime-local зону не несёт).
function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (isNaN(+d)) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// PeriodPicker — пресеты периода (1h..30d) + «Произвольный» с двумя
// datetime-local (календарь), §28 Пункт 4. По умолчанию 1h.
export function PeriodPicker({
  value,
  onChange,
  className,
}: {
  value: Period;
  onChange: (p: Period) => void;
  className?: string;
}) {
  const { t } = useTranslation();
  const isCustom = value.kind === "custom";
  const [showCustom, setShowCustom] = useState(isCustom);
  // Поля календаря засеваются из value: пришедший извне произвольный период
  // (§54 — восстановление фильтров, дип-линк) должен быть виден в полях, а не
  // только применён к метрикам.
  const [from, setFrom] = useState(() => (value.kind === "custom" ? toLocalInput(value.from) : ""));
  const [to, setTo] = useState(() => (value.kind === "custom" ? toLocalInput(value.to) : ""));

  // Засева в useState мало: период может прийти уже ПОСЛЕ монтирования, без
  // ремоунта компонента (§54 — клик «Узлы» на активной странице восстанавливает
  // фильтры эффектом). Зависимости — строки, поэтому набранное в полях не
  // затирается: пользовательский ввод value не меняет.
  const customFrom = value.kind === "custom" ? toLocalInput(value.from) : null;
  const customTo = value.kind === "custom" ? toLocalInput(value.to) : null;
  useEffect(() => {
    if (customFrom === null || customTo === null) return;
    setFrom(customFrom);
    setTo(customTo);
    setShowCustom(true);
  }, [customFrom, customTo]);

  function applyCustom() {
    if (!from || !to) return;
    const f = new Date(from);
    const tt = new Date(to);
    if (isNaN(+f) || isNaN(+tt) || f >= tt) return;
    onChange({ kind: "custom", from: f.toISOString(), to: tt.toISOString() });
  }

  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      <Seg
        value={value.kind === "preset" ? value.range : ("" as PresetRange)}
        onChange={(r) => {
          setShowCustom(false);
          onChange({ kind: "preset", range: r });
        }}
        options={PRESET_RANGES.map((r) => ({ value: r, label: t(`metrics.range.${r}`) }))}
      />
      <button
        type="button"
        onClick={() => setShowCustom((s) => !s)}
        className={cn(
          "rounded-md border border-line px-2.5 py-1 text-xs transition-colors hover:text-fg",
          isCustom ? "border-accent text-accent" : "text-fg-muted",
        )}
      >
        {t("metrics.range.custom")}
      </button>
      {showCustom && (
        <div className="flex flex-wrap items-center gap-1.5">
          <input
            type="datetime-local"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            className="rounded-md border border-line bg-app px-2 py-1 text-xs text-fg"
          />
          <span className="text-fg-subtle">—</span>
          <input
            type="datetime-local"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            className="rounded-md border border-line bg-app px-2 py-1 text-xs text-fg"
          />
          <button
            type="button"
            onClick={applyCustom}
            disabled={!from || !to}
            className="rounded-md bg-accent px-2.5 py-1 text-xs disabled:opacity-50"
          >
            {t("common.apply")}
          </button>
        </div>
      )}
    </div>
  );
}
