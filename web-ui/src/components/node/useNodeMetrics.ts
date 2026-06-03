import { useQuery } from "@tanstack/react-query";

import { api, type NodeMetricsResp } from "../../api/client";
import { type Period, periodParams, periodKey } from "../../lib/period";

// METRICS_REFETCH_MS — единый интервал авто-обновления метрик (§28, Пункт 2):
// все виджеты/графики метрик обновляются онлайн без перезагрузки страницы.
export const METRICS_REFETCH_MS = 12_000;

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21/§28).
export function useNodeMetrics(id: string | undefined, period: Period) {
  return useQuery({
    queryKey: ["node-metrics", id, periodKey(period)],
    queryFn: () => api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, periodParams(period)),
    enabled: !!id,
    refetchInterval: METRICS_REFETCH_MS,
  });
}
