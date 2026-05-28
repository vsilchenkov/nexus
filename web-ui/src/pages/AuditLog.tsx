import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Download } from "lucide-react";

import { api } from "../api/client";
import { AuditDetailsCell } from "../components/AuditDetailsCell";
import { Button, Card, Chip, Select } from "../components/ui";

type Entry = {
  id: string;
  user_login: string;
  action: string;
  target_type: string;
  target_id: string;
  details: Record<string, unknown>;
  ip_address: string;
  created_at: string;
};
type Resp = { items: Entry[] };

// actionTone — окраска чипа действия по префиксу/семантике.
function actionTone(action: string): "default" | "info" | "success" | "danger" | "warning" {
  if (action.endsWith(".create")) return "success";
  if (action.endsWith(".delete") || action.includes("failed") || action.includes("drop"))
    return "danger";
  if (action.endsWith(".update")) return "info";
  return "default";
}

export default function AuditLog() {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");

  const q = useQuery({
    queryKey: ["audit", filter],
    queryFn: () =>
      api.get<Resp>("/api/audit", filter ? { action: filter, limit: 200 } : { limit: 200 }),
  });

  const csvHref = filter
    ? `/api/audit/export.csv?action=${encodeURIComponent(filter)}`
    : "/api/audit/export.csv";

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-lg font-semibold">{t("audit.title")}</h1>
        <Select className="w-44" value={filter} onChange={(e) => setFilter(e.target.value)}>
          <option value="">{t("audit.all_actions")}</option>
          <option value="node.create">node.create</option>
          <option value="node.update">node.update</option>
          <option value="node.delete">node.delete</option>
          <option value="node.replay">node.replay</option>
          <option value="node.dry_run">node.dry_run</option>
          <option value="user.login.success">user.login.success</option>
          <option value="user.login.failed">user.login.failed</option>
          <option value="api_token.create">api_token.create</option>
          <option value="api_token.revoke">api_token.revoke</option>
        </Select>
        <a href={csvHref} download>
          <Button sm>
            <Download className="h-3.5 w-3.5" /> {t("audit.export_csv")}
          </Button>
        </a>
      </div>

      {q.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {q.error && <div className="text-err">{(q.error as Error).message}</div>}

      {q.data && (
        <Card className="overflow-hidden p-0">
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
                <th className="px-3 py-2 font-medium">{t("audit.columns.when")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.user")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.action")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.target")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.ip")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.details")}</th>
              </tr>
            </thead>
            <tbody>
              {q.data.items.map((e) => (
                <tr key={e.id} className="border-b border-line last:border-0 hover:bg-bg-muted">
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">
                    {new Date(e.created_at).toLocaleString()}
                  </td>
                  <td className="px-3 py-2">{e.user_login}</td>
                  <td className="px-3 py-2">
                    <Chip tone={actionTone(e.action)}>{e.action}</Chip>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                    {e.target_type}/{e.target_id.slice(0, 8)}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{e.ip_address}</td>
                  <td className="max-w-lg px-3 py-2">
                    <AuditDetailsCell action={e.action} details={e.details} />
                  </td>
                </tr>
              ))}
              {q.data.items.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center text-fg-muted">
                    {t("audit.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  );
}
