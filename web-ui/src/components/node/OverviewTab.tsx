import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api, type Node } from "../../api/client";
import { Card, Kpi, KpiRow, LabelHint, Pill, PeriodPicker, TrafficChart, defaultPeriod, type LogsRange, type Period } from "../ui";
import { fmtNum } from "../../lib/format";
import { useNodeMetrics, METRICS_REFETCH_MS } from "./useNodeMetrics";
import { type LogsResp } from "./types";

// OverviewTab — вкладка «Обзор» узла (§21): 4 KPI + график трафика + последние
// запросы. KPI/график — из ClickHouse через /api/metrics; таблица — из логов.
// onOpenLogs (§33.4) — клик по столбцу графика открывает логи за момент.
export function OverviewTab({
  node,
  onAllLogs,
  onOpenLogs,
}: {
  node: Node;
  onAllLogs: () => void;
  onOpenLogs?: (range: LogsRange) => void;
}) {
  const { t } = useTranslation();
  const [period, setPeriod] = useState<Period>(defaultPeriod);
  const m = useNodeMetrics(node.id, period);
  const hasLogsTable = !!node.clickhouse_table;

  const recentQ = useQuery({
    queryKey: ["logs", node.id, { limit: 8 }],
    queryFn: () => api.get<LogsResp>(`/api/nodes/${node.id}/logs`, { limit: 8 }),
    enabled: hasLogsTable,
    refetchInterval: METRICS_REFETCH_MS,
  });

  const kpi = m.data?.kpi;
  const deliveredPct =
    kpi && kpi.total > 0 ? ((kpi.delivered / kpi.total) * 100).toFixed(1) + "%" : undefined;

  return (
    <div className="space-y-4">
      {node.comment && (
        <Card>
          <div className="mb-1 text-[11px] uppercase tracking-wide text-fg-subtle">
            {t("node.form.comment")}
          </div>
          <p className="whitespace-pre-wrap text-[13px] text-fg-muted">{node.comment}</p>
        </Card>
      )}
      <KpiRow>
        <Kpi label={t("metrics.kpi.in")} value={kpi ? fmtNum(kpi.total) : "—"} hint={t("metrics.hints.in")} />
        <Kpi
          label={t("metrics.kpi.delivered")}
          value={kpi ? fmtNum(kpi.delivered) : "—"}
          delta={deliveredPct}
          deltaTone="up"
          hint={t("metrics.hints.delivered")}
        />
        <Kpi
          label={t("metrics.kpi.p95")}
          value={kpi ? <>{Math.round(kpi.p95_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
          delta={kpi ? `p99 ${Math.round(kpi.p99_ms)} ms` : undefined}
          hint={t("metrics.hints.p95")}
        />
        <Kpi
          label={t("metrics.kpi.errors")}
          value={kpi ? fmtNum(kpi.errors) : "—"}
          deltaTone={kpi && kpi.errors > 0 ? "down" : "muted"}
          hint={t("metrics.hints.errors")}
        />
      </KpiRow>

      <Card>
        <div className="mb-3 flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-sm font-semibold">
            {t("metrics.traffic")}
            <LabelHint content={t("metrics.hints.traffic")} />
          </span>
          <PeriodPicker value={period} onChange={setPeriod} />
        </div>
        <TrafficChart data={m.data?.series ?? []} onOpenLogs={hasLogsTable ? onOpenLogs : undefined} />
      </Card>

      <Card className="p-0">
        <div className="flex items-center justify-between px-4 py-3">
          <span className="text-sm font-semibold">{t("metrics.recent")}</span>
          <button
            type="button"
            onClick={onAllLogs}
            className="text-xs text-fg-muted hover:text-accent"
          >
            {t("metrics.all_logs")}
          </button>
        </div>
        <table className="w-full text-[12.5px]">
          <thead>
            <tr className="border-y border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
              <th className="px-4 py-2 font-medium">{t("logs.col.time")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.status")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.ms")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.method")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.url")}</th>
            </tr>
          </thead>
          <tbody>
            {(recentQ.data?.items ?? []).map((r) => {
              const isErr = !r.done || r.status >= 400 || r.status === 0;
              return (
                <tr key={r.id} className={`border-b border-line last:border-0 ${isErr ? "bg-err/5" : ""}`}>
                  <td className="px-4 py-2 font-mono text-xs">
                    {new Date(r.date_request).toLocaleTimeString()}
                  </td>
                  <td className="px-4 py-2">
                    <Pill tone={isErr ? "err" : "ok"}>{r.status}</Pill>
                  </td>
                  <td className="px-4 py-2 font-mono">{r.duration_ms} ms</td>
                  <td className="px-4 py-2 font-mono">{r.method}</td>
                  <td className="max-w-[24rem] truncate px-4 py-2 font-mono text-fg-muted">{r.url}</td>
                </tr>
              );
            })}
            {hasLogsTable && (recentQ.data?.items?.length ?? 0) === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-6 text-center text-fg-muted">
                  {recentQ.isLoading ? t("common.loading") : t("logs.empty")}
                </td>
              </tr>
            )}
            {!hasLogsTable && (
              <tr>
                <td colSpan={5} className="px-4 py-6 text-center text-fg-muted">
                  {t("logs.not_configured")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
