import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";

// §70.8: идентификатор ноды (инстанса) шины и префикс имён её БД ClickHouse.
//
// Нода — самостоятельное развёртывание со своими PostgreSQL/Redis/Kafka,
// пишущее логи в общий с другими нодами ClickHouse. У ноды без идентификатора
// (единственная/первая) оба значения «нулевые»: бейдж не рендерится, префикс —
// прежний "nexus_", то есть интерфейс не меняется.

// CH_DATABASE_PREFIX_FALLBACK — если сервер поле не вернул (старая сборка Web).
export const CH_DATABASE_PREFIX_FALLBACK = "nexus_";

type PublicInstanceSettings = {
  ch_database_prefix?: string;
};

type VersionInfo = {
  version: string;
  commit?: string;
  build_date?: string;
  override_allowed?: boolean;
  instance?: string;
};

// useInstanceID — код ноды ("" у ноды без идентификатора).
//
// Данные берутся из уже существующего запроса версии (queryKey "version",
// его же читает Sidebar) — дополнительного сетевого вызова не появляется.
export function useInstanceID(): string {
  const q = useQuery({
    queryKey: ["version"],
    queryFn: () => api.get<VersionInfo>("/api/version"),
    staleTime: Infinity,
  });
  return q.data?.instance ?? "";
}

// useCHDatabasePrefix — префикс имён БД ClickHouse этой ноды: "nexus_" либо
// "nexus_<id>_". Нужен предпросмотру имени БД в диалоге создания команды:
// склеивать литерал на клиенте нельзя — на ноде с идентификатором он показывал
// бы чужое имя. Тот же запрос, что у useNodeFormDefaults (дедуплицируется).
export function useCHDatabasePrefix(): string {
  const q = useQuery({
    queryKey: ["public-settings"],
    queryFn: () => api.get<PublicInstanceSettings>("/api/settings/public"),
    staleTime: 5 * 60_000,
  });
  return q.data?.ch_database_prefix || CH_DATABASE_PREFIX_FALLBACK;
}
