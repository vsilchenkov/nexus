import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus, Star, Play, Pause } from "lucide-react";

import {
  api,
  type Node,
  type OverviewKPI,
  type NodesThroughputResp,
  type NodesTotalsResp,
} from "../api/client";
import { useStableData } from "../lib/useStableData";
import {
  Button,
  Card,
  Chip,
  Input,
  Kpi,
  KpiRow,
  ChartTooltip,
  PeriodPicker,
  Pill,
  Popover,
  PopoverAnchor,
  PopoverContent,
  Seg,
  Select,
  Tooltip,
  periodKey,
  periodParams,
  periodLabel,
  periodWindow,
  type Period,
} from "../components/ui";
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
import {
  PREF_KEY_OVERVIEW_PERIOD,
  useMigrateLegacyPeriodPref,
  useSetPref,
  useTeamDefaultPeriod,
} from "../lib/prefs";
import { scopeParams, teamScopeKey, useAllTeamsScope } from "../lib/teamScope";
import { useCurrentTeamID, useMyTeams } from "../lib/teams";
import { useVisibleNodeMetrics } from "../lib/visibleMetrics";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { useMetricsRefetchMs } from "../components/node/useNodeMetrics";
import { SearchHistoryList } from "../components/SearchHistoryList";
import { MIN_SEARCH_QUERY_LEN, useRecordSearch, useSearchHistory } from "../lib/searchHistory";

