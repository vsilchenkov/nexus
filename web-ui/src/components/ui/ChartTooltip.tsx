import { type ReactNode } from "react";

import { cn } from "../../lib/cn";

// ChartTooltip — единый презентационный тултип всех графиков (§33). Чистая
// отрисовка по пропсам; данные наполняют мапперы каждого графика (recharts
// content-адаптер или Radix Tooltip). Иерархия: период → главное значение →
// серии с маркерами → подвал/дельта → опц. действие-подсказка.

export type TooltipMarker = "bar" | "line" | "dashed";

export type TooltipSeries = {
  marker: TooltipMarker;
  color: string; // data-driven цвет серии (hex/var графика) — не токен chrome
  name: string;
  value: string; // уже отформатировано вызывающим
  unit?: string;
  muted?: boolean; // приглушить (ряд сравнения)
};

export type ChartTooltipProps = {
  period?: { from: string; to?: string; durationLabel?: string; note?: string };
  primary?: { value: string; unit?: string; tone?: "normal" | "warn" | "err" };
  series?: TooltipSeries[];
  footer?: {
    tone?: "muted" | "warn" | "err";
    icon?: ReactNode;
    text: string;
    delta?: { dir: "up" | "down"; text: string };
  };
  // action — визуальная подсказка-affordance (клик вешается на сам элемент
  // графика, не на тултип: hover-тултип ненадёжно ловит клик по кнопке).
  action?: { icon?: ReactNode; label: string };
  compact?: boolean;
};

const PRIMARY_TONE = { normal: "text-fg", warn: "text-warn", err: "text-err" } as const;
const FOOTER_TONE = { muted: "text-fg-subtle", warn: "text-warn", err: "text-err" } as const;

function Marker({ marker, color }: { marker: TooltipMarker; color: string }) {
  if (marker === "dashed") {
    return (
      <span
        className="inline-block w-2.5 shrink-0 border-t-2 border-dashed"
        style={{ borderColor: color }}
      />
    );
  }
  if (marker === "line") {
    return (
      <span className="inline-block h-0.5 w-2.5 shrink-0 rounded-full" style={{ background: color }} />
    );
  }
  return <span className="inline-block h-2 w-2 shrink-0 rounded-sm" style={{ background: color }} />;
}

export function ChartTooltip({ period, primary, series, footer, action, compact }: ChartTooltipProps) {
  return (
    <div
      className={cn(
        "rounded-md border border-line-strong bg-bg/95 shadow-xl ring-1 ring-white/5 backdrop-blur",
        compact ? "min-w-[140px] px-2.5 py-2" : "min-w-[180px] px-3 py-2.5",
      )}
    >
      {period && (
        <div className="mb-1.5 flex items-baseline gap-1.5 font-mono text-[11px] text-fg-subtle">
          <span>{period.from}</span>
          {period.to && (
            <>
              <span className="text-fg-subtle/70">→</span>
              <span>{period.to}</span>
            </>
          )}
          {period.note && <span className="text-fg-muted">· {period.note}</span>}
          {period.durationLabel && (
            <span className="ml-auto rounded-full bg-bg-muted px-1.5 py-px text-[9px] text-fg-muted">
              {period.durationLabel}
            </span>
          )}
        </div>
      )}

      {primary && (
        <div
          className={cn(
            "mb-1 font-mono text-2xl font-bold leading-none",
            PRIMARY_TONE[primary.tone ?? "normal"],
          )}
        >
          {primary.value}
          {primary.unit && (
            <span className="ml-1 text-xs font-medium text-fg-muted">{primary.unit}</span>
          )}
        </div>
      )}

      {series && series.length > 0 && (
        <div className="space-y-0.5">
          {series.map((s, i) => (
            <div key={i} className="flex items-center gap-2 py-0.5 text-xs">
              <Marker marker={s.marker} color={s.color} />
              <span className={cn("flex-1 text-[11.5px]", s.muted ? "text-fg-subtle" : "text-fg-muted")}>
                {s.name}
              </span>
              <span
                className={cn(
                  "font-mono text-[12.5px] font-semibold",
                  s.muted ? "text-fg-subtle" : "text-fg",
                )}
              >
                {s.value}
                {s.unit && <span className="ml-0.5 text-[10px] font-normal text-fg-subtle">{s.unit}</span>}
              </span>
            </div>
          ))}
        </div>
      )}

      {footer && (
        <div
          className={cn(
            "mt-2 flex items-center gap-1.5 border-t border-line pt-1.5 text-[11px]",
            FOOTER_TONE[footer.tone ?? "muted"],
          )}
        >
          {footer.icon}
          <span>{footer.text}</span>
          {footer.delta && (
            <span
              className={cn("ml-auto font-mono", footer.delta.dir === "up" ? "text-ok" : "text-err")}
            >
              {footer.delta.text}
            </span>
          )}
        </div>
      )}

      {action && (
        <div className="mt-2 flex items-center gap-1.5 border-t border-line pt-1.5 text-[11px] text-accent">
          {action.icon}
          <span>{action.label}</span>
        </div>
      )}
    </div>
  );
}
