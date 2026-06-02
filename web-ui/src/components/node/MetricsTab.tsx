import { useState } from "react";
import { useTranslation } from "react-i18next";

import { type Node } from "../../api/client";
import { Card, Hint, Kpi, KpiRow, PeriodPicker, TrafficChart, type Period } from "../ui";
import { fmtNum } from "../../lib/format";
import { useNodeMetrics } from "./useNodeMetrics";

// MetricsTab — вкладка «Метрики» узла (§21): перцентили + счётчики + график
// за выбранный период. Источник — ClickHouse (точные quantile). По умолчанию 24h.
export function MetricsTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  const [period, setPeriod] = useState<Period>({ kind: "preset", range: "24h" });
  const m = useNodeMetrics(node.id, period);
  const kpi = m.data?.kpi;
  const chartUnavailable = m.data && !m.data.chart_available;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-semibold">{t("node.tabs.metrics")}</span>
        <PeriodPicker value={period} onChange={setPeriod} />
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
