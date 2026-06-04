import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { RefreshCw, Settings, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { api, type Node } from "../../api/client";
import { ReplayDialog } from "../ReplayDialog";
import { type LogRow, type LogsResp } from "./types";

type StatusFilter = "all" | "ok" | "err";
type PageSize = 50 | 100 | 200;

// LogsInitialFilter — стартовый фильтр логов, прокинутый кликом по графику (§33.4):
// from/to в формате <input type="datetime-local"> (локальная зона).
export type LogsInitialFilter = { from?: string; to?: string; status?: StatusFilter };

const LIVE_BUFFER_LIMIT = 500;
const HIGHLIGHT_DURATION_MS = 1000;
const SCROLL_TOP_THRESHOLD_PX = 8;

// LogsTab — вкладка «Логи» (§7.4): snapshot + SSE live-tail с буфером,
// клиентскими фильтрами, расширенным поиском и replay-меню строки.
export function LogsTab({ node, initialFilter }: { node: Node; initialFilter?: LogsInitialFilter }) {
  const { t } = useTranslation();
  const id = node.id;
  const hasLogsTable = !!node.clickhouse_table;

  const [pageSize, setPageSize] = useState<PageSize>(50);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>(initialFilter?.status ?? "all");
  const [doneFilter, setDoneFilter] = useState<"all" | "done" | "pending">("all");

  // Стартовый временной фильтр из клика по графику (§33.4). LogsTab монтируется
  // заново при переключении на вкладку, поэтому инициализация через useState ок.
  const initForm = { q: "", ip: "", host: "", from: initialFilter?.from ?? "", to: initialFilter?.to ?? "" };
  const [showAdv, setShowAdv] = useState(!!(initialFilter?.from || initialFilter?.to));
  const [advForm, setAdvForm] = useState(initForm);
  const [appliedFilters, setAppliedFilters] = useState(initForm);

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
    enabled: !!id && hasLogsTable,
    refetchInterval: 5_000,
  });

  const [live, setLive] = useState(false);
  const [liveLogs, setLiveLogs] = useState<LogRow[]>([]);
  const [highlighted, setHighlighted] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!live || !id || !hasLogsTable) return;
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
        setHighlighted((prev) => new Set(prev).add(rec.id));
        window.setTimeout(() => {
          setHighlighted((prev) => {
            if (!prev.has(rec.id)) return prev;
            const next = new Set(prev);
            next.delete(rec.id);
            return next;
          });
        }, HIGHLIGHT_DURATION_MS);
      } catch {
        // Невалидный JSON в SSE-событии — пропускаем запись, поток продолжаем.
      }
    });
    es.onerror = () => es.close();
    return () => es.close();
  }, [live, id, hasLogsTable, appliedFilters.q, appliedFilters.ip, appliedFilters.host]);

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
      if (statusFilter === "ok" && !(r.done && r.status >= 200 && r.status < 400)) return false;
      if (statusFilter === "err" && r.done && r.status >= 200 && r.status < 400) return false;
      if (doneFilter === "done" && !r.done) return false;
      if (doneFilter === "pending" && r.done) return false;
      return true;
    });
  }, [live, liveLogs, logsQ.data, statusFilter, doneFilter]);

  const [replayId, setReplayId] = useState<string | null>(null);

  const scrollToTop = () => {
    if (tableWrapRef.current) {
      tableWrapRef.current.scrollTop = 0;
      autoScrollRef.current = true;
      setPendingCount(0);
    }
  };

  return (
    <div className="relative overflow-hidden rounded-lg border border-line bg-bg">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
        <div className="flex items-center gap-3">
          <div className="font-medium">{t("node.tabs.logs")}</div>
          <span className="text-xs text-fg-muted">
            {t("logs.shown_count", { n: visibleLogs.length })}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-3">
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
              className="rounded bg-bg-muted px-2 py-1 text-fg outline-none disabled:opacity-50"
            >
              <option value={50}>50</option>
              <option value={100}>100</option>
              <option value={200}>200</option>
            </select>
          </label>
          <button
            onClick={() => setShowAdv((s) => !s)}
            className="px-2 py-1 text-xs text-fg-muted hover:text-fg"
          >
            {showAdv ? t("logs.advanced.toggle_off") : t("logs.advanced.toggle_on")}
          </button>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={live}
              disabled={!hasLogsTable}
              onChange={(e) => {
                setLiveLogs([]);
                setHighlighted(new Set());
                setPendingCount(0);
                setLive(e.target.checked);
              }}
            />
            {live ? t("logs.live_on") : t("logs.live_off")}
            {live && <RefreshCw className="h-3 w-3 animate-spin text-accent" />}
          </label>
        </div>
      </header>

      {showAdv && (
        <div className="grid grid-cols-1 items-end gap-3 border-b border-line px-4 py-3 md:grid-cols-12">
          <div className="space-y-1 md:col-span-5">
            <label className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.advanced.q")}
            </label>
            <input
              type="text"
              value={advForm.q}
              onChange={(e) => setAdvForm({ ...advForm, q: e.target.value })}
              placeholder={t("logs.advanced.q_placeholder")}
              className="w-full rounded-md bg-bg-muted px-3 py-1.5 text-sm outline-none"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.advanced.ip")}
            </label>
            <input
              type="text"
              value={advForm.ip}
              onChange={(e) => setAdvForm({ ...advForm, ip: e.target.value })}
              className="w-full rounded-md bg-bg-muted px-3 py-1.5 font-mono text-sm outline-none"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.advanced.host")}
            </label>
            <input
              type="text"
              value={advForm.host}
              onChange={(e) => setAdvForm({ ...advForm, host: e.target.value })}
              className="w-full rounded-md bg-bg-muted px-3 py-1.5 font-mono text-sm outline-none"
            />
          </div>
          <div className="space-y-1 md:col-span-3">
            <label className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.advanced.from")}
            </label>
            <input
              type="datetime-local"
              value={advForm.from}
              onChange={(e) => setAdvForm({ ...advForm, from: e.target.value })}
              className="w-full rounded-md bg-bg-muted px-3 py-1.5 text-sm outline-none"
            />
          </div>
          <div className="space-y-1 md:col-span-3">
            <label className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.advanced.to")}
            </label>
            <input
              type="datetime-local"
              value={advForm.to}
              onChange={(e) => setAdvForm({ ...advForm, to: e.target.value })}
              className="w-full rounded-md bg-bg-muted px-3 py-1.5 text-sm outline-none"
            />
          </div>
          <div className="flex items-center justify-end gap-2 md:col-span-6">
            <button
              onClick={() => {
                const reset = { q: "", ip: "", host: "", from: "", to: "" };
                setAdvForm(reset);
                setAppliedFilters(reset);
              }}
              className="rounded-md bg-bg-muted px-3 py-1.5 text-sm hover:bg-bg-3"
            >
              {t("logs.advanced.reset")}
            </button>
            <button
              onClick={() => setAppliedFilters(advForm)}
              className="rounded-md bg-accent px-3 py-1.5 text-sm text-white hover:bg-accent-hover"
            >
              {t("logs.advanced.apply")}
            </button>
          </div>
        </div>
      )}

      {!hasLogsTable ? (
        <div className="space-y-3 px-4 py-10 text-center text-fg-muted">
          <p className="mx-auto max-w-xl">{t("logs.not_configured")}</p>
          <Link
            to={`/nodes/${node.id}/edit`}
            className="inline-flex items-center gap-2 rounded-md bg-bg-muted px-3 py-2 text-sm hover:bg-bg-3"
          >
            <Settings className="h-4 w-4" />
            {t("node.actions.edit")}
          </Link>
        </div>
      ) : (
        <div ref={tableWrapRef} onScroll={onScroll} className="relative max-h-[60vh] overflow-y-auto">
          <table className="w-full text-sm">
            <thead className="sticky top-0 z-10 bg-bg-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 text-left">{t("logs.col.time")}</th>
                <th className="px-3 py-2 text-left">{t("logs.col.method")}</th>
                <th className="px-3 py-2 text-left">{t("logs.col.url")}</th>
                <th className="px-3 py-2 text-right">{t("logs.col.status")}</th>
                <th className="px-3 py-2 text-right">{t("logs.col.ms")}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {visibleLogs.map((r) => {
                const isHl = highlighted.has(r.id);
                const isErr = !r.done || r.status >= 400 || r.status === 0;
                return (
                  <tr
                    key={r.id}
                    className={`border-t border-line transition-colors ${
                      isHl ? "bg-accent/15" : isErr ? "bg-err/5" : ""
                    }`}
                  >
                    <td className="px-3 py-2 font-mono text-xs">
                      {new Date(r.date_request).toLocaleTimeString()}
                    </td>
                    <td className="px-3 py-2">{r.method}</td>
                    <td className="max-w-[28rem] truncate px-3 py-2 font-mono text-xs text-fg-muted">
                      {r.url}
                    </td>
                    <td className={`px-3 py-2 text-right ${isErr ? "text-err" : "text-ok"}`}>
                      {r.status}
                    </td>
                    <td className="px-3 py-2 text-right">{r.duration_ms}</td>
                    <td className="px-3 py-2 text-right">
                      <button
                        title={t("node.actions.replay")}
                        onClick={() => setReplayId(r.id)}
                        className="text-fg-muted hover:text-accent"
                      >
                        <RotateCcw className="h-4 w-4" />
                      </button>
                    </td>
                  </tr>
                );
              })}
              {visibleLogs.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center text-fg-muted">
                    {logsQ.isLoading ? t("common.loading") : t("logs.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {live && pendingCount > 0 && (
        <button
          onClick={scrollToTop}
          className="absolute bottom-6 left-1/2 z-20 -translate-x-1/2 rounded-full bg-accent px-3 py-1.5 text-xs text-white shadow-lg hover:bg-accent-hover"
        >
          {t("logs.new_records", { n: pendingCount })} ↑
        </button>
      )}

      {replayId && (
        <ReplayDialog logId={replayId} nodeId={node.id} onClose={() => setReplayId(null)} />
      )}
    </div>
  );
}

type SegmentedControlProps<T extends string> = {
  value: T;
  onChange: (v: T) => void;
  options: { v: T; label: string }[];
};

function SegmentedControl<T extends string>({ value, onChange, options }: SegmentedControlProps<T>) {
  return (
    <div className="inline-flex rounded-md bg-bg-muted p-0.5 text-xs">
      {options.map((o) => (
        <button
          key={o.v}
          onClick={() => onChange(o.v)}
          className={`rounded px-2.5 py-1 ${
            value === o.v ? "bg-bg-3 text-fg shadow-sm" : "text-fg-muted hover:text-fg"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
