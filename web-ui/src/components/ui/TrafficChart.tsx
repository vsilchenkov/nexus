import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { Tooltip } from "./Tooltip";

export type SeriesPoint = { ts: number; count: number; errors: number };

// TrafficChart — лёгкий бар-чарт трафика по эталону (§21): высота столбца ∝
// count, доля ошибок снизу — красным. Без внешних зависимостей. При наведении
// на столбец — богатый тултип: время, число запросов, ошибки (% от запросов).
export function TrafficChart({
  data,
  height = 120,
  className,
}: {
  data: SeriesPoint[];
  height?: number;
  className?: string;
}) {
  const { t } = useTranslation();
  const max = Math.max(1, ...data.map((d) => d.count));

  if (data.length === 0 || max <= 1) {
    return (
      <div
        className={cn("grid place-items-center text-xs text-fg-subtle", className)}
        style={{ height }}
      >
        {t("metrics.no_data")}
      </div>
    );
  }

  return (
    <div className={cn("flex items-end gap-[3px]", className)} style={{ height }}>
      {data.map((d, i) => {
        const h = Math.max(2, Math.round((d.count / max) * height));
        const errH = d.count > 0 ? Math.round((d.errors / d.count) * h) : 0;
        const ok = h - errH;
        const errPct = d.count > 0 ? ((d.errors / d.count) * 100).toFixed(1) : "0";
        return (
          <Tooltip
            key={i}
            side="top"
            delayDuration={80}
            content={
              <div className="space-y-1">
                <div className="font-mono text-fg-muted">
                  {new Date(d.ts).toLocaleTimeString()}
                </div>
                <div className="flex items-center gap-1.5">
                  <span className="inline-block h-2 w-2 rounded-sm bg-accent/60" />
                  {t("metrics.hints.tooltip.requests", { n: fmtNum(d.count) })}
                </div>
                <div className="flex items-center gap-1.5">
                  <span className="inline-block h-2 w-2 rounded-sm bg-err/70" />
                  {t("metrics.hints.tooltip.errors", { n: fmtNum(d.errors), pct: errPct })}
                </div>
              </div>
            }
          >
            <div className="flex flex-1 flex-col justify-end">
              {errH > 0 && (
                <div className="rounded-t-sm bg-err/70" style={{ height: errH }} />
              )}
              <div
                className={cn("bg-accent/60", errH === 0 && "rounded-t-sm")}
                style={{ height: ok }}
              />
            </div>
          </Tooltip>
        );
      })}
    </div>
  );
}
