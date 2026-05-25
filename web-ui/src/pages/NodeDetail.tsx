import { useParams, Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowLeft, RefreshCw, Settings, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { api, type Node } from "../api/client";
import { Topbar } from "../components/Topbar";
import { ReplayDialog } from "../components/ReplayDialog";

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

type StatusFilter = "all" | "ok" | "err";
type PageSize = 50 | 100 | 200;

const LIVE_BUFFER_LIMIT = 500;
const HIGHLIGHT_DURATION_MS = 1000;
const SCROLL_TOP_THRESHOLD_PX = 8;

export default function NodeDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();

  const nodeQ = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !!id,
  });

  const [pageSize, setPageSize] = useState<PageSize>(50);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [doneFilter, setDoneFilter] = useState<"all" | "done" | "pending">("all");

  // Phase 6.8 — расширенные фильтры (period/IP/Host/full-text).
  // Применяются по кнопке «Apply», чтобы не перезагружать SSE/snapshot на
  // каждый символ. appliedFilters — те, что реально ушли на backend.
  const [showAdv, setShowAdv] = useState(false);
  const [advForm, setAdvForm] = useState({
    q: "",
    ip: "",
    host: "",
    from: "",
    to: "",
  });
  const [appliedFilters, setAppliedFilters] = useState(advForm);

  const advQueryParams = useMemo(() => {
    const p: Record<string, string | number> = { limit: pageSize };
    if (appliedFilters.q) p.q = appliedFilters.q;
    if (appliedFilters.ip) p.ip = appliedFilters.ip;
    if (appliedFilters.host) p.host = appliedFilters.host;
    if (appliedFilters.from) p.from = new Date(appliedFilters.from).toISOString();
    if (appliedFilters.to) p.to = new Date(appliedFilters.to).toISOString();
    return p;
  }, [pageSize, appliedFilters]);

  const logsQ = useQuery({
    queryKey: ["logs", id, advQueryParams],
    queryFn: () => api.get<LogsResp>(`/api/nodes/${id}/logs`, advQueryParams),
    enabled: !!id,
    refetchInterval: 5_000,
  });

  // SSE live-tail (§7.4). По умолчанию выключен; включается toggle'ом.
  // При live: фильтры/поиск временно «греются» — применяются на клиенте к
  // буферу из последних 500 записей (LIVE_BUFFER_LIMIT).
  const [live, setLive] = useState(false);
  const [liveLogs, setLiveLogs] = useState<LogRow[]>([]);
  const [highlighted, setHighlighted] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!live || !id) return;
    // В SSE кладём только q/ip/host (from/to не имеют смысла для live).
    const qs = new URLSearchParams();
    if (appliedFilters.q) qs.set("q", appliedFilters.q);
    if (appliedFilters.ip) qs.set("ip", appliedFilters.ip);
    if (appliedFilters.host) qs.set("host", appliedFilters.host);
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    const es = new EventSource(`/api/nodes/${id}/logs/stream${suffix}`);
    es.addEventListener("log", (e) => {
      try {
        const rec = JSON.parse((e as MessageEvent).data) as LogRow;
        setLiveLogs((prev) => [rec, ...prev].slice(0, LIVE_BUFFER_LIMIT));
        setHighlighted((prev) => {
          const next = new Set(prev);
          next.add(rec.id);
          return next;
        });
        // Снимаем подсветку через секунду (§7.4: «подсвечивается на 1 секунду»).
        window.setTimeout(() => {
          setHighlighted((prev) => {
            if (!prev.has(rec.id)) return prev;
            const next = new Set(prev);
            next.delete(rec.id);
            return next;
          });
        }, HIGHLIGHT_DURATION_MS);
      } catch {}
    });
    es.onerror = () => es.close();
    return () => es.close();
  }, [live, id, appliedFilters.q, appliedFilters.ip, appliedFilters.host]);

  // Авто-прокрутка к верху, если пользователь не скроллил вручную;
  // иначе показываем баннер «N новых записей» (§7.4).
  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  const autoScrollRef = useRef(true);
  const [pendingCount, setPendingCount] = useState(0);
  const lastLiveLenRef = useRef(0);

  const onScroll = () => {
    const el = tableWrapRef.current;
    if (!el) return;
    autoScrollRef.current = el.scrollTop <= SCROLL_TOP_THRESHOLD_PX;
    if (autoScrollRef.current) setPendingCount(0);
  };

  useEffect(() => {
    if (!live) {
      lastLiveLenRef.current = 0;
      setPendingCount(0);
      return;
    }
    const delta = liveLogs.length - lastLiveLenRef.current;
    if (delta <= 0) {
      lastLiveLenRef.current = liveLogs.length;
      return;
    }
    if (autoScrollRef.current && tableWrapRef.current) {
      tableWrapRef.current.scrollTop = 0;
      setPendingCount(0);
    } else {
      setPendingCount((p) => p + delta);
    }
    lastLiveLenRef.current = liveLogs.length;
  }, [live, liveLogs]);

  const visibleLogs = useMemo(() => {
    const src = live ? liveLogs : (logsQ.data?.items ?? []);
    return src.filter((r) => {
      if (statusFilter === "ok" && !(r.done && r.status >= 200 && r.status < 400)) {
        return false;
      }
      if (statusFilter === "err" && r.done && r.status >= 200 && r.status < 400) {
        return false;
      }
      if (doneFilter === "done" && !r.done) return false;
      if (doneFilter === "pending" && r.done) return false;
      return true;
    });
  }, [live, liveLogs, logsQ.data, statusFilter, doneFilter]);

  const node = nodeQ.data;
  const [replayId, setReplayId] = useState<string | null>(null);

  const scrollToTop = () => {
    if (tableWrapRef.current) {
      tableWrapRef.current.scrollTop = 0;
      autoScrollRef.current = true;
      setPendingCount(0);
    }
  };

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 space-y-4">
        <Link
          to="/"
          className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-accent"
        >
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
            <div className="flex items-center gap-2">
              <Link
                to={`/nodes/${node.id}/edit`}
                className="flex items-center gap-2 bg-bg-muted hover:bg-bg-elev px-3 py-2 rounded-md text-sm"
              >
                <Settings className="w-4 h-4" />
                {t("node.actions.edit")}
              </Link>
              <span className="px-2 py-1 rounded bg-bg-muted text-xs">
                {t(`node.status.${node.status}`)}
              </span>
            </div>
          </header>
        )}

        <section className="bg-bg-elev rounded-xl border border-bg-muted overflow-hidden relative">
          <header className="flex items-center justify-between gap-3 px-4 py-3 border-b border-bg-muted flex-wrap">
            <div className="flex items-center gap-3">
              <div className="font-medium">{t("node.tabs.logs")}</div>
              <span className="text-xs text-fg-muted">
                {t("logs.shown_count", { n: visibleLogs.length })}
              </span>
            </div>

            <div className="flex items-center gap-3 flex-wrap">
              <SegmentedControl
                value={statusFilter}
                onChange={setStatusFilter}
                options={[
                  { v: "all", label: t("logs.filter.all") },
                  { v: "ok", label: t("logs.filter.ok") },
                  { v: "err", label: t("logs.filter.err") },
                ]}
              />

              <SegmentedControl
                value={doneFilter}
                onChange={setDoneFilter}
                options={[
                  { v: "all", label: t("logs.filter.done_all") },
                  { v: "done", label: t("logs.filter.done_yes") },
                  { v: "pending", label: t("logs.filter.done_no") },
                ]}
              />

              <label className="flex items-center gap-2 text-xs text-fg-muted">
                {t("logs.page_size")}
                <select
                  value={pageSize}
                  onChange={(e) => setPageSize(Number(e.target.value) as PageSize)}
                  disabled={live}
                  className="bg-bg-muted px-2 py-1 rounded outline-none text-fg disabled:opacity-50"
                >
                  <option value={50}>50</option>
                  <option value={100}>100</option>
                  <option value={200}>200</option>
                </select>
              </label>

              <button
                onClick={() => setShowAdv((s) => !s)}
                className="text-xs text-fg-muted hover:text-fg px-2 py-1"
              >
                {showAdv ? t("logs.advanced.toggle_off") : t("logs.advanced.toggle_on")}
              </button>

              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input
                  type="checkbox"
                  checked={live}
                  onChange={(e) => {
                    setLiveLogs([]);
                    setHighlighted(new Set());
                    setPendingCount(0);
                    setLive(e.target.checked);
                  }}
                />
                {live ? t("logs.live_on") : t("logs.live_off")}
                {live && (
                  <RefreshCw className="w-3 h-3 animate-spin text-accent" />
                )}
              </label>
            </div>
          </header>

          {showAdv && (
            <div className="px-4 py-3 border-b border-bg-muted grid grid-cols-1 md:grid-cols-12 gap-3 items-end">
              <div className="md:col-span-5 space-y-1">
                <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("logs.advanced.q")}
                </label>
                <input
                  type="text"
                  value={advForm.q}
                  onChange={(e) => setAdvForm({ ...advForm, q: e.target.value })}
                  placeholder={t("logs.advanced.q_placeholder")}
                  className="w-full px-3 py-1.5 bg-bg-muted rounded-md outline-none text-sm"
                />
              </div>
              <div className="md:col-span-2 space-y-1">
                <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("logs.advanced.ip")}
                </label>
                <input
                  type="text"
                  value={advForm.ip}
                  onChange={(e) => setAdvForm({ ...advForm, ip: e.target.value })}
                  className="w-full px-3 py-1.5 bg-bg-muted rounded-md outline-none text-sm font-mono"
                />
              </div>
              <div className="md:col-span-2 space-y-1">
                <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("logs.advanced.host")}
                </label>
                <input
                  type="text"
                  value={advForm.host}
                  onChange={(e) => setAdvForm({ ...advForm, host: e.target.value })}
                  className="w-full px-3 py-1.5 bg-bg-muted rounded-md outline-none text-sm font-mono"
                />
              </div>
              <div className="md:col-span-3 space-y-1">
                <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("logs.advanced.from")}
                </label>
                <input
                  type="datetime-local"
                  value={advForm.from}
                  onChange={(e) => setAdvForm({ ...advForm, from: e.target.value })}
                  className="w-full px-3 py-1.5 bg-bg-muted rounded-md outline-none text-sm"
                />
              </div>
              <div className="md:col-span-3 space-y-1">
                <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("logs.advanced.to")}
                </label>
                <input
                  type="datetime-local"
                  value={advForm.to}
                  onChange={(e) => setAdvForm({ ...advForm, to: e.target.value })}
                  className="w-full px-3 py-1.5 bg-bg-muted rounded-md outline-none text-sm"
                />
              </div>
              <div className="md:col-span-6 flex items-center gap-2 justify-end">
                <button
                  onClick={() => {
                    const reset = { q: "", ip: "", host: "", from: "", to: "" };
                    setAdvForm(reset);
                    setAppliedFilters(reset);
                  }}
                  className="px-3 py-1.5 text-sm bg-bg-muted hover:bg-bg-muted/70 rounded-md"
                >
                  {t("logs.advanced.reset")}
                </button>
                <button
                  onClick={() => setAppliedFilters(advForm)}
                  className="px-3 py-1.5 text-sm bg-accent hover:bg-accent-hover text-white rounded-md"
                >
                  {t("logs.advanced.apply")}
                </button>
              </div>
            </div>
          )}

          <div
            ref={tableWrapRef}
            onScroll={onScroll}
            className="relative max-h-[60vh] overflow-y-auto"
          >
            <table className="w-full text-sm">
              <thead className="bg-bg-muted text-fg-muted sticky top-0 z-10">
                <tr>
                  <th className="px-3 py-2 text-left">{t("logs.col.time")}</th>
                  <th className="px-3 py-2 text-left">{t("logs.col.method")}</th>
                  <th className="px-3 py-2 text-left">{t("logs.col.url")}</th>
                  <th className="px-3 py-2 text-right">{t("logs.col.status")}</th>
                  <th className="px-3 py-2 text-right">{t("logs.col.ms")}</th>
                  <th className="px-3 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {visibleLogs.map((r) => {
                  const isHl = highlighted.has(r.id);
                  const isErr = !r.done || r.status >= 400 || r.status === 0;
                  return (
                    <tr
                      key={r.id}
                      className={`border-t border-bg-muted transition-colors ${
                        isHl ? "bg-accent/15" : isErr ? "bg-err/5" : ""
                      }`}
                    >
                      <td className="px-3 py-2 font-mono text-xs">
                        {new Date(r.date_request).toLocaleTimeString()}
                      </td>
                      <td className="px-3 py-2">{r.method}</td>
                      <td className="px-3 py-2 font-mono text-xs text-fg-muted truncate max-w-[28rem]">
                        {r.url}
                      </td>
                      <td
                        className={`px-3 py-2 text-right ${
                          isErr ? "text-err" : "text-ok"
                        }`}
                      >
                        {r.status}
                      </td>
                      <td className="px-3 py-2 text-right">{r.duration_ms}</td>
                      <td className="px-3 py-2 text-right">
                        <button
                          title={t("node.actions.replay")}
                          onClick={() => setReplayId(r.id)}
                          className="text-fg-muted hover:text-accent"
                        >
                          <RotateCcw className="w-4 h-4" />
                        </button>
                      </td>
                    </tr>
                  );
                })}
                {visibleLogs.length === 0 && (
                  <tr>
                    <td
                      colSpan={6}
                      className="px-3 py-6 text-center text-fg-muted"
                    >
                      {logsQ.isLoading
                        ? t("common.loading")
                        : t("logs.empty")}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>

          {live && pendingCount > 0 && (
            <button
              onClick={scrollToTop}
              className="absolute left-1/2 -translate-x-1/2 bottom-6 z-20 bg-accent hover:bg-accent-hover text-white px-3 py-1.5 rounded-full text-xs shadow-lg"
            >
              {t("logs.new_records", { n: pendingCount })} ↑
            </button>
          )}
        </section>

        {replayId && node && (
          <ReplayDialog
            logId={replayId}
            nodeId={node.id}
            onClose={() => setReplayId(null)}
          />
        )}
      </main>
    </div>
  );
}

type SegmentedControlProps<T extends string> = {
  value: T;
  onChange: (v: T) => void;
  options: { v: T; label: string }[];
};

function SegmentedControl<T extends string>({
  value,
  onChange,
  options,
}: SegmentedControlProps<T>) {
  return (
    <div className="inline-flex bg-bg-muted rounded-md p-0.5 text-xs">
      {options.map((o) => (
        <button
          key={o.v}
          onClick={() => onChange(o.v)}
          className={`px-2.5 py-1 rounded ${
            value === o.v
              ? "bg-bg-elev text-fg shadow-sm"
              : "text-fg-muted hover:text-fg"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
