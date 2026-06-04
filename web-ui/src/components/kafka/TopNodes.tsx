import { useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";

import type { KafkaByNode } from "../../api/client";
import { Card, Chip, Pill } from "../ui";
import { fmtNum } from "../../lib/format";

// TopNodes — два блока side-by-side (§5.7 spec): top producers и top failures.
export function TopNodes({ data }: { data: KafkaByNode }) {
  const { t } = useTranslation();
  return (
    <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
      <Card className="p-0">
        <div className="border-b border-line px-4 py-2.5 text-[13.5px] font-semibold">
          {t("kafka.top_producers.title")}
        </div>
        <table className="w-full text-[12.5px]">
          <thead>
            <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.node")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.messages")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.share")}</th>
            </tr>
          </thead>
          <tbody>
            {data.top_producers.map((p) => (
              <tr key={p.node_path} className="border-b border-line last:border-0 hover:bg-bg-muted">
                <td className="px-3 py-2 font-mono text-xs">{p.node_path}</td>
                <td className="px-3 py-2">{fmtNum(p.produced)}</td>
                <td className="px-3 py-2 text-fg-muted">{(p.share * 100).toFixed(1)}%</td>
              </tr>
            ))}
            {data.top_producers.length === 0 && (
              <tr>
                <td colSpan={3} className="px-3 py-6 text-center text-fg-muted">
                  {t("kafka.top_producers.empty")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>

      <Card className="p-0">
        <div className="border-b border-line px-4 py-2.5 text-[13.5px] font-semibold">
          {t("kafka.top_failures.title")}
        </div>
        {data.top_failures.length === 0 ? (
          <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-[12.5px] text-ok">
            <CheckCircle2 className="h-7 w-7" />
            {t("kafka.top_failures.empty")}
          </div>
        ) : (
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.node")}</th>
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.errors")}</th>
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.rate")}</th>
              </tr>
            </thead>
            <tbody>
              {data.top_failures.map((f) => {
                const pct = f.rate * 100;
                const row = pct > 5 ? "bg-err/10" : pct > 1 ? "bg-warn/10" : "";
                return (
                  <tr key={f.node_path} className={`border-b border-line last:border-0 ${row}`}>
                    <td className="px-3 py-2 font-mono text-xs">{f.node_path}</td>
                    <td className="px-3 py-2">{fmtNum(f.failed)}</td>
                    <td className="px-3 py-2">
                      {pct > 1 ? (
                        <Pill tone={pct > 5 ? "err" : "warn"}>{pct.toFixed(1)}%</Pill>
                      ) : (
                        <Chip>{pct < 0.001 ? "<0.001" : pct.toFixed(3)}%</Chip>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
