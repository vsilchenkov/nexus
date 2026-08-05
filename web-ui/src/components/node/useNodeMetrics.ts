import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { api, type NodeMetricsResp } from "../../api/client";
import {
  type ChartStep,
  type Period,
  periodParams,
  periodKey,
  stepKey,
  stepParams,
} from "../../lib/period";
import { useStableData } from "../../lib/useStableData";

// METRICS_REFETCH_MS — фолбэк интервала авто-обновления метрик (§28, Пункт 2),
// если сервер не вернул значение. Реальный интервал настраивается оператором
// (§44.C, app_settings.general.metrics_refetch_ms) и отдаётся в
// /api/settings/public — см. useMetricsRefetchMs.
export const METRICS_REFETCH_MS = 12_000;

// useMetricsRefetchMs — настраиваемый интервал автообновления метрик из
// public-настроек (§44.C). Кешируется (staleTime 5м) и переиспользует тот же
// query, что и useNodeUrlBuilder. Фолбэк — METRICS_REFETCH_MS.
export function useMetricsRefetchMs(): number {
  const q = useQuery({
    queryKey: ["public-settings"],
    queryFn: () => api.get<{ metrics_refetch_ms?: number }>("/api/settings/public"),
    staleTime: 5 * 60_000,
  });
  return q.data?.metrics_refetch_ms ?? METRICS_REFETCH_MS;
}

// SLOW_METRICS_MS — §79.4: порог, после которого авто-обновление для ЭТОГО
// набора фильтров выключается. Полнотекстовый фильтр читает тела с диска, а
// вкладка поллится каждые ~12 с — медленные запросы наложились бы друг на
// друга. Калька с логов (§77.3, SLOW_FILTER_MS).
export const SLOW_METRICS_MS = 2000;

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21/§28).
// Анти-мерцание через useStableData (держим последний chart_available=true).
//
// §79.4/§79.5: к периоду добавились «Шаг графика» и фильтры журнала. Оба обязаны
// входить в queryKey — иначе react-query отдаёт кеш прежнего набора и фильтр
// «работает» только визуально (грабля §72.2).
export function useNodeMetrics(
  id: string | undefined,
  period: Period,
  step: ChartStep = "auto",
  filterParams: Record<string, string> = {},
) {
  const refetch = useMetricsRefetchMs();
  // Ключ фильтров — стабильная строка: объект в queryKey сравнивается
  // структурно, но строкой оно и читается в devtools понятнее.
  const filterKey = JSON.stringify(filterParams);
  const [slow, setSlow] = useState(false);
  // Смена набора фильтров снимает пометку «медленно»: новый фильтр может быть
  // дешёвым, и держать авто-обновление выключенным незачем.
  useEffect(() => setSlow(false), [filterKey, id]);

  const q = useQuery({
    queryKey: ["node-metrics", id, periodKey(period), stepKey(step), filterKey],
    queryFn: async () => {
      const started = Date.now();
      const res = await api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, {
        ...periodParams(period),
        ...stepParams(step),
        ...filterParams,
      });
      setSlow(Date.now() - started > SLOW_METRICS_MS);
      return res;
    },
    enabled: !!id,
    refetchInterval: slow ? false : refetch,
  });
  const data = useStableData(
    q.data,
    `${id ?? ""}:${periodKey(period)}:${stepKey(step)}:${filterKey}`,
    (d) => d.chart_available,
  );
  return { ...q, data, slow };
}
