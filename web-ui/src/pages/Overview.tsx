import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus, Star, Play, Pause } from "lucide-react";

import {
  api,
  type Node,
  type OverviewKPI,
  type NodesThroughputResp,
} from "../api/client";
import { useStableData } from "../lib/useStableData";
import {
  Button,
  Card,
  Chip,
  Field,
  Input,
  Kpi,
  KpiRow,
  ChartTooltip,
  PeriodPicker,
  Pill,
  Seg,
  Select,
  Tooltip,
  loadDefaultPeriod,
  saveDefaultPeriod,
  periodKey,
  periodParams,
  periodLabel,
  periodWindow,
  type Period,
} from "../components/ui";
import { Modal } from "../components/ui/Modal";
import {
  applyFilters,
  hasFilterParams,
  loadFilters,
  parseFilters,
  saveFilters,
  type MethodFilter,
  type OverviewFilters,
  type StatusFilter,
} from "../lib/overviewFilters";
import { cn } from "../lib/cn";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { useMetricsRefetchMs } from "../components/node/useNodeMetrics";

type ListResp = { items: Node[] };
type View = "table" | "cards";
type Throughput = {
  in: number;
  out: number;
  errors: number;
  p95: number;
  spark: number[];
  // §52: исход последнего вызова узла (ok/degraded/down).
  lastOutcome: "ok" | "degraded" | "down";
};

const VIEW_KEY = "nexus.overview.view";
const AUTOREFRESH_KEY = "nexus.overview.autorefresh";

// fmtNum — компактный формат больших чисел (1.24M / 12.0k) и разделители для мелких.
function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (n >= 10_000) return (n / 1000).toFixed(1) + "k";
  return n.toLocaleString();
}

