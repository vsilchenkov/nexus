import { Link } from "react-router-dom";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { RefreshCw, Settings, RotateCcw, ChevronRight, ChevronDown, Download } from "lucide-react";
import { Fragment, useEffect, useMemo, useRef, useState } from "react";

import { api, type Node } from "../../api/client";
import { fmtLogTs } from "../../lib/format";
import { FETCH_CHUNK, LARGE_WARN_RUNES, formatRunes, prettyMaybe } from "../../lib/logBody";
import { CopyButton } from "../ui/CopyButton";
import { ReplayDialog } from "../ReplayDialog";
import { type LogRow, type LogsResp, type LogDetail, type LogBodyChunk } from "./types";

type StatusFilter = "all" | "ok" | "err";
type PageSize = 50 | 100 | 200;
// LogsCursor — keyset-курсор бесконечного скролла (§44/45-fix): время самой
// старой загруженной строки (мс) + её id как тай-брейкер для «плотных» секунд.
type LogsCursor = { to: number; beforeId: string };

// LogsInitialFilter — стартовый фильтр логов, прокинутый кликом по графику (§33.4):
// from/to в формате <input type="datetime-local"> (локальная зона).
export type LogsInitialFilter = {
  from?: string;
  to?: string;
  status?: StatusFilter;
  done?: "yes" | "no"; // §35: дип-линк из вкладки «Очередь» (только неудачные)
};

const LIVE_BUFFER_LIMIT = 500;
const HIGHLIGHT_DURATION_MS = 1000;
const SCROLL_TOP_THRESHOLD_PX = 8;
// Близость к низу скролл-контейнера, при которой подгружаем следующую страницу.
const SCROLL_BOTTOM_THRESHOLD_PX = 200;
// Сколько ошибок SSE подряд терпим, прежде чем признать поток мёртвым.
// Между ними браузер сам переподключается (нативный retry EventSource).
const LIVE_MAX_CONSECUTIVE_ERRORS = 5;

// Потолок строк, накапливаемых бесконечным скроллом (§44: фикс зависания на
// узлах с сотнями тысяч логов). Без виртуализации неограниченный append раздувал
// DOM и вешал прокрутку. Достигнут потолок — подгрузка вниз останавливается,
// показываем подсказку сузить период/фильтр (точечный поиск — через фильтры).
const MAX_INFINITE_ROWS = 1000;

