import { useState } from "react";
import { useTranslation } from "react-i18next";

import { type Node } from "../../api/client";
import { Card, Hint, Kpi, KpiRow, Seg, TrafficChart } from "../ui";
import { fmtNum } from "../../lib/format";
import { useNodeMetrics, type MetricsRange } from "./useNodeMetrics";

const RANGES: MetricsRange[] = ["15m", "1h", "24h", "7d"];

// MetricsTab — вкладка «Метрики» узла (§21): перцентили + счётчики + график
// за выбранный диапазон. Источник — ClickHouse (точные quantile).
export function MetricsTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  const [range, setRange] = useState<MetricsRange>("24h");
  const m = useNodeMetrics(node.id, range);
  const kpi = m.data?.kpi;
  const chartUnavailable = m.data && !m.data.chart_available;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-sm font-semibold">{t("node.tabs.metrics")}</span>
        <Seg
          value={range}
          onChange={setRange}
          options={RANGES.map((r) => ({ value: r, label: t(`metrics.range.${r}`) }))}
        />
      </div>

      {chartUnavailable && (
        <Hint tone="muted">{t("logs.not_configured")}</Hint>
      )}

      <KpiRow>
        <Kpi label={t("metrics.kpi.total")} value={kpi ? fmtNum(kpi.total) : "—"} />
        <Kpi label={t("metrics.kpi.delivered")} value={kpi ? fmtNum(kpi.delivered) : "—"} />
        <Kpi
          label={t("metrics.kpi.p95")}
          value={kpi ? <>{Math.round(kpi.p95_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
        />
        <Kpi
          label={t("metrics.kpi.p99")}
          value={kpi ? <>{Math.round(kpi.p99_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
        />
      </KpiRow>

      <Card>
        <div className="mb-3 text-sm font-semibold">{t("metrics.kpi.requests")}</div>
        <TrafficChart data={m.data?.series ?? []} height={180} />
      </Card>
    </div>
  );
}