export default function Overview() {
  const { t } = useTranslation();
  // §26/§28 Пункт 3: создание/редактирование узлов — только manager+.
  const canEdit = useRoleAtLeast("manager");
  // Перенос узла между командами — admin-only (как и сам /move-эндпоинт):
  // не показываем кнопку «Перенести» viewer/manager, иначе клик упрётся в 403.
  const canMove = useRoleAtLeast("admin");
  // §54: фильтры (поиск/метод/статус/живой период) — производные от URL, не
  // useState: иначе они умирают при уходе на страницу узла (Overview — дочерний
  // Outlet, размонтируется) и «Назад» возвращает пустой экран. Зеркало в
  // sessionStorage добавляет кейс, где query теряется (кнопка «Узлы» = to="/").
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => parseFilters(params), [params]);
  const { search, method, status: statusFilter, period } = filters;

  // updateFilters — единая точка записи. saveFilters строго ДО setParams (§54.4):
  // иначе при ручной очистке фильтров restore-эффект ниже увидит «URL пуст,
  // зеркало ещё непусто» и воскресит только что очищенное.
  const updateFilters = useCallback(
    (patch: Partial<OverviewFilters>) => {
      const next = { ...filters, ...patch };
      saveFilters(next);
      setParams((prev) => applyFilters(prev, next), { replace: true });
    },
    [filters, setParams],
  );

  // Восстановление и зеркалирование. Пустой URL + непустое зеркало → вернуть
  // фильтры (replace, чтобы не ломать Back). Иначе URL — истина, зеркалим его.
  useEffect(() => {
    if (hasFilterParams(params)) {
      saveFilters(parseFilters(params));
      return;
    }
    const stored = loadFilters();
    if (!stored) return;
    setParams((prev) => applyFilters(prev, stored), { replace: true });
  }, [params, setParams]);

  // §44.B: savedDefault — для подсветки активного «по умолчанию» (звёздочки).
  const [savedDefault, setSavedDefault] = useState<Period>(() => loadDefaultPeriod());
  const [view, setView] = useState<View>(
    () => (localStorage.getItem(VIEW_KEY) as View) || "table",
  );
  const [moveTarget, setMoveTarget] = useState<Node | null>(null);
  // §44.C: автообновление рабочего стола — тоггл паузы (по умолчанию вкл),
  // персист в localStorage; интервал берётся из настроек (useMetricsRefetchMs).
  const [autoRefresh, setAutoRefresh] = useState(
    () => localStorage.getItem(AUTOREFRESH_KEY) !== "0",
  );
  const refetchMs = useMetricsRefetchMs();

  // §54.4: поиск коммитится в URL с задержкой. Не косметика — Safari троттлит
  // history.replaceState (~100 вызовов/30с, дальше SecurityError), а запись на
  // каждый keystroke в этот лимит упирается. Побочно снимает спам в /api/nodes.
  const [searchInput, setSearchInput] = useState(search);
  // committed — последнее значение, доехавшее до URL. Нужен, чтобы sync ниже не
  // затирал ввод пользователя, набранный уже после коммита.
  const committed = useRef(search);

  useEffect(() => {
    // Внешнее изменение поиска (восстановление, «Назад», дип-линк) → в инпут.
    if (search !== committed.current) {
      committed.current = search;
      setSearchInput(search);
    }
  }, [search]);

  useEffect(() => {
    if (searchInput === committed.current) return;
    const id = setTimeout(() => {
      committed.current = searchInput;
      updateFilters({ search: searchInput });
    }, 300);
    return () => clearTimeout(id);
  }, [searchInput, updateFilters]);

  useEffect(() => localStorage.setItem(VIEW_KEY, view), [view]);
  useEffect(() => localStorage.setItem(AUTOREFRESH_KEY, autoRefresh ? "1" : "0"), [autoRefresh]);

  const nodesQ = useQuery({
    queryKey: ["nodes", search],
    queryFn: () => api.get<ListResp>("/api/nodes", { search }),
  });

  const kpiQ = useQuery({
    queryKey: ["metrics-overview"],
    queryFn: () => api.get<OverviewKPI>("/api/metrics/overview"),
    refetchInterval: autoRefresh ? refetchMs : false,
  });

  // §28 Пункт 4: период per-node throughput выбирается (по умолчанию 24ч),
  // §28 Пункт 2 / §44.C: обновляется онлайн, если автообновление включено.
  const thrQ = useQuery({
    queryKey: ["metrics-nodes", periodKey(period)],
    queryFn: () => api.get<NodesThroughputResp>("/api/metrics/nodes", periodParams(period)),
    refetchInterval: autoRefresh ? refetchMs : false,
  });

  // Анти-мерцание: держим последний ответ с prometheus_available=true (§ useStableData).
  const thrData = useStableData(thrQ.data, periodKey(period), (d) => d.prometheus_available);

  const throughput = useMemo(() => {
    const m = new Map<string, Throughput>();
    for (const it of thrData?.items ?? []) {
      m.set(it.node, { in: it.in, out: it.out, errors: it.errors, p95: it.p95_ms, spark: it.spark ?? [], lastOutcome: it.last_outcome });
    }
    return m;
  }, [thrData]);

  // Сортировка: проблемные первыми (err → degraded → warn → paused → ok →
  // disabled), внутри статуса — по убыванию входящего трафика (§22, ui_cards.html).
  const sortRank: Record<Variant, number> = useMemo(
    () => ({ err: 0, degraded: 1, warn: 2, paused: 3, ok: 4, unknown: 5, disabled: 6 }),
    [],
  );

  // metricsReady — метрики throughput реально пришли и Prometheus доступен.
  // Пока не готовы, статус узла показываем нейтральным «unknown», а не зелёным
  // «OK» (П11: статус мигал ОК→down при дозагрузке метрик).
  const metricsReady = thrQ.isSuccess && (thrData?.prometheus_available ?? false);

  const nodes = useMemo(() => {
    let items = nodesQ.data?.items ?? [];
    if (method) items = items.filter((n) => n.root_method === method);
    if (statusFilter !== "all") {
      items = items.filter(
        (n) => nodeVariant(n, throughput.get(n.path), metricsReady) === statusFilter,
      );
    }
    return [...items].sort((a, b) => {
      const va = nodeVariant(a, throughput.get(a.path), metricsReady);
      const vb = nodeVariant(b, throughput.get(b.path), metricsReady);
      if (sortRank[va] !== sortRank[vb]) return sortRank[va] - sortRank[vb];
      return (throughput.get(b.path)?.in ?? 0) - (throughput.get(a.path)?.in ?? 0);
    });
  }, [nodesQ.data, method, statusFilter, throughput, sortRank, metricsReady]);

  const kpi = useStableData(kpiQ.data, "overview-kpi", (d) => d.prometheus_available);
  // §44.A: трафик KPI шапки = totals из throughput (сумма строк таблицы за
  // выбранный период, ClickHouse) → шапка сходится с таблицей. Очередь Kafka —
  // из kpiQ (мгновенный lag, только в Prometheus). Ярлык несёт выбранный период.
  const tot = thrData?.totals;
  const kpiPeriod = periodLabel(period, t);
  const errPct = tot && tot.error_rate > 0 ? (tot.error_rate * 100).toFixed(2) + "%" : "0%";

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <KpiRow>
        <Kpi label={`${t("overview.kpi.incoming")} ${kpiPeriod}`} value={tot ? fmtNum(tot.incoming) : "—"} />
        <Kpi label={`${t("overview.kpi.outgoing")} ${kpiPeriod}`} value={tot ? fmtNum(tot.outgoing) : "—"} />
        <Kpi label={t("overview.kpi.queue")} value={kpi ? fmtNum(kpi.kafka_queue) : "—"} />
        <Kpi
          label={`${t("overview.kpi.errors")} ${kpiPeriod}`}
          value={tot ? fmtNum(tot.errors) : "—"}
          delta={tot ? errPct : undefined}
          deltaTone={tot && tot.error_rate > 0.01 ? "down" : "muted"}
        />
      </KpiRow>

      {kpi && !kpi.prometheus_available && (
        <div className="text-xs text-fg-subtle">{t("metrics.prometheus_off")}</div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
          <Input
            className="pl-9"
            placeholder={t("overview.search_placeholder")}
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
          />
        </div>
        <Select
          className="w-40"
          value={method}
          onChange={(e) => updateFilters({ method: e.target.value as MethodFilter })}
        >
          <option value="">{t("overview.filter.all_methods")}</option>
          <option value="request">request</option>
          <option value="requestAsync">requestAsync</option>
          <option value="RabbitMQAsync">RabbitMQAsync</option>
        </Select>
        <Select
          className="w-40"
          value={statusFilter}
          onChange={(e) => updateFilters({ status: e.target.value as StatusFilter })}
        >
          <option value="all">{t("overview.filter.all_statuses")}</option>
          <option value="ok">{t("overview.status.ok")}</option>
          <option value="warn">{t("overview.status.queue")}</option>
          <option value="degraded">{t("overview.status.degraded")}</option>
          <option value="err">{t("overview.status.down")}</option>
          <option value="paused">{t("node.status.paused")}</option>
          <option value="disabled">{t("node.status.disabled")}</option>
        </Select>
        <Seg
          value={view}
          onChange={setView}
          options={[
            { value: "table", label: t("overview.view.table") },
            { value: "cards", label: t("overview.view.cards") },
          ]}
        />
        {canEdit && (
          <Link to="/nodes/new">
            <Button variant="primary">
              <Plus className="h-4 w-4" /> {t("overview.new_node")}
            </Button>
          </Link>
        )}
      </div>

      {/* §28 Пункт 4: период метрик — отдельной строкой под фильтрами. */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-fg-muted">{t("metrics.period")}</span>
        <PeriodPicker value={period} onChange={(p) => updateFilters({ period: p })} />
        {/* §44.B: «под себя» — сохранить текущий период как дефолт (только пресет). */}
        {period.kind === "preset" && (
          <button
            type="button"
            onClick={() => {
              saveDefaultPeriod(period);
              setSavedDefault(period);
            }}
            disabled={
              savedDefault.kind === "preset" && savedDefault.range === period.range
            }
            title={t("overview.set_default_period")}
            className="inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted hover:text-accent disabled:cursor-default disabled:opacity-50"
          >
            <Star
              className={cn(
                "h-3.5 w-3.5",
                savedDefault.kind === "preset" &&
                  savedDefault.range === period.range &&
                  "fill-current text-accent",
              )}
            />
            {t("overview.set_default_period")}
          </button>
        )}
        {/* §44.C: пауза/запуск автообновления рабочего стола. */}
        <button
          type="button"
          onClick={() => setAutoRefresh((v) => !v)}
          title={autoRefresh ? t("overview.autorefresh_on") : t("overview.autorefresh_off")}
          className="ml-auto inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted hover:text-accent"
        >
          {autoRefresh ? (
            <>
              <Pause className="h-3.5 w-3.5" />
              {t("overview.autorefresh_on")}
            </>
          ) : (
            <>
              <Play className="h-3.5 w-3.5" />
              {t("overview.autorefresh_off")}
            </>
          )}
        </button>
      </div>

      {nodesQ.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {nodesQ.error && <div className="text-err">{(nodesQ.error as Error).message}</div>}
      {nodesQ.data && nodes.length === 0 && (
        <div className="text-fg-muted">{t("overview.empty")}</div>
      )}

      {nodes.length > 0 &&
        (view === "table" ? (
          <NodeTable nodes={nodes} throughput={throughput} onMove={canMove ? setMoveTarget : undefined} period={period} ready={metricsReady} />
        ) : (
          <NodeCards nodes={nodes} throughput={throughput} onMove={canMove ? setMoveTarget : undefined} period={period} ready={metricsReady} />
        ))}

      {moveTarget && (
        <MoveNodeDialog node={moveTarget} onClose={() => setMoveTarget(null)} />
      )}
    </div>
  );
}

type Variant = "ok" | "warn" | "degraded" | "err" | "paused" | "disabled" | "unknown";
type StatusInfo = { tone: "ok" | "err" | "warn" | "muted"; label: string; variant: Variant };

// nodeVariant — чистая классификация статуса узла (без i18n), для фильтра,
// сортировки и цвета акцента карточки (§22, ui_cards.html). ready=false (метрики
// ещё не пришли / Prometheus недоступен) → нейтральный "unknown", чтобы не
// показывать ложный зелёный «OK» до загрузки данных (П11).
//
// §41/§52: runtime-статус определяется по ИСХОДУ ПОСЛЕДНЕГО исходящего вызова
// узла (m.lastOutcome), а не по доле ошибок за период. Это снимок «сейчас»:
// down (err, красный) — транспортная ошибка или 5xx; degraded (жёлтый) — узел
// ответил, но не-2xx <500 (ошибка данных/клиента, §50.4 — узел жив); 2xx — ok.
// "degraded" — отдельный вариант от "warn" (warn = отставание async-очереди).
// Колонки in/out/errors остаются за выбранный период.
function nodeVariant(n: Node, m: Throughput | undefined, ready: boolean): Variant {
  if (n.status === "disabled") return "disabled";
  if (n.status === "paused") return "paused";
  if (!ready || !m) return "unknown";
  if (m.lastOutcome === "down") return "err";
  if (m.lastOutcome === "degraded") return "degraded";
  if (n.root_method === "requestAsync" && m.in - m.out > Math.max(50, m.in * 0.1)) {
    return "warn";
  }
  return "ok";
}

// accentByVariant — класс полосы-акцента слева (border-l) по статусу.
const accentByVariant: Record<Variant, string> = {
  ok: "border-l-ok",
  warn: "border-l-warn",
  degraded: "border-l-warn",
  err: "border-l-err",
  paused: "border-l-accent",
  disabled: "border-l-fg-subtle",
  unknown: "border-l-line",
};

function useStatus() {
  const { t } = useTranslation();
  return (n: Node, m: Throughput | undefined, ready: boolean): StatusInfo => {
    const variant = nodeVariant(n, m, ready);
    switch (variant) {
      case "disabled":
        return { variant, tone: "err", label: t("node.status.disabled") };
      case "paused":
        return { variant, tone: "warn", label: t("node.status.paused") };
      case "err":
        return { variant, tone: "err", label: t("overview.status.down") };
      case "degraded":
        return { variant, tone: "warn", label: t("overview.status.degraded") };
      case "warn":
        return { variant, tone: "warn", label: t("overview.status.queue") };
      case "unknown":
        return { variant, tone: "muted", label: t("overview.status.unknown") };
      default:
        return { variant, tone: "ok", label: t("overview.status.ok") };
    }
  };
}

function NodeTable({
  nodes,
  throughput,
  onMove,
  period,
  ready,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  onMove?: (n: Node) => void;
  period: Period;
  ready: boolean;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  const plabel = periodLabel(period, t);
  return (
    <Card className="overflow-hidden p-0">
      <div className="overflow-x-auto">
      <table className="w-full min-w-[640px] text-[12.5px]">
        <thead>
          <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
            <th className="px-3 py-2 font-medium">{t("overview.table.path")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.method")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.in")} {plabel}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.out")} {plabel}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.errors")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.status")}</th>
            <th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {nodes.map((n) => {
            const m = throughput.get(n.path);
            const s = status(n, m, ready);
            return (
              <tr key={n.id} className="border-b border-line last:border-0 hover:bg-bg-muted">
                <td className="px-3 py-2.5 font-mono">
                  <Link to={`/nodes/${n.id}`} className="hover:text-accent">
                    {n.path}
                  </Link>
                </td>
                <td className="px-3 py-2.5">
                  <Chip>{n.root_method}</Chip>
                </td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.in) : "—"}</td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.out) : "—"}</td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.errors) : "—"}</td>
                <td className="px-3 py-2.5">
                  <Pill tone={s.tone}>{s.label}</Pill>
                </td>
                <td className="px-3 py-2.5 text-right">
                  {onMove && (
                    <button
                      type="button"
                      onClick={() => onMove(n)}
                      className="text-xs text-fg-subtle hover:text-accent"
                    >
                      {t("overview.move.action")}
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      </div>
    </Card>
  );
}

// fmtMs — компактный формат p95-латентности (ms / s).
function fmtMs(ms: number): string {
  if (ms <= 0) return "—";
  if (ms >= 1000) return (ms / 1000).toFixed(1) + "s";
  return Math.round(ms) + "ms";
}

function NodeCards({
  nodes,
  throughput,
  onMove,
  period,
  ready,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  onMove?: (n: Node) => void;
  period: Period;
  ready: boolean;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  const plabel = periodLabel(period, t);
  const navigate = useNavigate();
  return (
    <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3">
      {nodes.map((n) => {
        const m = throughput.get(n.path);
        const s = status(n, m, ready);
        const target =
          n.url_mode === "from_request" ? t("overview.card.dynamic_url") : n.target_url || "—";
        return (
          <Card
            key={n.id}
            className={cn(
              "flex h-full flex-col gap-3 border-l-[3px]",
              accentByVariant[s.variant],
              n.status === "disabled" && "opacity-70",
            )}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <Link
                  to={`/nodes/${n.id}`}
                  className="block truncate font-mono text-[13px] font-medium hover:text-accent"
                >
                  {n.path}
                </Link>
                <div className="mt-1 flex items-center gap-1.5">
                  <Chip>{n.root_method}</Chip>
                  <Pill tone={s.tone}>
                    <span className="mr-1 inline-block h-1.5 w-1.5 rounded-full bg-current" />
                    {s.label}
                  </Pill>
                </div>
              </div>
            </div>
            <div className="grid grid-cols-3 gap-2 border-y border-line py-2.5 text-center">
              <CardStat label={`${t("overview.table.in")} ${plabel}`} value={m ? fmtNum(m.in) : "—"} />
              <CardStat label="p95" value={m ? fmtMs(m.p95) : "—"} />
              <CardStat
                label={t("overview.table.errors")}
                value={m ? fmtNum(m.errors) : "—"}
                tone={m && m.errors > 0 ? "err" : undefined}
              />
            </div>
            <Sparkline
              data={m?.spark ?? []}
              variant={s.variant}
              period={period}
              onOpenLogs={
                n.clickhouse_table
                  ? (r) =>
                      navigate(
                        `/nodes/${n.id}?tab=logs&from=${Math.round(r.from)}&to=${Math.round(r.to)}`,
                      )
                  : undefined
              }
            />
            <div className="flex items-center justify-between gap-2 text-[11px] text-fg-subtle">
              <span className="truncate font-mono" title={target}>
                {target}
              </span>
              {onMove && (
                <button
                  type="button"
                  onClick={() => onMove(n)}
                  className="shrink-0 hover:text-accent"
                >
                  {t("overview.move.action")}
                </button>
              )}
            </div>
          </Card>
        );
      })}
    </div>
  );
}

function CardStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: "err";
}) {
  return (
    <div>
      <div className={cn("font-mono text-[15px]", tone === "err" && "text-err")}>{value}</div>
      <div className="text-[10px] uppercase tracking-wide text-fg-subtle">{label}</div>
    </div>
  );
}