// LogsTab — вкладка «Логи» (§7.4): snapshot + SSE live-tail с буфером,
// клиентскими фильтрами, расширенным поиском и replay-меню строки.
export function LogsTab({ node, initialFilter }: { node: Node; initialFilter?: LogsInitialFilter }) {
  const { t } = useTranslation();
  const id = node.id;
  const hasLogsTable = !!node.clickhouse_table;

  const [pageSize, setPageSize] = useState<PageSize>(50);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>(initialFilter?.status ?? "all");
  const [doneFilter, setDoneFilter] = useState<"all" | "done" | "pending">(
    initialFilter?.done === "no" ? "pending" : initialFilter?.done === "yes" ? "done" : "all",
  );

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

  const [live, setLive] = useState(false);
  // «У верха» скролл-контейнера. Управляет авто-рефетчем (см. logsQ ниже):
  // вверху — обновляем свежие записи каждые 5с; прокрутил вниз (листает историю)
  // — замораживаем; вернулся вверх — снова размораживаем.
  const [atTop, setAtTop] = useState(true);

  // Бесконечный скролл (§7.4): первая страница — последние pageSize записей
  // (ORDER BY date_request DESC), скролл вниз подгружает следующие pageSize
  // более старых через курсор `to` = date_request самой старой загруженной.
  const logsQ = useInfiniteQuery({
    queryKey: ["logs", id, advQueryParams],
    enabled: !!id && hasLogsTable && !live, // в Live snapshot не нужен — читаем SSE-буфер
    initialPageParam: null as LogsCursor | null,
    queryFn: ({ pageParam }) => {
      const params: Record<string, string | number> = { ...advQueryParams };
      // §44/45-fix: keyset-курсор (date_request, id) — строгая граница
      // (date_request < to) ИЛИ (= to И id < before_id). Перешагивает «плотные»
      // секунды (сотни записей с одинаковым date_request), где простой `to <=`
      // зацикливался → скролл не двигался вниз. Дедуп по id ниже — страховка.
      if (pageParam) {
        params.to = pageParam.to;
        params.before_id = pageParam.beforeId;
      }
      return api.get<LogsResp>(`/api/nodes/${id}/logs`, params);
    },
    getNextPageParam: (lastPage) => {
      const items = lastPage.items ?? [];
      if (items.length < Number(advQueryParams.limit)) return undefined; // конец истории
      const oldest = items[items.length - 1]; // DESC → последний самый старый
      return oldest ? { to: new Date(oldest.date_request).getTime(), beforeId: oldest.id } : undefined;
    },
    // Авто-рефетч только пока пользователь у верха (см. atTop). При Live выключен.
    refetchInterval: !live && atTop ? 5_000 : false,
  });

  const [liveLogs, setLiveLogs] = useState<LogRow[]>([]);
  const [highlighted, setHighlighted] = useState<Set<string>>(new Set());
  // Поток умер окончательно (LIVE_MAX_CONSECUTIVE_ERRORS ошибок подряд) —
  // live выключен, показываем предупреждение вместо вечного спиннера.
  const [liveLost, setLiveLost] = useState(false);
  // ClickHouse временно недоступен в live-режиме (SSE-событие logs_unavailable).
  const [liveUnavailable, setLiveUnavailable] = useState(false);
  // Snapshot-запрос вернул logs_available=false (CH недоступен): показываем
  // мягкий индикатор «логи временно недоступны», а не пустой список/спиннер.
  const logsUnavailable = logsQ.data?.pages?.[0]?.logs_available === false;

  // Плоский список из всех подгруженных страниц с дедупом по id: курсорная
  // граница `to` включительна, поэтому самая старая запись страницы приходит
  // повторно первой записью следующей. Порядок DESC сохраняется.
  const infiniteItems = useMemo(() => {
    const pages = logsQ.data?.pages ?? [];
    const seen = new Set<string>();
    const out: LogRow[] = [];
    for (const p of pages) {
      for (const r of p.items ?? []) {
        if (seen.has(r.id)) continue;
        seen.add(r.id);
        out.push(r);
      }
    }
    return out;
  }, [logsQ.data]);
  // Таймеры снятия подсветки: чистим при unmount/перезапуске потока, иначе
  // setState стреляет по размонтированному компоненту.
  const highlightTimersRef = useRef<Set<number>>(new Set());

  useEffect(() => {
    if (!live || !id || !hasLogsTable) return;
    const qs = new URLSearchParams();
    if (appliedFilters.q) qs.set("q", appliedFilters.q);
    if (appliedFilters.ip) qs.set("ip", appliedFilters.ip);
    if (appliedFilters.host) qs.set("host", appliedFilters.host);
    const suffix = qs.toString() ? `?${qs.toString()}` : "";
    const es = new EventSource(`/api/nodes/${id}/logs/stream${suffix}`);
    const timers = highlightTimersRef.current;
    let consecutiveErrors = 0;
    es.addEventListener("log", (e) => {
      consecutiveErrors = 0;
      try {
        const rec = JSON.parse((e as MessageEvent).data) as LogRow;
        setLiveLogs((prev) => [rec, ...prev].slice(0, LIVE_BUFFER_LIMIT));
        setHighlighted((prev) => new Set(prev).add(rec.id));
        const tid = window.setTimeout(() => {
          timers.delete(tid);
          setHighlighted((prev) => {
            if (!prev.has(rec.id)) return prev;
            const next = new Set(prev);
            next.delete(rec.id);
            return next;
          });
        }, HIGHLIGHT_DURATION_MS);
        timers.add(tid);
      } catch {
        // Невалидный JSON в SSE-событии — пропускаем запись, поток продолжаем.
      }
    });
    // Бэкенд прислал, что CH недоступен — закрываем поток и показываем
    // индикатор недоступности (не вечный спиннер, не «поток потерян»).
    es.addEventListener("logs_unavailable", () => {
      es.close();
      setLive(false);
      setLiveUnavailable(true);
    });
    es.onopen = () => {
      consecutiveErrors = 0;
    };
    es.onerror = () => {
      // Транзиентные обрывы браузер переподключает сам (readyState=CONNECTING).
      // Фатально: сервер закрыл поток (CLOSED — например, 401/404) либо
      // несколько ошибок подряд — выключаем live и показываем предупреждение,
      // а не молча оставляем «Live ▶» со спиннером без данных.
      consecutiveErrors += 1;
      if (es.readyState === EventSource.CLOSED || consecutiveErrors >= LIVE_MAX_CONSECUTIVE_ERRORS) {
        es.close();
        setLive(false);
        setLiveLost(true);
      }
    };
    return () => {
      es.close();
      for (const tid of timers) window.clearTimeout(tid);
      timers.clear();
    };
  }, [live, id, hasLogsTable, appliedFilters.q, appliedFilters.ip, appliedFilters.host]);

  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  const autoScrollRef = useRef(true);
  const [pendingCount, setPendingCount] = useState(0);
  const lastLiveLenRef = useRef(0);

  const atTopRef = useRef(true);

  const onScroll = () => {
    const el = tableWrapRef.current;
    if (!el) return;
    const nowAtTop = el.scrollTop <= SCROLL_TOP_THRESHOLD_PX;
    autoScrollRef.current = nowAtTop;
    if (nowAtTop) setPendingCount(0);
    // atTop управляет авто-рефетчем (logsQ): ре-рендерим только на пересечении
    // порога, чтобы не дёргать состояние на каждом событии скролла.
    if (nowAtTop !== atTopRef.current) {
      atTopRef.current = nowAtTop;
      setAtTop(nowAtTop);
    }
    // Бесконечный скролл вниз — только не в Live (Live добавляет записи сверху).
    if (!live) {
      const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_BOTTOM_THRESHOLD_PX;
      if (
        nearBottom &&
        logsQ.hasNextPage &&
        !logsQ.isFetchingNextPage &&
        infiniteItems.length < MAX_INFINITE_ROWS
      ) {
        logsQ.fetchNextPage();
      }
    }
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
    const src = live ? liveLogs : infiniteItems;
    return src.filter((r) => {
      if (statusFilter === "ok" && !(r.done && r.status >= 200 && r.status < 400)) return false;
      if (statusFilter === "err" && r.done && r.status >= 200 && r.status < 400) return false;
      if (doneFilter === "done" && !r.done) return false;
      if (doneFilter === "pending" && r.done) return false;
      return true;
    });
  }, [live, liveLogs, infiniteItems, statusFilter, doneFilter]);

  // Догрузка при недоборе высоты: если контента меньше высоты контейнера (нет
  // скроллбара) или клиентский фильтр выел строки — доскроллить нельзя, тянем
  // следующую страницу сами, пока есть что и пока влезает.
  useEffect(() => {
    if (live) return;
    const el = tableWrapRef.current;
    if (!el) return;
    if (
      el.scrollHeight <= el.clientHeight &&
      logsQ.hasNextPage &&
      !logsQ.isFetchingNextPage &&
      infiniteItems.length < MAX_INFINITE_ROWS
    ) {
      logsQ.fetchNextPage();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, visibleLogs.length, logsQ.hasNextPage, logsQ.isFetchingNextPage]);

  const [replayId, setReplayId] = useState<string | null>(null);
  // Раскрытая строка: тела request/response грузятся лениво только для неё
  // (GET /api/nodes/:id/log/:logId). Список этих данных не содержит — иначе
  // сотни строк с большими JSON-телами вешают фронт (§7.4.1).
  const [expandedId, setExpandedId] = useState<string | null>(null);

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
                setLiveLost(false);
                setLiveUnavailable(false);
                setLive(e.target.checked);
              }}
            />
            {live ? t("logs.live_on") : t("logs.live_off")}
            {live && <RefreshCw className="h-3 w-3 animate-spin text-accent" />}
            {liveLost && !live && (
              <span className="text-xs text-warn">{t("logs.live_lost")}</span>
            )}
            {liveUnavailable && !live && (
              <span className="text-xs text-warn">{t("logs.unavailable")}</span>
            )}
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

      {(logsUnavailable || liveUnavailable) && hasLogsTable && (
        <div className="border-b border-warn/30 bg-warn/10 px-4 py-2 text-center text-xs text-warn">
          {t("logs.unavailable")}
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
                <th className="px-3 py-2 text-left">{t("logs.col.http_method")}</th>
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
                const isOpen = expandedId === r.id;
                return (
                  <Fragment key={r.id}>
                    <tr
                      onClick={() => setExpandedId(isOpen ? null : r.id)}
                      className={`cursor-pointer border-t border-line transition-colors hover:bg-bg-muted/60 ${
                        isHl ? "bg-accent/15" : isErr ? "bg-err/5" : ""
                      }`}
                    >
                      <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">
                        <span className="inline-flex items-center gap-1">
                          {isOpen ? (
                            <ChevronDown className="h-3 w-3 shrink-0" />
                          ) : (
                            <ChevronRight className="h-3 w-3 shrink-0" />
                          )}
                          {fmtLogTs(r.date_request)}
                        </span>
                      </td>
                      <td className="px-3 py-2">{r.http_method}</td>
                      <td className="px-3 py-2 font-mono text-xs text-fg-muted">{r.method}</td>
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
                          onClick={(e) => {
                            e.stopPropagation();
                            setReplayId(r.id);
                          }}
                          className="text-fg-muted hover:text-accent"
                        >
                          <RotateCcw className="h-4 w-4" />
                        </button>
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="border-t border-line bg-bg-muted/30">
                        <td colSpan={7} className="px-3 py-3">
                          <LogBodies nodeId={id} logId={r.id} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
              {visibleLogs.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-3 py-6 text-center text-fg-muted">
                    {logsQ.isLoading
                      ? t("common.loading")
                      : logsUnavailable
                        ? t("logs.unavailable")
                        : t("logs.empty")}
                  </td>
                </tr>
              )}
              {!live && logsQ.isFetchingNextPage && (
                <tr>
                  <td colSpan={7} className="px-3 py-4 text-center text-xs text-fg-muted">
                    {t("common.loading")}
                  </td>
                </tr>
              )}
              {!live && !logsQ.hasNextPage && visibleLogs.length > 0 && (
                <tr>
                  <td colSpan={7} className="px-3 py-3 text-center text-[11px] text-fg-muted">
                    {t("logs.no_more")}
                  </td>
                </tr>
              )}
              {/* §44: достигнут потолок строк — дальше не подгружаем (защита от
                  зависания на узлах с сотнями тысяч логов), просим сузить поиск. */}
              {!live &&
                logsQ.hasNextPage &&
                infiniteItems.length >= MAX_INFINITE_ROWS && (
                  <tr>
                    <td colSpan={7} className="px-3 py-3 text-center text-[11px] text-warn">
                      {t("logs.cap_reached", { n: MAX_INFINITE_ROWS })}
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

// LogBodies — ленивая подгрузка тел request/response одной записи (§7.4.1/§42).
// Монтируется только при раскрытии строки. Get отдаёт ПРЕВЬЮ тел (первые ~64K
// рун) + полные длины — большое тело не грузится разом и не вешает фронт;
// остаток тянется по кнопке «Показать весь» или скачивается файлом.
function LogBodies({ nodeId, logId }: { nodeId: string; logId: string }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["log", nodeId, logId],
    queryFn: () => api.get<LogDetail>(`/api/nodes/${nodeId}/log/${logId}`),
    staleTime: 60_000,
  });
  if (q.isLoading) {
    return <div className="text-xs text-fg-muted">{t("common.loading")}</div>;
  }
  if (q.isError || !q.data) {
    return <div className="text-xs text-err">{t("logs.detail.error")}</div>;
  }
  // Причина для не-успешных записей (done=0 / 4xx-5xx): напр. §43 «response body
  // exceeds max_body_size: N > M runes» — иначе пустой 502 выглядит загадочно.
  const reason = q.data.reason?.trim();
  const showReason = !!reason && reason !== "OK";
  return (
    <div className="space-y-3">
      {showReason && (
        <div className="rounded border border-warn/30 bg-warn/10 px-2 py-1.5 text-[11px] text-warn">
          <span className="font-medium">{t("logs.detail.reason")}: </span>
          <span className="break-all font-mono">{reason}</span>
        </div>
      )}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <LogBodyBlock
          nodeId={nodeId}
          logId={logId}
          which="request"
          title={t("logs.detail.request")}
          preview={q.data.request ?? ""}
          total={q.data.request_len ?? 0}
        />
        <LogBodyBlock
          nodeId={nodeId}
          logId={logId}
          which="response"
          title={t("logs.detail.response")}
          preview={q.data.response ?? ""}
          total={q.data.response_len ?? 0}
        />
      </div>
    </div>
  );
}

function LogBodyBlock({
  nodeId,
  logId,
  which,
  title,
  preview,
  total,
}: {
  nodeId: string;
  logId: string;
  which: "request" | "response";
  title: string;
  preview: string;
  total: number;
}) {
  const { t } = useTranslation();
  const [full, setFull] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const previewRunes = useMemo(() => [...preview].length, [preview]);
  // total из бэкенда в рунах; 0 → длины нет (пустое тело / старый ответ).
  const totalRunes = total || previewRunes;
  const hasMore = full === null && totalRunes > previewRunes;
  const shown = full ?? preview;
  const isEmpty = previewRunes === 0 && totalRunes === 0;

  async function loadFull() {
    if (loading) return;
    if (
      totalRunes > LARGE_WARN_RUNES &&
      !window.confirm(t("logs.detail.large_warning", { mb: Math.ceil(totalRunes / (1024 * 1024)) }))
    ) {
      return;
    }
    setLoading(true);
    try {
      let offset = 0;
      let acc = "";
      for (;;) {
        const r = await api.get<LogBodyChunk>(`/api/nodes/${nodeId}/log/${logId}/body`, {
          which,
          offset,
          limit: FETCH_CHUNK,
        });
        acc += r.chunk;
        offset += r.returned;
        if (r.eof || r.returned === 0) break;
      }
      setFull(acc);
    } catch {
      // тело не догрузилось — оставляем превью; пользователь может повторить/скачать
    } finally {
      setLoading(false);
    }
  }

  const downloadUrl = `/api/nodes/${nodeId}/log/${logId}/body/download?which=${which}`;

  return (
    <div className="min-w-0">
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="text-[10px] uppercase tracking-wider text-fg-muted">{title}</span>
        {!isEmpty && (
          <div className="flex items-center gap-1.5">
            <CopyButton value={shown} />
            <a
              href={downloadUrl}
              download
              title={t("logs.detail.download")}
              className="inline-flex shrink-0 items-center gap-1 rounded border border-line px-1.5 py-1 text-fg-muted transition-colors hover:bg-bg-muted hover:text-fg"
            >
              <Download className="h-3.5 w-3.5" />
            </a>
          </div>
        )}
      </div>
      <pre
        className={`${full !== null ? "max-h-[32rem]" : "max-h-72"} overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted/50 p-2 font-mono text-[11px]`}
      >
        {isEmpty ? t("logs.detail.empty") : prettyMaybe(shown)}
      </pre>
      {hasMore && (
        <div className="mt-1 flex items-center gap-2 text-[11px] text-fg-muted">
          <span>
            {t("logs.detail.shown_of", {
              shown: formatRunes(previewRunes),
              total: formatRunes(totalRunes),
            })}
          </span>
          <button
            onClick={loadFull}
            disabled={loading}
            className="rounded border border-line px-1.5 py-0.5 hover:bg-bg-muted hover:text-fg disabled:opacity-50"
          >
            {loading ? t("logs.detail.loading_full") : t("logs.detail.show_full")}
          </button>
        </div>
      )}
    </div>
  );
}
