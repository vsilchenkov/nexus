import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { Topbar } from "../components/Topbar";
import { AuditDetailsCell } from "../components/AuditDetailsCell";

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

export default function AuditLog() {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");

  const q = useQuery({
    queryKey: ["audit", filter],
    queryFn: () =>
      api.get<Resp>("/api/audit", filter ? { action: filter, limit: 200 } : { limit: 200 }),
  });

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 space-y-4">
        <header className="flex items-center justify-between">
          <h1 className="text-2xl font-semibold">{t("audit.title")}</h1>
          <div className="flex items-center gap-2">
            <select
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="px-3 py-2 bg-bg-muted rounded-md outline-none"
            >
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
            </select>
            <a
              href={
                filter
                  ? `/api/audit/export.csv?action=${encodeURIComponent(filter)}`
                  : "/api/audit/export.csv"
              }
              className="px-3 py-2 bg-bg-muted hover:bg-bg-muted/70 rounded-md text-sm transition-colors"
              download
            >
              {t("audit.export_csv")}
            </a>
          </div>
        </header>

        {q.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
        {q.error && <div className="text-err">{(q.error as Error).message}</div>}

        {q.data && (
          <div className="bg-bg-elev rounded-xl border border-bg-muted overflow-hidden">
            <table className="w-full text-sm">
              <thead className="bg-bg-muted text-fg-muted">
                <tr>
                  <th className="px-3 py-2 text-left">{t("audit.columns.when")}</th>
                  <th className="px-3 py-2 text-left">{t("audit.columns.user")}</th>
                  <th className="px-3 py-2 text-left">{t("audit.columns.action")}</th>
                  <th className="px-3 py-2 text-left">{t("audit.columns.target")}</th>
                  <th className="px-3 py-2 text-left">{t("audit.columns.ip")}</th>
                  <th className="px-3 py-2 text-left">{t("audit.columns.details")}</th>
                </tr>
              </thead>
              <tbody>
                {q.data.items.map((e) => (
                  <tr key={e.id} className="border-t border-bg-muted">
                    <td className="px-3 py-2 font-mono text-xs whitespace-nowrap">
                      {new Date(e.created_at).toLocaleString()}
                    </td>
                    <td className="px-3 py-2">{e.user_login}</td>
                    <td className="px-3 py-2 font-mono text-xs">{e.action}</td>
                    <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                      {e.target_type}/{e.target_id.slice(0, 8)}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-fg-muted">{e.ip_address}</td>
                    <td className="px-3 py-2 max-w-lg">
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
          </div>
        )}
      </main>
    </div>
  );
}
