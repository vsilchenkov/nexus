import { useTranslation } from "react-i18next";
import { AlertTriangle, CheckCircle2, XCircle } from "lucide-react";

import type { KafkaOverview, KafkaSeverity } from "../../api/client";
import { cn } from "../../lib/cn";

const tone: Record<KafkaSeverity, { box: string; icon: React.ReactNode }> = {
  ok: { box: "bg-ok/10 border-ok/40 text-ok", icon: <CheckCircle2 className="h-5 w-5" /> },
  warn: { box: "bg-warn/10 border-warn/40 text-warn", icon: <AlertTriangle className="h-5 w-5" /> },
  err: { box: "bg-err/10 border-err/40 text-err", icon: <XCircle className="h-5 w-5" /> },
};

// HealthBanner — баннер состояния кластера (§5.2 spec). Severity и reason
// приходят с бэка; конкретный текст локализуется здесь с подстановкой чисел из
// summary/broker_health (бэкенд i18n-независим).
export function HealthBanner({ ov }: { ov: KafkaOverview }) {
  const { t } = useTranslation();
  const sev = ov.health.severity;
  const reason = ov.health.reason;
  const interp = {
    lag: ov.summary.current_lag.toLocaleString(),
    error_rate: (ov.error_rate * 100).toFixed(3),
    offline: ov.broker_health.offline_partitions,
    under_replicated: ov.broker_health.under_replicated_partitions,
    online: ov.broker_health.brokers_online,
    total: ov.broker_health.brokers_total,
  };
  return (
    <div className={cn("flex items-start gap-3 rounded-md border px-4 py-3.5", tone[sev].box)}>
      <span className="mt-0.5 shrink-0">{tone[sev].icon}</span>
      <div>
        <div className="text-sm font-semibold">{t(`kafka.health.${reason}.title`)}</div>
        <div className="mt-0.5 text-[12.5px] opacity-90">
          {t(`kafka.health.${reason}.details`, interp)}
        </div>
      </div>
    </div>
  );
}
