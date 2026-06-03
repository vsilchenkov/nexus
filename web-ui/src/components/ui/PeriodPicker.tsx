import { useState } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { PRESET_RANGES, type Period, type PresetRange } from "../../lib/period";
import { Seg } from "./data";

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
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");

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
