import { useParams, Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Pencil } from "lucide-react";
import { useState } from "react";

import { api, type Node } from "../api/client";
import { Button, Chip, Pill } from "../components/ui";
import { cn } from "../lib/cn";
import { LogsTab } from "../components/node/LogsTab";
import { OverviewTab } from "../components/node/OverviewTab";
import { ConfigTab } from "../components/node/ConfigTab";
import { MetricsTab } from "../components/node/MetricsTab";

type Tab = "overview" | "logs" | "config" | "metrics";

const TABS: Tab[] = ["overview", "logs", "config", "metrics"];

export default function NodeDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const [tab, setTab] = useState<Tab>("overview");

  const nodeQ = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !!id,
  });

  const node = nodeQ.data;

  if (nodeQ.isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (!node) return <div className="text-err">{t("node.not_found")}</div>;

  const statusTone = node.status === "enabled" ? "ok" : node.status === "paused" ? "warn" : "err";

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="font-mono text-xl font-semibold">{node.path}</h1>
        <Chip>{node.root_method}</Chip>
        <Pill tone={statusTone}>{t(`node.status.${node.status}`)}</Pill>
        <div className="ml-auto flex items-center gap-2">
          <Link to={`/nodes/${node.id}/edit`}>
            <Button sm variant="primary">
              <Pencil className="h-3.5 w-3.5" /> {t("node.actions.edit")}
            </Button>
          </Link>
        </div>
      </div>

      <div className="flex gap-1 border-b border-line">
        {TABS.map((tb) => (
          <button
            key={tb}
            type="button"
            onClick={() => setTab(tb)}
            className={cn(
              "px-3.5 py-2.5 text-[13px]",
              tab === tb
                ? "border-b-2 border-accent font-medium text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            {t(`node.tabs.${tb}`)}
          </button>
        ))}
      </div>

      {tab === "overview" && <OverviewTab node={node} onAllLogs={() => setTab("logs")} />}
      {tab === "logs" && <LogsTab node={node} />}
      {tab === "config" && <ConfigTab node={node} />}
      {tab === "metrics" && <MetricsTab node={node} />}
    </div>
  );
}