// fmtBucket — подпись временного окна одного столбца спарклайна. Для окна ≤ 24ч
// показываем только время (ЧЧ:ММ), для длинных периодов — дату со временем.
function fmtBucket(start: number, end: number, multiDay: boolean): string {
  const opts: Intl.DateTimeFormatOptions = multiDay
    ? { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }
    : { hour: "2-digit", minute: "2-digit" };
  return `${new Date(start).toLocaleString([], opts)}–${new Date(end).toLocaleString([], opts)}`;
}

// Sparkline — мини-график входящего трафика за период (§22, ui_cards.html).
// На каждом столбце единый тултип (§33): окно бакета + число входящих запросов.
// Radix-тултип монтирует контент лениво на hover (провайдер общий в AppShell) —
// на Overview много карточек, но накладные минимальны.
// onOpenLogs (§47.4) — клик по столбцу спарклайна открывает логи узла за окно
// этого бакета (как клик по графику на странице узла). Задаётся только для
// узлов с таблицей логов; иначе бары некликабельны.
function Sparkline({
  data,
  variant,
  period,
  onOpenLogs,
}: {
  data: number[];
  variant: Variant;
  period: Period;
  onOpenLogs?: (range: { from: number; to: number }) => void;
}) {
  const { t } = useTranslation();
  if (data.length === 0) {
    return <div className="h-7" />;
  }
  const max = Math.max(1, ...data);
  const color =
    variant === "err"
      ? "bg-err"
      : variant === "warn"
        ? "bg-warn"
        : variant === "disabled"
          ? "bg-fg-subtle"
          : "bg-accent";
  const { since, until } = periodWindow(period);
  const bucketW = (until - since) / data.length;
  const multiDay = until - since > 86_400_000;
  return (
    <div className="flex h-7 items-end gap-px">
      {data.map((v, i) => {
        const start = since + i * bucketW;
        return (
          <Tooltip
            key={i}
            side="top"
            delayDuration={150}
            content={
              <ChartTooltip
                compact
                period={{ from: fmtBucket(start, start + bucketW, multiDay) }}
                primary={{ value: fmtNum(Math.round(v)), unit: t("metrics.tooltip.requests") }}
              />
            }
          >
            <span
              className={cn("flex-1 rounded-sm opacity-80", color, onOpenLogs && "cursor-pointer")}
              style={{ height: `${Math.max(4, (v / max) * 100)}%` }}
              onClick={
                onOpenLogs ? () => onOpenLogs({ from: start, to: start + bucketW }) : undefined
              }
            />
          </Tooltip>
        );
      })}
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
    mutationFn: () => api.post(`/api/nodes/${node.id}/move`, { target_team_slug: slug }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      onClose();
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <Modal
      title={t("overview.move.title")}
      subtitle={node.path}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={!slug || move.isPending} onClick={() => move.mutate()}>
            {t("overview.move.submit")}
          </Button>
        </>
      }
    >
      {error && (
        <div className="mb-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
      )}
      <Field label={t("overview.move.target_team")} hint={t("overview.move.hint")}>
        <Select value={slug} onChange={(e) => setSlug(e.target.value)}>
          <option value="">{t("overview.move.pick_team")}</option>
          {(teams.data?.items ?? []).map((tm) => (
            <option key={tm.id} value={tm.slug}>
              {tm.name} ({tm.slug})
            </option>
          ))}
        </Select>
      </Field>
    </Modal>
  );
}
