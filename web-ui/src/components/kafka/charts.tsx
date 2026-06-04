import { useMemo } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Line,
  LineChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import type { KafkaPoint } from "../../api/client";

// Цвета линий — эталон мокапа (specs/nexus_kafka_monitoring_ui.html).
const COLOR = {
  produced: "#5b9bf0",
  consumed: "#4cc38a",
  errors: "#e85d5c",
  lag: "#d9a441",
};

const axisProps = {
  stroke: "#5f6875",
  tick: { fill: "#5f6875", fontSize: 10 },
  tickLine: false,
  axisLine: false,
} as const;

const tooltipStyle = {
  background: "#1c2027",
  border: "0.5px solid #38404a",
  borderRadius: 6,
  fontSize: 11,
} as const;

// fmtTime — подпись оси X по масштабу окна: короткие периоды → HH:MM,
// длинные (>2 дней) → день/месяц.
function fmtTime(ts: number, longRange: boolean): string {
  const d = new Date(ts);
  if (longRange) return d.toLocaleDateString(undefined, { day: "2-digit", month: "short" });
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

type ThroughputRow = { t: number; produced?: number; consumed?: number; errors?: number };

// mergeSeries сводит ряды produced/consumed/errors в строки по метке времени.
function mergeSeries(series: Record<string, KafkaPoint[]>): ThroughputRow[] {
  const byT = new Map<number, ThroughputRow>();
  const put = (key: "produced" | "consumed" | "errors") => {
    for (const p of series[key] ?? []) {
      const row = byT.get(p.t) ?? { t: p.t };
      row[key] = p.v;
      byT.set(p.t, row);
    }
  };
  put("produced");
  put("consumed");
  put("errors");
  return [...byT.values()].sort((a, b) => a.t - b.t);
}

// ThroughputChart — главный график (§5.4): produced/consumed сплошные, errors
// пунктир. Высота ~280px.
export function ThroughputChart({
  series,
  longRange,
  labels,
}: {
  series: Record<string, KafkaPoint[]>;
  longRange: boolean;
  labels: { produced: string; consumed: string; errors: string };
}) {
  const data = useMemo(() => mergeSeries(series), [series]);
  return (
    <ResponsiveContainer width="100%" height={280}>
      <LineChart data={data} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
        <CartesianGrid stroke="#2a3038" strokeWidth={0.5} vertical={false} />
        <XAxis
          dataKey="t"
          type="number"
          domain={["dataMin", "dataMax"]}
          scale="time"
          tickFormatter={(v) => fmtTime(v, longRange)}
          {...axisProps}
        />
        <YAxis {...axisProps} width={40} />
        <Tooltip
          contentStyle={tooltipStyle}
          labelFormatter={(v) => fmtTime(Number(v), longRange)}
          formatter={(value, name) => [Number(value).toFixed(1) + "/s", name]}
        />
        <Line type="monotone" dataKey="produced" name={labels.produced} stroke={COLOR.produced} strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="consumed" name={labels.consumed} stroke={COLOR.consumed} strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="errors" name={labels.errors} stroke={COLOR.errors} strokeWidth={1.5} strokeDasharray="4 4" dot={false} isAnimationActive={false} />
      </LineChart>
    </ResponsiveContainer>
  );
}

// LagChart — отдельный график lag (§5.5): линия + пороговая линия критичности
// с подсветкой зоны выше порога. Высота ~170px.
export function LagChart({
  points,
  threshold,
  longRange,
  lagLabel,
}: {
  points: KafkaPoint[];
  threshold: number;
  longRange: boolean;
  lagLabel: string;
}) {
  const data = useMemo(() => points.map((p) => ({ t: p.t, lag: p.v })), [points]);
  return (
    <ResponsiveContainer width="100%" height={170}>
      <AreaChart data={data} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
        <defs>
          <linearGradient id="lagFill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={COLOR.lag} stopOpacity={0.35} />
            <stop offset="100%" stopColor={COLOR.lag} stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid stroke="#2a3038" strokeWidth={0.5} vertical={false} />
        <XAxis
          dataKey="t"
          type="number"
          domain={["dataMin", "dataMax"]}
          scale="time"
          tickFormatter={(v) => fmtTime(v, longRange)}
          {...axisProps}
        />
        <YAxis {...axisProps} width={40} />
        <Tooltip
          contentStyle={tooltipStyle}
          labelFormatter={(v) => fmtTime(Number(v), longRange)}
          formatter={(value) => [Math.round(Number(value)).toLocaleString(), lagLabel]}
        />
        {threshold > 0 && (
          <ReferenceLine y={threshold} stroke={COLOR.errors} strokeDasharray="6 4" strokeOpacity={0.6} />
        )}
        <Area type="monotone" dataKey="lag" name={lagLabel} stroke={COLOR.lag} strokeWidth={2} fill="url(#lagFill)" isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}

// MiniSpark — мини-спарклайн для KPI/строк таблицы.
export function MiniSpark({ values, color = COLOR.produced, height = 22 }: { values: number[]; color?: string; height?: number }) {
  const data = useMemo(() => values.map((v, i) => ({ i, v })), [values]);
  if (data.length === 0) return null;
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 2, right: 0, left: 0, bottom: 0 }}>
        <Area type="monotone" dataKey="v" stroke={color} strokeWidth={1.5} fill={color} fillOpacity={0.18} isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}
