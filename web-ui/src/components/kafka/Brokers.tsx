import { useTranslation } from "react-i18next";
import { AlertTriangle, RefreshCw } from "lucide-react";

import type { KafkaTestResp } from "../../api/client";
import { Button, Card, Pill } from "../ui";
import { cn } from "../../lib/cn";

// Brokers — карточки брокеров из §4.5 ping (§5.8 spec). Leader-for/ISR не
// экспонируются per-broker high-level API — показываем адрес, online/offline,
// ping и предупреждения.
export function Brokers({
  data,
  onRecheck,
  rechecking,
}: {
  data: KafkaTestResp | undefined;
  onRecheck: () => void;
  rechecking: boolean;
}) {
  const { t } = useTranslation();
  const brokers = data?.brokers ?? [];
  const online = brokers.filter((b) => b.ok).length;

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between">
        <div className="text-[13.5px] font-semibold">
          {t("kafka.brokers.title")}
          <span className="ml-2 text-[11.5px] font-normal text-fg-subtle">
            {t("kafka.brokers.online", { online, total: brokers.length })}
          </span>
        </div>
        <Button sm onClick={onRecheck} disabled={rechecking}>
          <RefreshCw className={cn("h-3.5 w-3.5", rechecking && "animate-spin")} />
          {t("kafka.brokers.recheck")}
        </Button>
      </div>
      {brokers.length === 0 ? (
        <div className="py-6 text-center text-[12.5px] text-fg-muted">{t("kafka.brokers.empty")}</div>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {brokers.map((b) => {
            const warn = !b.ok || !!b.warn;
            return (
              <div
                key={b.addr}
                className={cn(
                  "relative rounded-md border bg-bg px-3.5 py-3",
                  b.ok ? (b.warn ? "border-warn/40" : "border-line") : "border-err/40",
                )}
              >
                <div className="mb-2 flex items-center justify-between">
                  <span className="font-mono text-[13px] font-medium">{b.addr}</span>
                  <Pill tone={b.ok ? (b.warn ? "warn" : "ok") : "err"}>
                    {b.ok ? t("kafka.brokers.up") : t("kafka.brokers.down")}
                  </Pill>
                </div>
                <div className="border-t border-line pt-2 text-[11px] text-fg-muted">
                  <span className="uppercase tracking-wide">{t("kafka.brokers.ping")}</span>{" "}
                  <span className={cn("font-semibold", b.warn && "text-warn")}>{b.elapsed_ms} ms</span>
                </div>
                {warn && b.warn && (
                  <div className="mt-1.5 flex items-center gap-1 text-[10.5px] text-warn">
                    <AlertTriangle className="h-3 w-3" />
                    {t(`kafka.brokers.warn.${b.warn === "unreachable" ? "unreachable" : "latency"}`)}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Card>
  );
}
