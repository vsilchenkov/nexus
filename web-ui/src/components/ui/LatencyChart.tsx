import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";

export type LatencyPoint = { ts: number; p50_ms: number; p95_ms: number; attempts: number };

// Цвета серий — те же токены, что у графика трафика: p50 синим (штатная
// медиана), p95 оранжевым (хвост, на который смотрят при разборе).
const C_P50 = "#5b9bf0";
const C_P95 = "#e0a458";

// fmtMs — «5,3 с» / «310 мс»: секунды читаются глазом, миллисекунды нужны
// только на быстрых узлах.
function fmtLatency(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} c`;
  return `${Math.round(ms)} мс`;
}

// niceCeil — верх шкалы, округлённый вверх до 1/2/5×10^n. Ось без этого
// показывает «30 490», и глаз считает деления вместо того, чтобы читать форму.
function niceCeil(v: number): number {
  if (v <= 0) return 1;
  const pow = 10 ** Math.floor(Math.log10(v));
  const n = v / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * pow;
}

// segments — точки, разбитые на непрерывные отрезки по признаку attempts > 0.
//
// Пустой интервал — РАЗРЫВ линии, а не ноль (§84.5): «узел отвечал за 0 мс» —
// прямая ложь, а на узле с ночным простоем таких интервалов половина окна.
// Отличить пустой интервал от настоящего нуля можно только по числу попыток.
function segments(data: LatencyPoint[]): LatencyPoint[][] {
  const out: LatencyPoint[][] = [];
  let cur: LatencyPoint[] = [];
  for (const p of data) {
    if (p.attempts > 0) {
      cur.push(p);
    } else if (cur.length) {
      out.push(cur);
      cur = [];
    }
  }
  if (cur.length) out.push(cur);
  return out;
}

/**
 * LatencyChart — p50/p95 по тем же интервалам, что и график трафика (§84.5).
 *
 * Стоит ПОД графиком трафика и в тех же столбцах: два числа за всё окно (p95 и
 * p99 в KPI) не отвечают на вопрос «когда было плохо» — на боевом acs_sigur они
 * описывали получасовой пик, а выглядели как характеристика суток.
 *
 * Единица — ПОПЫТКИ (строки), а не записи: длительность характеризует внешний
 * вызов (§79.5). Поэтому график и не может быть частью TrafficChart.
 */
export function LatencyChart({
  data,
  height = 120,
  className,
}: {
  data: LatencyPoint[];
  height?: number;
  className?: string;
}) {
  const { t } = useTranslation();
  const segs = segments(data);
  const hasData = segs.length > 0;

  const max = niceCeil(Math.max(0, ...data.map((p) => Math.max(p.p95_ms, p.p50_ms))));
  const t0 = data.length ? data[0].ts : 0;
  const t1 = data.length ? data[data.length - 1].ts : 1;
  const span = Math.max(t1 - t0, 1);

  // Координаты в процентах: SVG тянется по ширине контейнера, пересчитывать на
  // ресайз нечего.
  const x = (ts: number) => ((ts - t0) / span) * 100;
  const y = (v: number) => 100 - (Math.min(v, max) / max) * 100;
  const path = (seg: LatencyPoint[], pick: (p: LatencyPoint) => number) =>
    seg.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.ts).toFixed(2)},${y(pick(p)).toFixed(2)}`).join(" ");

  return (
    <div className={cn("w-full", className)}>
      <div className="mb-1 flex items-center gap-3 text-xs text-fg-muted">
        <span className="flex items-center gap-1">
          <span className="inline-block h-0.5 w-3" style={{ background: C_P50 }} />
          p50
        </span>
        <span className="flex items-center gap-1">
          <span className="inline-block h-0.5 w-3" style={{ background: C_P95 }} />
          p95
        </span>
        {hasData && <span className="ml-auto">{t("metrics.latency.axis_max", { v: fmtLatency(max) })}</span>}
      </div>

      {hasData ? (
        <svg
          viewBox="0 0 100 100"
          preserveAspectRatio="none"
          style={{ height }}
          className="w-full overflow-visible"
          role="img"
          aria-label={t("metrics.latency.title")}
        >
          {segs.map((seg, i) => (
            <g key={i}>
              {/* Отрезок из одной точки линией не рисуется — ставим маркер,
                  иначе одиночный всплеск на пустом окне не видно вовсе. */}
              {seg.length === 1 ? (
                <>
                  <circle cx={x(seg[0].ts)} cy={y(seg[0].p95_ms)} r={1.2} fill={C_P95} />
                  <circle cx={x(seg[0].ts)} cy={y(seg[0].p50_ms)} r={1.2} fill={C_P50} />
                </>
              ) : (
                <>
                  <path
                    d={path(seg, (p) => p.p95_ms)}
                    fill="none"
                    stroke={C_P95}
                    strokeWidth={1.2}
                    vectorEffect="non-scaling-stroke"
                  />
                  <path
                    d={path(seg, (p) => p.p50_ms)}
                    fill="none"
                    stroke={C_P50}
                    strokeWidth={1.2}
                    vectorEffect="non-scaling-stroke"
                  />
                </>
              )}
            </g>
          ))}
        </svg>
      ) : (
        <div className="flex items-center justify-center text-xs text-fg-subtle" style={{ height }}>
          {t("metrics.latency.no_data")}
        </div>
      )}
    </div>
  );
}
