import { useMemo } from "react";
import { useTranslation } from "react-i18next";
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
import { fmtNum } from "../../lib/format";
import { ChartTooltip, type TooltipSeries } from "../ui/ChartTooltip";

// Цвета линий — эталон мокапа (specs/nexus_kafka_monitoring_ui.html). Совпадают
// с дизайн-токенами accent/ok/err/warn; используются как data-driven цвета серий
// (в т.ч. как маркеры в тултипе §33), поэтому остаются hex.
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

// Курсор-кроссхэйр под тултипом — пунктирная вертикаль (эталон §33).
const cursorStyle = { stroke: "#9aa3b2", strokeWidth: 0.5, strokeDasharray: "2 3" } as const;

// fmtTime — подпись оси X по масштабу окна: короткие периоды → HH:MM,
// длинные (>2 дней) → день/месяц.
function fmtTime(ts: number, longRange: boolean): string {
  const d = new Date(ts);
  if (longRange) return d.toLocaleDateString(undefined, { day: "2-digit", month: "short" });
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

// fmtRate — компактный формат rate (msg/сек): крупные округляем, мелкие с 1 знаком.
function fmtRate(v: number): string {
  return v >= 100 ? Math.round(v).toString() : v.toFixed(1);
}

// stepNote — подпись шага агрегации для шапки тултипа ("30-сек. окно").
function stepNote(stepSeconds: number | undefined, t: (k: string, o?: Record<string, unknown>) => string): string | undefined {
  if (!stepSeconds || stepSeconds <= 0) return undefined;
  if (stepSeconds < 60) return t("kafka.tooltip.window_s", { n: stepSeconds });
  return t("kafka.tooltip.window_m", { n: Math.round(stepSeconds / 60) });
}

// recharts передаёт в content-render объект с active/payload/label.
type RechartsPayloadItem = { dataKey?: string | number; name?: string; value?: number; color?: string; stroke?: string };
type RechartsTipProps = { active?: boolean; payload?: RechartsPayloadItem[]; label?: string | number };

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

// ThroughputTip — тултип multi-line графика: серии с маркерами line/dashed,
// сортировка по убыванию, подвал при consumed < produced (§33.3).
function ThroughputTip({
  active,
  payload,
  label,
  longRange,
  stepSeconds,
}: RechartsTipProps & { longRange: boolean; stepSeconds?: number }) {
  const { t } = useTranslation();
  if (!active || !payload?.length) return null;
  const rows = payload.filter((p) => p.value != null);
  if (rows.length === 0) return null;

  const valueOf = (k: string) => rows.find((r) => r.dataKey === k)?.value;
  const produced = valueOf("produced");
  const consumed = valueOf("consumed");
  const unit = t("kafka.tooltip.rate_unit");

  const series: TooltipSeries[] = rows
    .map((p) => ({
      v: Number(p.value),
      row: {
        marker: (p.dataKey === "errors" ? "dashed" : "line") as TooltipSeries["marker"],
        color: p.color ?? p.stroke ?? "#9aa3b2",
        name: p.name ?? String(p.dataKey),
        value: fmtRate(Number(p.value)),
        unit,
      },
    }))
    .sort((a, b) => b.v - a.v)
    .map((x) => x.row);

  const footer =
    produced != null && consumed != null && consumed < produced
      ? {
          tone: "warn" as const,
          text: t("kafka.tooltip.consumed_lt_produced"),
          delta: { dir: "down" as const, text: `−${fmtRate(produced - consumed)}${unit}` },
        }
      : undefined;

  return (
    <ChartTooltip
      period={{ from: fmtTime(Number(label), longRange), note: stepNote(stepSeconds, t) }}
      series={series}
      footer={footer}
    />
  );
}

// ThroughputChart — главный график (§5.4): produced/consumed сплошные, errors
// пунктир. Высота ~280px.
export function ThroughputChart({
  series,
  longRange,
  labels,
  stepSeconds,
}: {
  series: Record<string, KafkaPoint[]>;
  longRange: boolean;
  labels: { produced: string; consumed: string; errors: string };
  stepSeconds?: number;
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
          cursor={cursorStyle}
          content={<ThroughputTip longRange={longRange} stepSeconds={stepSeconds} />}
        />
        <Line type="monotone" dataKey="produced" name={labels.produced} stroke={COLOR.produced} strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="consumed" name={labels.consumed} stroke={COLOR.consumed} strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="errors" name={labels.errors} stroke={COLOR.errors} strokeWidth={1.5} strokeDasharray="4 4" dot={false} isAnimationActive={false} />
      </LineChart>
    </ResponsiveContainer>
  );
}

// LagTip — тултип lag-графика: крупный lag-агрегат с tone по близости к порогу,
// подвал-предупреждение о пороге. Разбивки по партициям нет — данных нет (§33.7).
function LagTip({
  active,
  payload,
  label,
  longRange,
  threshold,
}: RechartsTipProps & { longRange: boolean; threshold: number }) {
  const { t } = useTranslation();
  if (!active || !payload?.length) return null;
  const v = Number(payload[0].value);
  const tone: "normal" | "warn" | "err" =
    threshold > 0 && v >= threshold ? "err" : threshold > 0 && v >= threshold * 0.8 ? "warn" : "normal";

  // TODO §33.7: разбивка lag по партициям — нет данных в timeseries
  // (Prometheus отдаёт sum(nexus_kafka_lag)); задел на v2.
  const footer =
    tone === "err"
      ? { tone: "err" as const, text: t("kafka.tooltip.threshold", { n: threshold.toLocaleString() }) }
      : tone === "warn"
        ? { tone: "warn" as const, text: t("kafka.tooltip.threshold_near", { n: threshold.toLocaleString() }) }
        : undefined;

  return (
    <ChartTooltip
      period={{ from: fmtTime(Number(label), longRange), note: t("kafka.tooltip.moment") }}
      primary={{ value: Math.round(v).toLocaleString(), unit: t("kafka.tooltip.lag_unit"), tone }}
      footer={footer}
    />
  );
}

// LagChart — отдельный график lag (§5.5): линия + пороговая линия критичности
// с подсветкой зоны выше порога. Высота ~170px.
export function LagChart({
  points,
  threshold,
  longRange,
}: {
  points: KafkaPoint[];
  threshold: number;
  longRange: boolean;
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
        <Tooltip cursor={cursorStyle} content={<LagTip longRange={longRange} threshold={threshold} />} />
        {threshold > 0 && (
          <ReferenceLine y={threshold} stroke={COLOR.errors} strokeDasharray="6 4" strokeOpacity={0.6} />
        )}
        <Area type="monotone" dataKey="lag" stroke={COLOR.lag} strokeWidth={2} fill="url(#lagFill)" isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}

// MiniTip — компактный тултип спарклайна (только главное значение, без времени:
// в MiniSpark нет временных меток, только массив значений).
function MiniTip({ active, payload, unit }: RechartsTipProps & { unit: string }) {
  if (!active || !payload?.length) return null;
  return <ChartTooltip compact primary={{ value: fmtNum(Number(payload[0].value)), unit }} />;
}

// MiniSpark — мини-спарклайн для KPI/строк таблицы. При заданном `unit` —
// лёгкий тултип с текущим значением (§33.3).
export function MiniSpark({
  values,
  color = COLOR.produced,
  height = 22,
  unit,
}: {
  values: number[];
  color?: string;
  height?: number;
  unit?: string;
}) {
  const data = useMemo(() => values.map((v, i) => ({ i, v })), [values]);
  if (data.length === 0) return null;
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 2, right: 0, left: 0, bottom: 0 }}>
        {unit !== undefined && <Tooltip cursor={false} content={<MiniTip unit={unit} />} />}
        <Area type="monotone" dataKey="v" stroke={color} strokeWidth={1.5} fill={color} fillOpacity={0.18} isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}
