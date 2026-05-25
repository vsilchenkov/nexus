import { useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus } from "lucide-react";

import { api, type Node } from "../api/client";
import { Topbar } from "../components/Topbar";

type ListResp = { items: Node[] };

export default function Overview() {
  const { t } = useTranslation();
  const [search, setSearch] = useState("");
  const { data, isLoading, error } = useQuery({
    queryKey: ["nodes", search],
    queryFn: () => api.get<ListResp>("/api/nodes", { search }),
  });

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 space-y-4">
        <header className="flex items-center justify-between">
          <h1 className="text-2xl font-semibold">{t("overview.title")}</h1>
          <Link
            to="/nodes/new"
            className="flex items-center gap-2 bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm"
          >
            <Plus className="w-4 h-4" />
            {t("overview.new_node")}
          </Link>
        </header>

        <div className="relative w-full max-w-md">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-fg-muted" />
          <input
            className="w-full pl-9 pr-3 py-2 bg-bg-elev rounded-md border border-bg-muted focus:border-accent outline-none"
            placeholder={t("overview.search_placeholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>

        {isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
        {error && <div className="text-err">{(error as Error).message}</div>}

        {data && data.items.length === 0 && (
          <div className="text-fg-muted">{t("overview.empty")}</div>
        )}

        {data && data.items.length > 0 && (
          <div className="bg-bg-elev rounded-xl border border-bg-muted overflow-hidden">
            <table className="w-full text-left text-sm">
              <thead className="bg-bg-muted text-fg-muted">
                <tr>
                  <th className="px-4 py-3">{t("overview.table.path")}</th>
                  <th className="px-4 py-3">{t("overview.table.method")}</th>
                  <th className="px-4 py-3">{t("overview.table.target")}</th>
                  <th className="px-4 py-3">{t("overview.table.auth")}</th>
                  <th className="px-4 py-3">{t("overview.table.status")}</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((n) => (
                  <tr
                    key={n.id}
                    className="border-t border-bg-muted hover:bg-bg-muted/60 transition-colors"
                  >
                    <td className="px-4 py-3 font-mono">
                      <Link to={`/nodes/${n.id}`} className="hover:text-accent">
                        {n.path}
                      </Link>
                    </td>
                    <td className="px-4 py-3">{n.root_method}</td>
                    <td className="px-4 py-3 font-mono text-xs text-fg-muted">
                      {n.target_url || "—"}
                    </td>
                    <td className="px-4 py-3">{n.auth_type}</td>
                    <td className="px-4 py-3">
                      <StatusBadge status={n.status} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </main>
    </div>
  );
}

function StatusBadge({ status }: { status: Node["status"] }) {
  const { t } = useTranslation();
  const cls =
    status === "enabled"
      ? "bg-ok/10 text-ok"
      : status === "paused"
        ? "bg-warn/10 text-warn"
        : "bg-err/10 text-err";
  return (
    <span className={`px-2 py-0.5 rounded text-xs ${cls}`}>
      {t(`node.status.${status}`)}
    </span>
  );
}
