import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus } from "lucide-react";

import { api, type Node } from "../api/client";

type ListResp = { items: Node[] };

export default function Overview() {
  const { t } = useTranslation();
  const [search, setSearch] = useState("");
  const [moveTarget, setMoveTarget] = useState<Node | null>(null);
  const { data, isLoading, error } = useQuery({
    queryKey: ["nodes", search],
    queryFn: () => api.get<ListResp>("/api/nodes", { search }),
  });

  return (
    <div className="mx-auto max-w-6xl space-y-4">
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
                  <th className="px-4 py-3"></th>
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
                    <td className="px-4 py-3 text-right">
                      <button
                        onClick={() => setMoveTarget(n)}
                        className="text-xs text-fg-muted hover:text-accent"
                        title={t("overview.move.action")}
                      >
                        {t("overview.move.action")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      {moveTarget && (
        <MoveNodeDialog node={moveTarget} onClose={() => setMoveTarget(null)} />
      )}
    </div>
  );
}

type Team = { id: string; slug: string; name: string };

function MoveNodeDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [error, setError] = useState<string | null>(null);

  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => api.get<{ items: Team[] }>("/api/teams"),
  });

  const move = useMutation({
    mutationFn: () =>
      api.post(`/api/nodes/${node.id}/move`, { target_team_slug: slug }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      onClose();
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4"
      onClick={onClose}
    >
      <div
        className="bg-bg-elev rounded-xl border border-bg-muted p-5 shadow-xl w-[420px] max-w-full space-y-4"
        onClick={(e) => e.stopPropagation()}
      >
        <header>
          <h3 className="text-lg font-semibold">{t("overview.move.title")}</h3>
          <p className="text-xs text-fg-muted mt-1 font-mono">{node.path}</p>
        </header>

        {error && (
          <div className="bg-err/10 border border-err/40 text-err px-3 py-2 rounded text-sm">
            {error}
          </div>
        )}

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("overview.move.target_team")}
          </label>
          <select
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          >
            <option value="">{t("overview.move.pick_team")}</option>
            {(teams.data?.items ?? []).map((tm) => (
              <option key={tm.id} value={tm.slug}>
                {tm.name} ({tm.slug})
              </option>
            ))}
          </select>
          <p className="text-xs text-fg-muted">{t("overview.move.hint")}</p>
        </div>

        <footer className="flex items-center justify-end gap-2 pt-2 border-t border-bg-muted">
          <button onClick={onClose} className="px-3 py-2 text-sm text-fg-muted hover:text-fg">
            {t("common.cancel")}
          </button>
          <button
            onClick={() => move.mutate()}
            disabled={!slug || move.isPending}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
          >
            {t("overview.move.submit")}
          </button>
        </footer>
      </div>
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
