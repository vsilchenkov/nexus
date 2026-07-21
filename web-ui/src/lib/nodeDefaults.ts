import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";

// §64: дефолты формы СОЗДАНИЯ узла, задаваемые развёртыванием, а не UI.
// Приходят в тех же публичных настройках, что публичный адрес и интервал
// автообновления метрик (queryKey "public-settings" — team-независимый, см.
// TEAM_INDEPENDENT_KEYS; тот же запрос переиспользуют useNodeUrlBuilder и
// useMetricsRefetchMs, react-query его дедуплицирует).

// NODE_MAX_BODY_SIZE_FALLBACK — если сервер значение не вернул (старая сборка
// Web-сервиса). Совпадает с дефолтом web.node_default_max_body_size.
export const NODE_MAX_BODY_SIZE_FALLBACK = 50_000;

export type NodeFormDefaults = {
  maxBodySize: number;
};

export function useNodeFormDefaults(): NodeFormDefaults {
  const q = useQuery({
    queryKey: ["public-settings"],
    queryFn: () => api.get<{ node_default_max_body_size?: number }>("/api/settings/public"),
    staleTime: 5 * 60_000,
  });
  return { maxBodySize: q.data?.node_default_max_body_size ?? NODE_MAX_BODY_SIZE_FALLBACK };
}
