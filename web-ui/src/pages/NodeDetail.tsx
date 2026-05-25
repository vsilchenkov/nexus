import { useParams, Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowLeft, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";

import { api, type Node } from "../api/client";
import { Topbar } from "../components/Topbar";

type LogRow = {
  id: string;
  url: string;
  method: string;
  status: number;
  duration_ms: number;
  date_request: string;
  done: boolean;
  reason?: string;
};
type LogsResp = { items: LogRow[] };

export default function NodeDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();

  const nodeQ = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !!id,
  });

  const logsQ = useQuery({
    queryKey: ["logs", id],
    queryFn: () => api.get<LogsResp>(`/api/nodes/${id}/logs`, { limit: 50 }),
    enabled: !!id,
    refetchInterval: 5_000,
  });

  // SSE live-tail (§7.4). По умолчанию выключен; включается toggle'ом.
  const [live, setLive] = useState(false);
  const [liveLogs, setLiveLogs] = useState<LogRow[]>([]);
  useEffect(() => {
    if (!live || !id) return;
    const es = new EventSource(`/api/nodes/${id}/logs/stream`);
    es.addEventListener("log", (e) => {
      try {
        const rec = JSON.parse((e as MessageEvent).data) as LogRow;
        setLiveLogs((prev) => [rec, ...prev].slice(0, 500));
      } catch {}
    });
    es.onerror = () => es.close();
    return () => es.close();
  }, [live, id]);

  const node = nodeQ.data;
  const visibleLogs = live ? liveLogs : (logsQ.data?.items ?? []);

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 space-y-4">
        <Link to="/" className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-accent">
          <ArrowLeft className="w-4 h-4" />
          back
        </Link>

        {nodeQ.isLoading && <div>{t("common.loading")}</div>}
        {node && (
          <header className="flex items-center justify-between">
            <div>
              <h1 className="text-2xl font-semibold font-mono">{node.path}</h1>
              <div className="text-sm text-fg-muted">
                {node.root_method} · {node.url_mode} · {node.auth_type}
              </div>
            </div>
            <span className="px-2 py-1 rounded bg-bg-muted text-xs">{t(`node.status.${node.status}`)}</span>
          </header>
        )}

        <section className="bg-bg-elev rounded-xl border border-bg-muted overflow-hidden">
          <header className="flex items-center justify-between px-4 py-3 border-b border-bg-muted">
            <div className="font-medium">{t("node.tabs.logs")}</div>
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={live}
                onChange={(e) => {
                  setLiveLogs([]);
                  setLive(e.target.checked);
                }}
              />
              live-tail
              {live && <RefreshCw className="w-3 h-3 animate-spin text-accent" />}
            </label>
          </header>
          <table className="w-full text-sm">
            <thead className="bg-bg-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 text-left">Time</th>
                <th className="px-3 py-2 text-left">Method</th>
                <th className="px-3 py-2 text-left">URL</th>
                <th className="px-3 py-2 text-right">Status</th>
                <th className="px-3 py-2 text-right">ms</th>
              </tr>
            </thead>
            <tbody>
              {visibleLogs.map((r) => (
                <tr key={r.id} className="border-t border-bg-muted">
                  <td className="px-3 py-2 font-mono text-xs">
                    {new Date(r.date_request).toLocaleTimeString()}
                  </td>
                  <td className="px-3 py-2">{r.method}</td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{r.url}</td>
                  <td className={`px-3 py-2 text-right ${r.done ? "text-ok" : "text-err"}`}>
                    {r.status}
                  </td>
                  <td className="px-3 py-2 text-right">{r.duration_ms}</td>
                </tr>
              ))}
              {visibleLogs.length === 0 && (
                <tr>
                  <td colSpan={5} className="px-3 py-6 text-center text-fg-muted">
                    {t("common.loading")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      </main>
    </div>
  );
}
