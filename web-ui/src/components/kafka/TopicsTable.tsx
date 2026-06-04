import { useTranslation } from "react-i18next";

import type { KafkaTopic } from "../../api/client";
import { Card, Chip, Pill } from "../ui";
import { fmtBytes, fmtNum } from "../../lib/format";

// topicTone — состояние топика: offline → err, under-replicated/растущий lag →
// warn, иначе ok.
function topicHealth(tp: KafkaTopic): { tone: "ok" | "warn" | "err"; key: string } {
  if (tp.offline_partitions > 0) return { tone: "err", key: "offline" };
  if (tp.under_replicated > 0) return { tone: "warn", key: "under_replicated" };
  const lag = tp.consumer_groups.reduce((s, g) => s + g.lag_total, 0);
  if (lag > 1000) return { tone: "warn", key: "lag" };
  return { tone: "ok", key: "ok" };
}

// TopicsTable — таблица всех топиков кластера (§5.6 spec).
export function TopicsTable({ topics }: { topics: KafkaTopic[] }) {
  const { t } = useTranslation();
  return (
    <Card className="overflow-hidden p-0">
      <div className="border-b border-line px-4 py-2.5 text-[13.5px] font-semibold">
        {t("kafka.topics.title")}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-[12.5px]">
          <thead>
            <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
              <th className="px-3 py-2 font-medium">{t("kafka.topics.name")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.partitions")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.replication")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.size")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.messages")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.groups")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.topics.health")}</th>
            </tr>
          </thead>
          <tbody>
            {topics.map((tp) => {
              const h = topicHealth(tp);
              return (
                <tr key={tp.name} className="border-b border-line last:border-0 hover:bg-bg-muted">
                  <td className="px-3 py-2 font-mono text-xs">{tp.name}</td>
                  <td className="px-3 py-2">{tp.partitions}</td>
                  <td className="px-3 py-2">{tp.replication_factor}</td>
                  <td className="px-3 py-2">{fmtBytes(tp.size_bytes)}</td>
                  <td className="px-3 py-2">{fmtNum(tp.messages_estimate)}</td>
                  <td className="px-3 py-2">
                    {tp.consumer_groups.length === 0 ? (
                      <span className="text-fg-subtle">—</span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {tp.consumer_groups.map((g) => (
                          <Chip key={g.group} tone={g.lag_total > 1000 ? "warning" : g.lag_total > 0 ? "default" : "success"}>
                            {g.group} · lag {g.lag_total.toLocaleString()}
                          </Chip>
                        ))}
                      </div>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Pill tone={h.tone}>{t(`kafka.topics.state.${h.key}`)}</Pill>
                  </td>
                </tr>
              );
            })}
            {topics.length === 0 && (
              <tr>
                <td colSpan={7} className="px-3 py-6 text-center text-fg-muted">
                  {t("kafka.topics.empty")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
