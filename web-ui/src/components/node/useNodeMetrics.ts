import { useQuery } from "@tanstack/react-query";

import { api, type NodeMetricsResp } from "../../api/client";

export type MetricsRange = "15m" | "1h" | "24h" | "7d";

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21).
export function useNodeMetrics(id: string | undefined, range: MetricsRange) {
  return useQuery({
    queryKey: ["node-metrics", id, range],
    queryFn: () => api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, { range }),
    enabled: !!id,
    refetchInterval: 30_000,
  });
}
