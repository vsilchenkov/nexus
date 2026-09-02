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

// §97: ПОТОЛКИ рабочих лимитов размера тела. Приходят из того же публичного
// эндпоинта и задаются развёртыванием (receiver.max_body_bytes /
// max_async_body_bytes): под них настроены nginx, gRPC, Kafka и память
// сервисов, поэтому форма настроек не даёт задать больше.
//
// BODY_LIMIT_CAP_FALLBACK — если сервер потолки не вернул (старая сборка Web).
// Ноль означает «потолок неизвестен»: форма тогда не ограничивает ввод сверху,
// а решение остаётся за серверной валидацией — она есть в любом случае.
export const BODY_LIMIT_CAP_FALLBACK = 0;

export type BodyLimitCaps = {
  syncMaxBytes: number;
  asyncMaxBytes: number;
};

export function useBodyLimitCaps(): BodyLimitCaps {
  const q = useQuery({
    queryKey: ["public-settings"],
    queryFn: () =>
      api.get<{ max_body_bytes_cap?: number; max_async_body_bytes_cap?: number }>(
        "/api/settings/public",
      ),
    staleTime: 5 * 60_000,
  });
  return {
    syncMaxBytes: q.data?.max_body_bytes_cap ?? BODY_LIMIT_CAP_FALLBACK,
    asyncMaxBytes: q.data?.max_async_body_bytes_cap ?? BODY_LIMIT_CAP_FALLBACK,
  };
}