type ListResp = { items: Node[] };
type View = "table" | "cards";
type Throughput = {
  in: number;
  out: number;
  errors: number;
  p95: number;
  spark: number[];
  // §52: исход последнего вызова узла (ok/degraded/down).
  // §86.8: "unknown" — исход неизвестен, потому что он ключуется ПУТЁМ
  // (nexus:node:last_error:<path> в Redis, метка node в Prometheus), а путь
  // уникален лишь внутри команды: у двух одноимённых узлов разных команд
  // значение общее и как минимум одному из них не принадлежит.
  lastOutcome: "ok" | "degraded" | "down" | "unknown";
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
  // teamId в ключах team-scoped запросов обязателен: сервер фильтрует ответ по
  // команде СЕССИИ, и без teamId записи разных команд алиасятся в один слот
  // кеша — после смены команды (например, через глобальный поиск §62) стирание
  // поля поиска мгновенно показывало закешированный список ПРЕЖНЕЙ команды.
  // enabled: пока членства не загрузились (teamId=""), запрос не шлём — иначе
  // ответ лёг бы под ключ с пустым teamId и алиасился между командами.
  const teamId = useCurrentTeamID();
  // §86: сквозной режим «Все команды». Скоуп обязан входить в queryKey — иначе
  // выдача всех команд и выдача одной алиасятся в один слот кеша (правило
  // §4.44, из-за него уже был баг «стёр поиск → узлы не той команды»).
  const allTeams = useAllTeamsScope();

  // §71: дефолтный период — персональный и СВОЙ У КАЖДОЙ КОМАНДЫ, хранится на
  // сервере (преф команды → глобальный преф → системные 24ч). При переключении
  // команды период всегда сбрасывается на её дефолт (эффект ниже).
  const { value: teamDefault, settled: prefsSettled } = useTeamDefaultPeriod(teamId);
  // periodReady — и членства, и префы разрешились. До этого момента дефолтный
  // период неизвестен, и любая запись в URL/зеркало была бы записью НЕ ТОГО
  // периода (у пользователя с дефолтом 7д в зеркале осел бы 24ч).
  const periodReady = teamId !== "" && prefsSettled;
  // §71.5: разовый перенос прежнего localStorage-значения на сервер.
  useMigrateLegacyPeriodPref();
  // serializeDefault — дефолт для решения «писать ли период в URL/зеркало».
  // Пока префы не пришли — null («дефолт неизвестен», период пишется всегда):
  // иначе правка любого другого фильтра в это окно посчитала бы период
  // дефолтным по системным 24ч и стёрла бы из URL явно выбранный range=24h.
  const serializeDefault = periodReady ? teamDefault : null;

  // §54: фильтры (поиск/метод/статус/живой период) — производные от URL, не
  // useState: иначе они умирают при уходе на страницу узла (Overview — дочерний
  // Outlet, размонтируется) и «Назад» возвращает пустой экран. Зеркало в
  // sessionStorage добавляет кейс, где query теряется (кнопка «Узлы» = to="/").
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => parseFilters(params, teamDefault), [params, teamDefault]);
  const { search, method, status: statusFilter, period } = filters;

  // updateFilters — единая точка записи. saveFilters строго ДО setParams (§54.4):
  // иначе при ручной очистке фильтров restore-эффект ниже увидит «URL пуст,
  // зеркало ещё непусто» и воскресит только что очищенное.
  const updateFilters = useCallback(
    (patch: Partial<OverviewFilters>) => {
      const next = { ...filters, ...patch };
      saveFilters(next, serializeDefault);
      setParams((prev) => applyFilters(prev, next, serializeDefault), { replace: true });
    },
    [filters, setParams, serializeDefault],
  );

  // Восстановление и зеркалирование. Пустой URL + непустое зеркало → вернуть
  // фильтры (replace, чтобы не ломать Back). Иначе URL — истина, зеркалим его.
  // Гейт по periodReady обязателен (§71): до прихода префов «дефолтным» считался
  // бы системный 24ч, и период пользователя уехал бы в URL как «не дефолтный».
  useEffect(() => {
    if (!periodReady) return;
    if (hasFilterParams(params)) {
      saveFilters(parseFilters(params, teamDefault), teamDefault);
      return;
    }
    const stored = loadFilters(teamDefault);
    if (!stored) return;
    setParams((prev) => applyFilters(prev, stored, teamDefault), { replace: true });
  }, [params, setParams, periodReady, teamDefault]);

  // §71: при ФАКТИЧЕСКОМ переключении команды период сбрасывается на её дефолт —
  // даже если до этого был выбран вручную или пришёл ссылкой. Иначе, работая в
  // двух командах с разными горизонтами наблюдения, приходится каждый раз
  // переставлять период руками.
  //
  // Первое появление teamId сбросом НЕ считается: useCurrentTeamID отдаёт ""
  // до загрузки членств, и наивная проверка «id изменился» затёрла бы дип-линк
  // /?range=30d при обычном открытии страницы. Приём одноразового guard'а — как
  // handledKey в lib/nodeShare.ts (§58).
  const seenTeam = useRef("");
  useEffect(() => {
    if (!periodReady) return;
    if (seenTeam.current === teamId) return;
    const firstResolve = seenTeam.current === "";
    seenTeam.current = teamId;
    if (firstResolve) return;
    updateFilters({ period: teamDefault });
  }, [teamId, periodReady, teamDefault, updateFilters]);

  // §44.B/§71: дефолт для подсветки звёздочки — производный от команды, не
  // локальный state. Раньше это был useState с ленивой инициализацией, из-за
  // чего после смены команды подсветка показывала дефолт прежней команды.
  const savedDefault = teamDefault;
  const setDefaultPeriod = useSetPref();
  const teamsQ = useMyTeams();
  const currentTeamName =
    teamsQ.data?.items.find((m) => m.id === teamsQ.data?.current_team_id)?.name ?? "";
  // §86.3: имя команды резолвит клиент — членства уже на руках, и сервер выдачу
  // не обогащает. null вне сквозного режима = колонки/чипа команды нет вовсе.
  const teamNames = useMemo(() => {
    if (!allTeams) return null;
    const m = new Map<string, string>();
    for (const tm of teamsQ.data?.items ?? []) m.set(tm.id, tm.name);
    return m;
  }, [allTeams, teamsQ.data]);
  const [view, setView] = useState<View>(
    () => (localStorage.getItem(VIEW_KEY) as View) || "table",
  );
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

  // §62: история поиска в поле «Поиск» (общая с глобальным поиском в шапке).
  // Дропдаун открывается по фокусу, если история непуста; запись строки — по
  // Enter и по blur непустого поля (сервер дедупит). pickingHistory гасит
  // запись «на blur» при клике по пункту истории (чтобы не сохранять начатую,
  // но не завершённую строку вместо выбранной).
  const recordSearch = useRecordSearch();
  const searchHistoryQ = useSearchHistory();
  const hasHistory = (searchHistoryQ.data?.items.length ?? 0) > 0;
  const [historyOpen, setHistoryOpen] = useState(false);
  const pickingHistory = useRef(false);

  const recordIfValid = useCallback(
    (value: string) => {
      if (value.trim().length >= MIN_SEARCH_QUERY_LEN) recordSearch.mutate(value);
    },
    [recordSearch],
  );

  const pickHistory = useCallback((entry: string) => {
    pickingHistory.current = true;
    setSearchInput(entry);
    setHistoryOpen(false);
  }, []);

  useEffect(() => localStorage.setItem(VIEW_KEY, view), [view]);
  useEffect(() => localStorage.setItem(AUTOREFRESH_KEY, autoRefresh ? "1" : "0"), [autoRefresh]);

  const scopeKey = teamScopeKey(allTeams, teamId);
  const scopeQuery = useMemo(() => scopeParams(allTeams), [allTeams]);
  // В сквозном режиме команда сессии не участвует, поэтому ждать её незачем —
  // иначе список не загрузился бы у пользователя, у которого она ещё не пришла.
  const scopeReady = allTeams || teamId !== "";

  const nodesQ = useQuery({
    queryKey: ["nodes", scopeKey, search],
    queryFn: () => api.get<ListResp>("/api/nodes", { ...scopeQuery, search }),
    enabled: scopeReady,
  });

  const kpiQ = useQuery({
    queryKey: ["metrics-overview", scopeKey],
    queryFn: () => api.get<OverviewKPI>("/api/metrics/overview"),
    refetchInterval: autoRefresh ? refetchMs : false,
    enabled: scopeReady,
  });

  // §28 Пункт 4: период per-node throughput выбирается (дефолт — персональный
  // для команды, §71), §28 Пункт 2 / §44.C: обновляется онлайн, если
  // автообновление включено.
  //
  // enabled ждёт periodReady, а не только teamId (§71): иначе первый запрос
  // ушёл бы с системными 24ч, а следом второй — с настоящим дефолтом команды.
  // Это двойная нагрузка на ClickHouse на КАЖДЫЙ заход на рабочий стол.
  const thrQ = useQuery({
    queryKey: ["metrics-nodes", scopeKey, periodKey(period)],
    queryFn: () => api.get<NodesThroughputResp>("/api/metrics/nodes", periodParams(period)),
    refetchInterval: autoRefresh ? refetchMs : false,
    // §86.4: в сквозном режиме одним запросом метрики не считаются — строки
    // грузятся порционно (useVisibleNodeMetrics), а шапка отдельным агрегатом.
    enabled: periodReady && !allTeams,
  });

  // §86.4: порционная загрузка — только сквозной режим. Режим одной команды
  // идёт прежним путём: там всё приезжает одним запросом и менять нечего.
  const visible = useVisibleNodeMetrics({
    enabled: allTeams && periodReady,
    scopeKey,
    periodKey: periodKey(period),
    params: useMemo(
      () => ({ ...periodParams(period), ...scopeQuery }),
      [period, scopeQuery],
    ),
    refetchInterval: autoRefresh ? refetchMs : false,
  });

  // §86.4: агрегат шапки в сквозном режиме считается отдельно и приезжает
  // позже строк — сумма по ВСЕМ узлам скоупа, а не по загруженным (иначе
  // значение шапки менялось бы от прокрутки).
  const totalsQ = useQuery({
    queryKey: ["metrics-totals", scopeKey, periodKey(period)],
    queryFn: () =>
      api.get<NodesTotalsResp>("/api/metrics/totals", { ...periodParams(period), ...scopeQuery }),
    refetchInterval: autoRefresh ? refetchMs : false,
    enabled: periodReady && allTeams,
  });

  // Анти-мерцание: держим последний ответ с prometheus_available=true (§ useStableData).
  const thrData = useStableData(thrQ.data, periodKey(period), (d) => d.prometheus_available);

  // Ключ карты — id узла, а НЕ путь (§86.7): путь уникален лишь внутри команды,
  // и в сквозном списке два узла разных команд могут иметь одинаковый.
  // §86.8: пути, встречающиеся в скоупе больше одного раза. Внутри команды путь
  // уникален (UNIQUE(team_id, path)), поэтому набор непуст только в сквозном
  // режиме — и только там бейдж исхода может принадлежать чужому узлу.
  const ambiguousPaths = useMemo(() => {
    if (!allTeams) return null;
    const seen = new Map<string, number>();
    for (const n of nodesQ.data?.items ?? []) seen.set(n.path, (seen.get(n.path) ?? 0) + 1);
    const dup = new Set<string>();
    for (const [path, count] of seen) if (count > 1) dup.add(path);
    return dup;
  }, [allTeams, nodesQ.data]);

  const throughput = useMemo(() => {
    const m = new Map<string, Throughput>();
    const src = allTeams ? Array.from(visible.items.values()) : (thrData?.items ?? []);
    for (const it of src) {
      if (!it.node_id) continue;
      m.set(it.node_id, {
        in: it.in,
        out: it.out,
        errors: it.errors,
        p95: it.p95_ms,
        spark: it.spark ?? [],
        // Числа и спарклайн приходят из ClickHouse с фильтром по node_id и
        // коллизией путей не задеты — подменяется только исход (§86.8).
        lastOutcome: ambiguousPaths?.has(it.node) ? "unknown" : it.last_outcome,
      });
    }
    return m;
  }, [thrData, allTeams, visible.items, ambiguousPaths]);

  // Сортировка: проблемные первыми (err → degraded → warn → paused → ok →
  // disabled), внутри статуса — по убыванию входящего трафика (§22, ui_cards.html).
  const sortRank: Record<Variant, number> = useMemo(
    () => ({ err: 0, degraded: 1, warn: 2, paused: 3, ok: 4, unknown: 5, disabled: 6 }),
    [],
  );

  // metricsReady — метрики throughput реально пришли и Prometheus доступен.
  // Пока не готовы, статус узла показываем нейтральным «unknown», а не зелёным
  // «OK» (П11: статус мигал ОК→down при дозагрузке метрик).
  // В сквозном режиме источник другой (порционные пачки), и признак готовности
  // строится по факту наличия метрик у узла: единого «ответ пришёл» здесь нет —
  // строки досчитываются по мере прокрутки.
  const metricsReady = allTeams
    ? visible.items.size > 0
    : thrQ.isSuccess && (thrData?.prometheus_available ?? false);

  const nodes = useMemo(() => {
    let items = nodesQ.data?.items ?? [];
    if (method) items = items.filter((n) => n.root_method === method);
    if (statusFilter !== "all") {
      items = items.filter(
        (n) => nodeVariant(n, throughput.get(n.id), metricsReady) === statusFilter,
      );
    }
    return [...items].sort((a, b) => {
      const va = nodeVariant(a, throughput.get(a.id), metricsReady);
      const vb = nodeVariant(b, throughput.get(b.id), metricsReady);
      if (sortRank[va] !== sortRank[vb]) return sortRank[va] - sortRank[vb];
      return (throughput.get(b.id)?.in ?? 0) - (throughput.get(a.id)?.in ?? 0);
    });
  }, [nodesQ.data, method, statusFilter, throughput, sortRank, metricsReady]);

  const kpi = useStableData(kpiQ.data, "overview-kpi", (d) => d.prometheus_available);
  // §44.A: трафик KPI шапки = totals из throughput (сумма строк таблицы за
  // выбранный период, ClickHouse) → шапка сходится с таблицей. Очередь Kafka —
  // из kpiQ (мгновенный lag, только в Prometheus). Ярлык несёт выбранный период.
  // §86.4: в сквозном режиме шапка приезжает отдельным запросом и позже строк
  // (сумма по ВСЕМ узлам скоупа, а не по загруженным).
  const tot = allTeams ? totalsQ.data?.totals : thrData?.totals;
  // Пока дефолт команды не пришёл (§71), период в подписи ещё не тот — метку не
  // показываем: значения всё равно «—» (thrQ выключен), а мигание «24ч → 7д»
  // в заголовке KPI выглядело бы как смена данных.
  const kpiPeriod = periodReady ? periodLabel(period, t) : "";
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
        <Popover open={historyOpen && hasHistory} onOpenChange={setHistoryOpen}>
          <PopoverAnchor asChild>
            {/* onFocus/onClick на обёртке (React focus всплывает с инпута):
                onClick переоткрывает попап после pointerdown-дисмисса Radix —
                тот же приём, что в GlobalSearch. */}
            <div
              className="relative min-w-[200px] flex-1"
              onFocus={() => setHistoryOpen(true)}
              onClick={() => setHistoryOpen(true)}
            >
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
              <Input
                className="pl-9"
                placeholder={t("overview.search_placeholder")}
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    recordIfValid(searchInput);
                    setHistoryOpen(false);
                  }
                }}
                onBlur={() => {
                  if (pickingHistory.current) {
                    pickingHistory.current = false;
                    return;
                  }
                  recordIfValid(searchInput);
                }}
              />
            </div>
          </PopoverAnchor>
          <PopoverContent
            align="start"
            sideOffset={4}
            onOpenAutoFocus={(e) => e.preventDefault()}
            className="w-[--radix-popover-trigger-width] min-w-[260px] p-0"
          >
            <SearchHistoryList onPick={pickHistory} />
          </PopoverContent>
        </Popover>
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
        {/* §71: пока дефолт команды не пришёл, вместо переключателя — плейсхолдер
            той же высоты. Показать 24ч и переключить на настоящий дефолт нельзя:
            это и мигание, и лишний запрос метрик на каждый заход. */}
        {periodReady ? (
          <PeriodPicker value={period} onChange={(p) => updateFilters({ period: p })} />
        ) : (
          <div className="h-[30px] w-[320px] animate-pulse rounded-md bg-line/40" aria-hidden />
        )}
        {/* §44.B/§71: «под себя» — сохранить текущий период как дефолт ЭТОЙ
            команды (только пресет; произвольный диапазон дефолтом не имеет смысла).
            §86.7: в сквозном режиме кнопки нет вовсе — преф хранится НА КОМАНДУ,
            а команды здесь нет; сохранять было бы некуда, и «сохранить в текущую
            команду сессии» означало бы настроить не тот экран, что открыт. */}
        {periodReady && !allTeams && period.kind === "preset" && (
          <button
            type="button"
            onClick={() =>
              setDefaultPeriod.mutate({
                teamId,
                key: PREF_KEY_OVERVIEW_PERIOD,
                value: period,
              })
            }
            disabled={
              savedDefault.kind === "preset" && savedDefault.range === period.range
            }
            title={
              currentTeamName
                ? t("overview.set_default_period_hint", { team: currentTeamName })
                : t("overview.set_default_period")
            }
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
          <NodeTable
            nodes={nodes}
            throughput={throughput}
            period={period}
            ready={metricsReady}
            teamNames={teamNames}
            observe={allTeams ? visible.observe : null}
          />
        ) : (
          <NodeCards
            nodes={nodes}
            throughput={throughput}
            period={period}
            ready={metricsReady}
            teamNames={teamNames}
            observe={allTeams ? visible.observe : null}
          />
        ))}
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
  // §86.8/§84.7: неизвестный исход не красится в «ok» — соврать оператору
  // «всё в порядке» хуже, чем не сказать ничего.
  if (m.lastOutcome === "unknown") return "unknown";
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
  period,
  ready,
  teamNames,
  observe,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  period: Period;
  ready: boolean;
  // teamNames — карта id → имя команды; непуста только в сквозном режиме (§86.7).
  // Имя резолвит клиент по членствам: сервер выдачу не обогащает (§86.3).
  teamNames: Map<string, string> | null;
  // observe — ref-callback порционной загрузки (§86.4); в режиме одной команды
  // наблюдать нечего, метрики приезжают одним запросом.
  observe: ((nodeId: string) => (el: Element | null) => void) | null;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  const plabel = periodLabel(period, t);
  const navigate = useNavigate();
  return (
    <Card className="overflow-hidden p-0">
      <div className="overflow-x-auto">
      <table className="w-full min-w-[640px] text-[12.5px]">
        <thead>
          <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
            {teamNames && (
              <th className="px-3 py-2 font-medium">{t("overview.table.team")}</th>
            )}
            <th className="px-3 py-2 font-medium">{t("overview.table.path")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.method")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.in")} {plabel}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.out")} {plabel}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.errors")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.status")}</th>
            {/* Мини-график трафика — тот же Sparkline, что в карточках. Ширина
                фиксирована: бары растягиваются по ячейке (flex-1), без неё
                колонка съедала бы остаток строки. */}
            <th className="w-[140px] px-3 py-2 font-medium">{t("overview.table.traffic")}</th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((n) => {
            const m = throughput.get(n.id);
            const s = status(n, m, ready);
            return (
              <tr
                key={n.id}
                ref={observe?.(n.id)}
                className="border-b border-line last:border-0 hover:bg-bg-muted"
              >
                {teamNames && (
                  <td className="px-3 py-2.5">
                    <Chip tone="info">{teamNames.get(n.team_id) ?? "—"}</Chip>
                  </td>
                )}
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
                <td className="w-[140px] px-3 py-2.5">
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
  period,
  ready,
  teamNames,
  observe,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  period: Period;
  ready: boolean;
  teamNames: Map<string, string> | null;
  observe: ((nodeId: string) => (el: Element | null) => void) | null;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  const plabel = periodLabel(period, t);
  const navigate = useNavigate();
  return (
    <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3">
      {nodes.map((n) => {
        const m = throughput.get(n.id);
        const s = status(n, m, ready);
        const target =
          n.url_mode === "from_request" ? t("overview.card.dynamic_url") : n.target_url || "—";
        return (
          <Card
            key={n.id}
            ref={observe?.(n.id)}
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
              {/* §86.7: имя команды — в правый слот шапки карточки (он тут уже
                  был, justify-between с единственным ребёнком). Тот же
                  визуальный язык, что у результата глобального поиска §62. */}
              {teamNames && (
                <Chip tone="info" className="ml-2 shrink-0">
                  {teamNames.get(n.team_id) ?? "—"}
                </Chip>
              )}
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
            <div className="flex items-center gap-2 text-[11px] text-fg-subtle">
              <span className="truncate font-mono" title={target}>
                {target}
              </span>
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

