import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ShieldOff, ShieldCheck, RotateCcw } from "lucide-react";

import { api } from "../../api/client";
import { Button, Hint } from "../ui";
import { useConfirm } from "../../lib/confirm";
import { useRoleAtLeast } from "../../lib/useCurrentRole";

// §81.4: состояние защиты узла (circuit breaker) и её ручной сброс.
export type BreakerState = {
  available: boolean;
  exists: boolean;
  state: "closed" | "open" | "half_open";
  failures: number;
  threshold: number;
  opened_at: string;
  retry_after_ms: number;
};

type ResetResult = {
  was_open: boolean;
  previous_state: string;
  previous_failures: number;
  node_status_cleared: boolean;
};

// Открытая защита опрашивается часто (оператор ждёт, когда пройдёт проба),
// закрытая — редко: там меняться нечему, а вкладка живёт открытой часами.
const POLL_OPEN_MS = 5_000;
const POLL_CLOSED_MS = 30_000;

/** mmss — обратный отсчёт «0:12» из миллисекунд. */
function mmss(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function BreakerCard({ nodeId }: { nodeId: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();
  const isManager = useRoleAtLeast("manager");

  const q = useQuery({
    queryKey: ["node-breaker", nodeId],
    queryFn: () => api.get<BreakerState>(`/api/nodes/${nodeId}/breaker`),
    refetchInterval: (query) => (query.state.data?.state === "open" ? POLL_OPEN_MS : POLL_CLOSED_MS),
  });

  const reset = useMutation({
    mutationFn: () => api.post<ResetResult>(`/api/nodes/${nodeId}/breaker/reset`, {}),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node-breaker", nodeId] });
      // Сброс снимает и персистентный бейдж «Down» (§52). Он приходит НЕ с
      // конфигурацией узла (["node", id]), а с метриками: last_outcome живёт в
      // ответе /api/metrics/nodes (список — ["nodes", …]) и в KPI узла
      // (["node-metrics", …]). Инвалидируем их, иначе бейдж останется красным
      // до ближайшего планового рефетча.
      qc.invalidateQueries({ queryKey: ["nodes"] });
      qc.invalidateQueries({ queryKey: ["node-metrics"] });
    },
  });

  const st = q.data;
  const open = st?.state === "open";

  // Обратный отсчёт тикает локально: дёргать сервер ради секундной стрелки
  // незачем, состояние всё равно опрашивается по refetchInterval.
  const [leftMs, setLeftMs] = useState(0);
  useEffect(() => {
    setLeftMs(st?.retry_after_ms ?? 0);
    if (!open || !st?.retry_after_ms) return;
    const id = setInterval(() => setLeftMs((v) => Math.max(0, v - 1000)), 1000);
    return () => clearInterval(id);
  }, [st?.retry_after_ms, open]);

  // Redis не сконфигурирован — защиты в этой инсталляции нет вообще, показывать
  // нечего. Ошибку запроса тоже не выносим в карточку: вкладка не должна
  // краснеть из-за второстепенного блока.
  if (!st?.available) return null;

  const failures =
    st.threshold > 0 ? `${st.failures}/${st.threshold}` : String(st.failures);

  return (
    <Hint tone={open ? "danger" : "muted"} icon={open ? <ShieldOff className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />}>
      <div className="flex w-full flex-wrap items-start justify-between gap-2">
        <div className="space-y-1">
          <p className="font-semibold">
            {open
              ? t("queue.breaker.state_open")
              : st.state === "half_open"
                ? t("queue.breaker.state_half_open")
                : t("queue.breaker.state_closed")}
            {/* Счётчик показывается и при закрытой защите: «3 из 5» — это
                предупреждение «узел на грани», ради него политика и лежит в
                состоянии (§81.3.1). При открытой он всегда равен порогу. */}
            {st.failures > 0 && (
              <>
                {" · "}
                {t("queue.breaker.failures", { value: failures })}
              </>
            )}
            {open && leftMs > 0 && (
              <>
                {" · "}
                {t("queue.breaker.retry_after", { value: mmss(leftMs) })}
              </>
            )}
          </p>
          {open && (
            <>
              <p>{t("queue.breaker.explain_open")}</p>
              <p className="text-fg-muted">{t("queue.breaker.badge_hint")}</p>
            </>
          )}
        </div>
        {isManager && (
          <Button
            sm
            variant="ghost"
            disabled={reset.isPending}
            onClick={async () => {
              if (
                await confirm({
                  title: t("queue.breaker.reset"),
                  message: t("queue.breaker.reset_confirm"),
                  confirmLabel: t("queue.breaker.reset"),
                })
              )
                reset.mutate();
            }}
          >
            <RotateCcw className="h-3.5 w-3.5" /> {t("queue.breaker.reset")}
          </Button>
        )}
      </div>
    </Hint>
  );
}
