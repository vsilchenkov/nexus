import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";

export type SeriesPoint = { ts: number; count: number; errors: number };

// TrafficChart — лёгкий бар-чарт трафика по эталону (§21): высота столбца ∝
// count, доля ошибок снизу — красным. Без внешних зависимостей.
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
        const time = new Date(d.ts).toLocaleTimeString();
        return (
          <div
            key={i}
            className="flex flex-1 flex-col justify-end"
            title={`${time} · ${d.count} (${d.errors} err)`}
          >
            {errH > 0 && (
              <div className="rounded-t-sm bg-err/70" style={{ height: errH }} />
            )}
            <div
              className={cn("bg-accent/60", errH === 0 && "rounded-t-sm")}
              style={{ height: ok }}
            />
          </div>
        );
      })}
    </div>
  );
}
