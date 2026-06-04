import { useTranslation } from "react-i18next";
import { ExternalLink, TrendingUp } from "lucide-react";

import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { ChartTooltip, type TooltipSeries } from "./ChartTooltip";
import { Tooltip } from "./Tooltip";

export type SeriesPoint = { ts: number; count: number; errors: number };

// LogsRange — окно бакета для перехода в логи за момент (§33.4): клик по столбцу
// открывает логи узла с этими границами.
export type LogsRange = { from: number; to: number; onlyErrors: boolean };

// Цвета серий — дизайн-токены accent/err как hex (data-driven маркеры тултипа §33).
const C_OK = "#5b9bf0";
const C_ERR = "#e85d5c";

// fmtClock — HH:MM:SS локально (граница бакета в шапке тултипа).
function fmtClock(ms: number): string {
  return new Date(ms).toLocaleTimeString();
}

// fmtDuration — длительность бакета словами (1 мин / 30 сек / 2 ч).
function fmtDuration(ms: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  const sec = Math.round(ms / 1000);
  if (sec < 60) return t("metrics.tooltip.seconds", { n: sec });
  const min = Math.round(sec / 60);
  if (min < 60) return t("metrics.tooltip.minutes", { n: min });
  return t("metrics.tooltip.hours", { n: Math.round(min / 60) });
}

// TrafficChart — лёгкий бар-чарт трафика по эталону (§21): высота столбца ∝
// count, доля ошибок снизу — красным. Без внешних зависимостей. При наведении
// на столбец — богатый тултип (§33): период, главное значение, серии
// доставлено/ошибок, подвал (пик/дельта к среднему). Если задан onOpenLogs —
// клик по столбцу открывает логи за этот интервал.
export function TrafficChart({
  data,
  height = 120,
  className,
  onOpenLogs,
}: {
  data: SeriesPoint[];
  height?: number;
  className?: string;
  onOpenLogs?: (range: LogsRange) => void;
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

  const avg = data.reduce((s, d) => s + d.count, 0) / data.length;
  // Ширина бакета — шаг между метками времени (равномерный); для последнего
  // экстраполируем тем же шагом.
  const bucketW = data.length > 1 ? data[1].ts - data[0].ts : 60_000;

  return (
    <div className={cn("flex w-full items-end gap-[3px]", className)} style={{ height }}>
      {data.map((d, i) => {
        const h = Math.max(2, Math.round((d.count / max) * height));
        const errH = d.count > 0 ? Math.round((d.errors / d.count) * h) : 0;
        const ok = h - errH;
        const errPct = d.count > 0 ? (d.errors / d.count) * 100 : 0;
        const delivered = Math.max(0, d.count - d.errors);
        const from = d.ts;
        const to = d.ts + bucketW;

        const series: TooltipSeries[] = [
          { marker: "bar", color: C_OK, name: t("metrics.tooltip.delivered"), value: fmtNum(delivered) },
        ];
        if (d.errors > 0) {
          series.push({ marker: "bar", color: C_ERR, name: t("metrics.tooltip.errors"), value: fmtNum(d.errors) });
        }

        const isPeak = d.count === max;
        const deltaPct = avg > 0 ? Math.round(((d.count - avg) / avg) * 100) : 0;
        const footer = isPeak
          ? { tone: "muted" as const, icon: <TrendingUp className="h-3 w-3" />, text: t("metrics.tooltip.peak") }
          : {
              tone: "muted" as const,
              text: t("metrics.tooltip.vs_avg"),
              delta: {
                dir: deltaPct >= 0 ? ("up" as const) : ("down" as const),
                text: `${deltaPct >= 0 ? "+" : ""}${deltaPct}%`,
              },
            };

        return (
          <Tooltip
            key={i}
            side="top"
            delayDuration={150}
            content={
              <ChartTooltip
                period={{ from: fmtClock(from), to: fmtClock(to), durationLabel: fmtDuration(bucketW, t) }}
                primary={{ value: fmtNum(d.count), unit: t("metrics.tooltip.requests"), tone: errPct >= 50 ? "warn" : "normal" }}
                series={series}
                footer={footer}
                action={
                  onOpenLogs
                    ? { icon: <ExternalLink className="h-3 w-3" />, label: t("metrics.tooltip.open_logs") }
                    : undefined
                }
              />
            }
          >
            <div
              className={cn("flex flex-1 flex-col justify-end", onOpenLogs && "cursor-pointer")}
              onClick={onOpenLogs ? () => onOpenLogs({ from, to, onlyErrors: d.errors > 0 }) : undefined}
            >
              {errH > 0 && <div className="rounded-t-sm bg-err/70" style={{ height: errH }} />}
              <div className={cn("bg-accent/60", errH === 0 && "rounded-t-sm")} style={{ height: ok }} />
            </div>
          </Tooltip>
        );
      })}
    </div>
  );
}
