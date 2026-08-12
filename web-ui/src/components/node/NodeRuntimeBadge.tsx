import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { Pill, Tooltip } from "../ui";

export type NodeRuntimeResp = {
  available: boolean;
  last_outcome: "ok" | "degraded" | "down";
  source: "redis" | "prometheus" | "none";
};

// NODE_RUNTIME_KEY — общий ключ: карточка защиты (§81.4) инвалидирует его
// после ручного сброса, потому что сброс гасит и персистентный бейдж §52.
// Без этого кнопка «сбросить» выглядела бы безрезультатной до планового
// обновления.
export const NODE_RUNTIME_KEY = "node-runtime";

// REFETCH_MS — бейдж не критичен по свежести: 30 с против 12 с у метрик.
// Тащить сюда общий интервал вкладки незачем — это одна строка в шапке.
const REFETCH_MS = 30_000;

/**
 * NodeRuntimeBadge — исход последнего исходящего вызова узла в шапке (§84.7).
 *
 * Закрывает follow-up §52: до раздела трёхсостоянье ok/degraded/down было
 * только на рабочем столе, а на странице узла — лишь конфигурационный статус
 * (Активен/На паузе/Выключен). Оператор видел «Активен» у узла, к которому
 * доставка не проходит вовсе.
 *
 * Правила отображения:
 *   ok       → бейджа НЕТ. Зелёная плашка на каждом узле — шум, который
 *              перестают замечать раньше, чем он понадобится;
 *   degraded → предупреждающий тон, down → ошибочный;
 *   available=false («неизвестно») → бейджа нет. «Неизвестно» НЕ красится в ok:
 *              соврать «всё в порядке» хуже, чем не сказать ничего.
 */
export function NodeRuntimeBadge({ nodeId }: { nodeId: string }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: [NODE_RUNTIME_KEY, nodeId],
    queryFn: () => api.get<NodeRuntimeResp>(`/api/nodes/${nodeId}/runtime`),
    enabled: !!nodeId,
    refetchInterval: REFETCH_MS,
  });

  const d = q.data;
  if (!d?.available || d.last_outcome === "ok") return null;

  const down = d.last_outcome === "down";
  return (
    <Tooltip
      content={
        <div className="max-w-xs space-y-1 text-left">
          <div>{down ? t("node.runtime.hint_down") : t("node.runtime.hint_degraded")}</div>
          <div>
            {d.source === "redis" ? t("node.runtime.src_redis") : t("node.runtime.src_prom")}
          </div>
        </div>
      }
    >
      <span>
        <Pill tone={down ? "err" : "warn"}>
          {down ? t("node.runtime.down") : t("node.runtime.degraded")}
        </Pill>
      </span>
    </Tooltip>
  );
}
