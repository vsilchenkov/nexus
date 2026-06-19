import { useRef } from "react";
import { useQuery } from "@tanstack/react-query";

import { api, type NodeMetricsResp } from "../../api/client";
import { type Period, periodParams, periodKey } from "../../lib/period";

// METRICS_REFETCH_MS — единый интервал авто-обновления метрик (§28, Пункт 2):
// все виджеты/графики метрик обновляются онлайн без перезагрузки страницы.
export const METRICS_REFETCH_MS = 12_000;

// useNodeMetrics — KPI + ряд графика узла из /api/metrics/nodes/:id (§21/§28).
//
// Анти-мерцание: бэкенд при ОШИБКЕ Prometheus (таймаут и т.п.) штатно отдаёт
// chart_available=false с пустыми KPI/series (чтобы поллинг не спамил 500).
// Чтобы при транзиентной ошибке чарт/счётчики НЕ гасли «то есть, то нет»,
// держим последний УДАЧНЫЙ ответ (chart_available=true) и отдаём его, пока
// прилетают деградированные. Реальное «нет данных» (chart_available=true +
// пустой series) не маскируем.
export function useNodeMetrics(id: string | undefined, period: Period) {
  const key = `${id ?? ""}:${periodKey(period)}`;
  const lastGood = useRef<{ key: string; data: NodeMetricsResp } | undefined>(undefined);
  const q = useQuery({
    queryKey: ["node-metrics", id, periodKey(period)],
    queryFn: () => api.get<NodeMetricsResp>(`/api/metrics/nodes/${id}`, periodParams(period)),
    enabled: !!id,
    refetchInterval: METRICS_REFETCH_MS,
  });

  // Сбрасываем «последний удачный» при смене узла/периода — чтобы не показать
  // данные другого окна.
  if (lastGood.current && lastGood.current.key !== key) {
    lastGood.current = undefined;
  }
  if (q.data?.chart_available) {
    lastGood.current = { key, data: q.data };
  }
  const data = q.data && !q.data.chart_available && lastGood.current ? lastGood.current.data : q.data;
  return { ...q, data };
}
