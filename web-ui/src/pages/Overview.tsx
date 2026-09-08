import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus, Play, Pause, RefreshCw, ChevronDown, ChevronRight } from "lucide-react";

import {
  api,
  type Node,
  type NodeGroup,
  type NodeRank,
  type OverviewKPI,
  type NodesThroughputResp,
  type NodesTotalsResp,
} from "../api/client";
import { useStableData } from "../lib/useStableData";
import {
  Button,
  Card,
  Chip,
  Kpi,
  KpiRow,
  ChartTooltip,
  DefaultPeriodButton,
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
  SearchInput,
} from "../components/ui";
import {
  applyFilters,
  GROUP_NONE,
  hasFilterParams,
  loadFilters,
  parseFilters,
  saveFilters,
  type MethodFilter,
  type OverviewFilters,
  type StatusFilter,
} from "../lib/overviewFilters";
import {
  groupNodes,
  hasGroupedSections,
  loadCollapsed,
  saveCollapsed,
  type NodeSection,
} from "../lib/nodeGroups";
import { cn } from "../lib/cn";
import {
  PREF_KEY_OVERVIEW_PERIOD,
  useMigrateLegacyPeriodPref,
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
  // Ошибки по тем же бакетам (§52-доп). Пустой ряд = разбивки нет: столбцы
  // рисуются одноцветными, а не «без ошибок».
  sparkErr: number[];
  // §52: исход последнего вызова узла (ok/degraded/down).
  // §86.8: "unknown" — исход неизвестен, потому что он ключуется ПУТЁМ
  // (nexus:node:last_error:<path> в Redis, метка node в Prometheus), а путь
  // уникален лишь внутри команды: у двух одноимённых узлов разных команд
  // значение общее и как минимум одному из них не принадлежит.
  lastOutcome: "ok" | "degraded" | "down" | "unknown";
};

// TOTALS_REFETCH_FACTOR — во сколько раз реже строк обновляется KPI-шапка
// сквозного режима (§86.4).
const TOTALS_REFETCH_FACTOR = 4;

// rankThroughput — метрика узла из строки среза шапки (§86.10).
//
// p95=0 и пустой спарклайн — это «ещё не загружено», а не «ноль»: карточка на
// нуле рисует «—» (fmtMs), пустой ряд даёт ту же заглушку, что у узла без
// метрик. Оба поля приезжают только с порционной пачкой и ложатся поверх.
function rankThroughput(r: NodeRank, path: string, ambiguous: Set<string> | null): Throughput {
  return {
    in: r.in,
    out: r.out,
    errors: r.errors,
    p95: 0,
    spark: [],
    sparkErr: [],
    // §86.8: исход ключуется путём, а путь уникален лишь внутри команды.
    lastOutcome: ambiguous?.has(path) ? "unknown" : r.last_outcome,
  };
}

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
  //
  // §92: в сквозном режиме спрашивается ГЛОБАЛЬНЫЙ преф (пустой teamId), а не
  // преф команды сессии. Экран показывает узлы всех команд, поэтому дефолт
  // одной из них здесь ничего не значит, — и ровно это значение пишет кнопка
  // «По умолчанию» в этом режиме: читаем то же, что сохраняем.
  const { value: teamDefault, settled: prefsSettled } = useTeamDefaultPeriod(
    allTeams ? "" : teamId,
  );
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
  const { search, method, status: statusFilter, group: groupFilter, period } = filters;

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

  // §86.4: статусы, которые выводятся ИЗ МЕТРИК. paused/disabled берутся из
  // конфигурации узла и метрик не требуют.
  const statusNeedsMetrics =
    statusFilter !== "all" && statusFilter !== "paused" && statusFilter !== "disabled";

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

  // §86.4: агрегат шапки в сквозном режиме считается отдельно и приезжает
  // позже строк — сумма по ВСЕМ узлам скоупа, а не по загруженным (иначе
  // значение шапки менялось бы от прокрутки).
  // §86.10: тем же ответом приходит срез по узлам, поэтому запрос объявлен ДО
  // порционной загрузки — та смотрит на срез, решая, нужен ли ей preload.
  const totalsQ = useQuery({
    queryKey: ["metrics-totals", scopeKey, periodKey(period)],
    queryFn: () =>
      api.get<NodesTotalsResp>("/api/metrics/totals", { ...periodParams(period), ...scopeQuery }),
    // Своё, более редкое автообновление (§86.4). Агрегат — полный проход по
    // ВСЕМ узлам скоупа, и гонять его в темпе строк незачем: серверный кеш
    // живёт секунды, а шапка за минуту не устаревает. Кратность интервалу
    // строк, а не отдельная константа: оператор регулирует темп одной
    // настройкой (§44.C), и связь между ними не теряется.
    refetchInterval: autoRefresh ? refetchMs * TOTALS_REFETCH_FACTOR : false,
    enabled: periodReady && allTeams,
  });

  // Кандидаты фильтра по статусу: узлы после фильтра по методу. Именно их
  // метрики нужны целиком — «какие узлы OK» нельзя ответить про непосчитанные.
  //
  // §86.10: при наличии среза preload НЕ НУЖЕН — статус всех узлов уже известен
  // из него. Это не микро-экономия: без гейта одно касание фильтра по статусу
  // добавляло в порционную загрузку ВЕСЬ список (на стенде — 82 узла сверх
  // видимых), и дальше он пересчитывался в ClickHouse каждые 12 секунд, даже
  // после сброса фильтра (пачки заморожены и не удаляются).
  //
  // Гейт оставлен, а не удалён: без среза (старый бэкенд, недоступный
  // ClickHouse) фильтр по статусу снова замкнулся бы в круг — отфильтрованные
  // строки не рендерятся, значит не становятся видимыми, значит их метрики не
  // запрашиваются, и список пуст навсегда.
  const statusCandidates = useMemo(() => {
    if (!allTeams || !statusNeedsMetrics) return undefined;
    // Ответа шапки ещё нет — ЖДЁМ, а не досылаем на всякий случай: пачка
    // замораживается в момент создания и потом не удаляется, поэтому один
    // преждевременный досыл остаётся в автообновлении навсегда.
    if (totalsQ.isPending) return undefined;
    if ((totalsQ.data?.nodes?.length ?? 0) > 0) return undefined;
    const items = nodesQ.data?.items ?? [];
    return (method ? items.filter((n) => n.root_method === method) : items).map((n) => n.id);
  }, [allTeams, statusNeedsMetrics, nodesQ.data, method, totalsQ.data, totalsQ.isPending]);

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
    preload: statusCandidates,
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
    // §86.10: в сквозном режиме БАЗОВЫЙ слой — срез из шапки, он есть на ВСЕ
    // узлы скоупа. Порционные пачки ложатся поверх: они свежее и несут p95 со
    // спарклайном.
    //
    // Слой один намеренно: порядок, бейдж и фильтр по статусу считает один и тот
    // же nodeVariant. Если бы ранг брался из среза, а бейдж — из пачек, узел
    // вставал бы наверх с серым «неизвестно», и верх списка читался бы как
    // случайный.
    //
    // p95=0 и пустой спарклайн — не «ноль», а «ещё не загружено»: карточка на
    // нуле рисует «—» (fmtMs), спарклайн от пустого массива рисует ту же
    // заглушку, что и у узла без метрик. Показанного не портим, недостающее не
    // выдумываем.
    if (allTeams) {
      const pathById = new Map((nodesQ.data?.items ?? []).map((n) => [n.id, n.path]));
      for (const r of totalsQ.data?.nodes ?? []) {
        m.set(r.node_id, rankThroughput(r, pathById.get(r.node_id) ?? "", ambiguousPaths));
      }
    }
    const src = allTeams ? Array.from(visible.items.values()) : (thrData?.items ?? []);
    for (const it of src) {
      if (!it.node_id) continue;
      m.set(it.node_id, {
        in: it.in,
        out: it.out,
        errors: it.errors,
        p95: it.p95_ms,
        spark: it.spark ?? [],
        sparkErr: it.spark_err ?? [],
        // Числа и спарклайн приходят из ClickHouse с фильтром по node_id и
        // коллизией путей не задеты — подменяется только исход (§86.8).
        lastOutcome: ambiguousPaths?.has(it.node) ? "unknown" : it.last_outcome,
      });
    }
    return m;
  }, [thrData, allTeams, visible.items, ambiguousPaths, totalsQ.data, nodesQ.data]);

  // Сортировка: проблемные первыми (err → degraded → warn → paused → ok →
  // disabled), внутри статуса — по убыванию входящего трафика (§22, ui_cards.html).
  const sortRank: Record<Variant, number> = useMemo(
    () => ({ err: 0, degraded: 1, warn: 2, paused: 3, ok: 4, unknown: 5, disabled: 6 }),
    [],
  );

  // metricsReady — метрики throughput реально пришли и Prometheus доступен.
  // Пока не готовы, статус узла показываем нейтральным «unknown», а не зелёным
  // «OK» (П11: статус мигал ОК→down при дозагрузке метрик).
  // В сквозном режиме источника два: срез шапки (все узлы разом, §86.10) и
  // порционные пачки. Готовность — по факту наличия метрик хоть откуда: срез
  // приходит одним ответом, пачки досчитываются по мере прокрутки, и ждать
  // вторые, когда пришёл первый, значит держать весь список серым без причины.
  const metricsReady = allTeams
    ? (totalsQ.data?.nodes?.length ?? 0) > 0 || visible.items.size > 0
    : thrQ.isSuccess && (thrData?.prometheus_available ?? false);

  // §86.10: порядок сквозного режима — «проблемные первыми», построенный ОДИН
  // раз и замороженный до смены периода или скоупа.
  //
  // Замораживается КАРТА «id узла → позиция», а не готовый массив: поиск, метод
  // и статус отфильтровывают ту же карту, поэтому взаимный порядок переживает
  // любой фильтр, а переключение «Таблица ↔ Карточки» не перетасовывает список
  // (вид в ключ заморозки не входит — данные те же, меняется только отрисовка).
  //
  // Почему не пересортировывать на каждом ответе: срез обновляется по
  // автообновлению, и живая сортировка переставляла бы строки под курсором —
  // ровно то, из-за чего в §86.4 от ранжирования отказались вовсе.
  //
  // Ранг считается СТРОГО из среза, а не из throughput: тот подмешивает
  // порционные пачки, и порядок стал бы зависеть от того, докуда успели
  // долистать. Из среза он зависит только от периода и скоупа.
  // rankEpoch — счётчик ручных обновлений (§86.11). Входит в ключ заморозки,
  // потому что «Обновить» — единственный способ перестроить порядок, не трогая
  // период и скоуп. Инкремент делается ПОСЛЕ прихода нового среза: сделай его
  // до — карта пересобралась бы по старым числам, а новые уже не пересобрали бы
  // её, ключ-то совпал.
  const [rankEpoch, setRankEpoch] = useState(0);
  // Поиск входит в ключ, потому что список узлов приходит УЖЕ суженным им
  // (`/api/nodes?search=`), а карта рангов строится по этому списку. Без него
  // открытие страницы по ссылке с поиском и последующий сброс оставляли бы
  // наверху горстку найденного, а всё остальное — алфавитом: в карте этих узлов
  // просто нет. Метод и статус фильтруют на клиенте и в ключ НЕ входят — в этом
  // и смысл заморозки карты, а не готового списка.
  const rankKey = `${scopeKey}|${periodKey(period)}|${search}|${rankEpoch}`;
  const frozenRank = useRef<{ key: string; order: Map<string, number> } | null>(null);
  const rankOrder = useMemo(() => {
    if (!allTeams) return null;
    const rows = totalsQ.data?.nodes;
    // Среза нет (старый бэкенд, недоступный ClickHouse) — порядок не строим:
    // экран обязан пережить это, а не остаться без списка.
    if (!rows || rows.length === 0) return null;
    // Список узлов ещё не приехал — НЕ морозим: иначе зафиксировалась бы пустая
    // (или частичная) карта, и подъехавшие узлы остались бы вне ранга навсегда,
    // ключ-то уже совпал. Оба входа обязаны быть на руках.
    const items = nodesQ.data?.items;
    if (!items || items.length === 0) return null;
    if (frozenRank.current?.key === rankKey) return frozenRank.current.order;
    const rowByID = new Map(rows.map((r) => [r.node_id, r]));
    const teamOf = (n: Node) => teamNames?.get(n.team_id) ?? "";
    const byTeamPath = (a: Node, b: Node) =>
      teamOf(a).localeCompare(teamOf(b)) || a.path.localeCompare(b.path);

    const ranked: Node[] = [];
    const rest: Node[] = [];
    for (const n of items) (rowByID.has(n.id) ? ranked : rest).push(n);
    ranked.sort((a, b) => {
      const ma = rankThroughput(rowByID.get(a.id)!, a.path, ambiguousPaths);
      const mb = rankThroughput(rowByID.get(b.id)!, b.path, ambiguousPaths);
      const va = nodeVariant(a, ma, true);
      const vb = nodeVariant(b, mb, true);
      if (sortRank[va] !== sortRank[vb]) return sortRank[va] - sortRank[vb];
      if (mb.in !== ma.in) return mb.in - ma.in;
      // Полная детерминированность: без этого равные узлы шевелились бы между
      // перестроениями, потому что порядок items приходит из репозитория.
      return byTeamPath(a, b);
    });
    // Узлы вне среза (созданы после его расчёта) — в конец: про них ещё ничего
    // не известно, и ставить их среди ранжированных значило бы соврать.
    rest.sort(byTeamPath);

    const order = new Map<string, number>();
    [...ranked, ...rest].forEach((n, i) => order.set(n.id, i));
    frozenRank.current = { key: rankKey, order };
    return order;
  }, [allTeams, totalsQ.data, nodesQ.data, rankKey, teamNames, ambiguousPaths, sortRank]);

  const nodes = useMemo(() => {
    let items = nodesQ.data?.items ?? [];
    if (method) items = items.filter((n) => n.root_method === method);
    // §99.5: фильтр по группе. GROUP_NONE — узлы без группы; узел со ссылкой на
    // исчезнувшую группу считается здесь сгруппированным (его id не пуст), но в
    // секции попадёт к «без группы» — расхождение осознанное: фильтр отвечает на
    // «что записано у узла», а раскладка — на «что показать».
    if (groupFilter === GROUP_NONE) items = items.filter((n) => !n.group_id);
    else if (groupFilter) items = items.filter((n) => n.group_id === groupFilter);
    if (statusFilter !== "all") {
      items = items.filter(
        (n) => nodeVariant(n, throughput.get(n.id), metricsReady) === statusFilter,
      );
    }
    // §86.10: в сквозном режиме порядок берётся из ЗАМОРОЖЕННОЙ карты рангов.
    //
    // До §86.10 здесь был стабильный порядок «команда, затем путь»: метрики
    // приезжали только порциями по мере прокрутки, и ранжировать было не по
    // чему — каждая пачка переставляла бы строки под курсором. Теперь ранг
    // известен сразу для всех узлов (срез приходит с шапкой), а от перестановок
    // под курсором защищает заморозка, а не отказ от сортировки.
    //
    // Прежний порядок остался деградацией: срез недоступен — список всё равно
    // осмысленно сгруппирован по командам и читается вместе с колонкой «Команда».
    if (allTeams) {
      const teamOf = (n: Node) => teamNames?.get(n.team_id) ?? "";
      const byTeamPath = (a: Node, b: Node) =>
        teamOf(a).localeCompare(teamOf(b)) || a.path.localeCompare(b.path);
      if (!rankOrder) return [...items].sort(byTeamPath);
      return [...items].sort((a, b) => {
        const ra = rankOrder.get(a.id);
        const rb = rankOrder.get(b.id);
        // Узел появился после заморозки — в конец, но детерминированно.
        if (ra === undefined || rb === undefined) {
          if (ra === rb) return byTeamPath(a, b);
          return ra === undefined ? 1 : -1;
        }
        return ra - rb;
      });
    }
    return [...items].sort((a, b) => {
      const va = nodeVariant(a, throughput.get(a.id), metricsReady);
      const vb = nodeVariant(b, throughput.get(b.id), metricsReady);
      if (sortRank[va] !== sortRank[vb]) return sortRank[va] - sortRank[vb];
      return (throughput.get(b.id)?.in ?? 0) - (throughput.get(a.id)?.in ?? 0);
    });
  }, [
    nodesQ.data,
    method,
    statusFilter,
    groupFilter,
    throughput,
    sortRank,
    metricsReady,
    allTeams,
    teamNames,
    rankOrder,
  ]);

  // §99: справочник групп. Ключ ["node-groups",""] общий с комбобоксом формы
  // узла и вкладкой «Конфиг» — один запрос на приложение, а не по одному на
  // экран. Справочник глобальный, скоуп команды в ключ не входит (§99.9).
  const groupsQ = useQuery({
    queryKey: ["node-groups", ""],
    queryFn: () => api.get<{ items: NodeGroup[] }>("/api/node-groups", { q: "", limit: 200 }),
    staleTime: 60_000,
  });
  const groups = useMemo(() => groupsQ.data?.items ?? [], [groupsQ.data]);

  // Свёрнутые группы переживают перезапуск браузера (localStorage, §99.5):
  // это настройка рабочего места, а не состояние сиюминутной задачи.
  const [collapsed, setCollapsed] = useState<Set<string>>(() => loadCollapsed());
  const toggleGroup = useCallback((id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      saveCollapsed(next);
      return next;
    });
  }, []);

  // Секции: группировка НЕ сортирует, а стабильно разбивает уже отсортированный
  // список (§99.9) — поэтому «проблемные первыми» действует внутри группы, а
  // заморозка порядка §86.10 продолжает работать без изменений.
  const sections = useMemo(() => groupNodes(nodes, groups), [nodes, groups]);
  // Групп нет вовсе (или ни один видимый узел не сгруппирован) → плоский список,
  // как до §99: одинокая безымянная секция добавила бы разметку ни о чём.
  const grouped = hasGroupedSections(sections);
  // Фильтр ссылается на группу, которой в справочнике уже нет. Справочник
  // должен быть ЗАГРУЖЕН — пока он едет, любой id «неизвестен», и подставлять
  // пункт «группа удалена» было бы враньём в другую сторону.
  const unknownGroupFilter =
    groupsQ.isSuccess &&
    groupFilter !== "" &&
    groupFilter !== GROUP_NONE &&
    !groups.some((g) => g.id === groupFilter);

  // statusFilterPending — фильтр по статусу выбран, но метрики, из которых
  // статус выводится, ещё не пришли. В сквозном режиме это окно длится до
  // прихода среза (§86.10), а без него — пока догружаются пачки preload; в
  // режиме одной команды — пока идёт общий запрос.
  const statusFilterPending = statusNeedsMetrics && !metricsReady;

  // §86.11: ручное обновление. Нужно и при выключенном автообновлении (там оно
  // единственный способ обновиться), и при включённом — чтобы не ждать интервал.
  //
  // Перезапрашиваем ИМЕННО текущие запросы, а не инвалидируем экран целиком:
  // лишний проход по всем узлам скоупа здесь стоит дорого.
  const [refreshing, setRefreshing] = useState(false);
  const refreshAll = useCallback(async () => {
    setRefreshing(true);
    try {
      await Promise.all([
        nodesQ.refetch(),
        kpiQ.refetch(),
        allTeams ? totalsQ.refetch() : thrQ.refetch(),
        allTeams ? visible.refresh() : Promise.resolve(),
      ]);
      // Порядок перестраивается ПОСЛЕ того, как свежий срез уже в кеше (см.
      // rankEpoch). В режиме одной команды перестраивать нечего: там сортировка
      // и так живая, на каждом ответе.
      if (allTeams) setRankEpoch((e) => e + 1);
    } finally {
      setRefreshing(false);
    }
  }, [allTeams, nodesQ, kpiQ, totalsQ, thrQ, visible]);

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
              <SearchInput
                // Escape здесь закрывает список истории поиска, а search-поле
                // Chrome по Escape очищает само — тип оставлен текстовым.
                type="text"
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
        {/* §99.5: фильтр по группе. Показывается, только когда группы вообще
            заведены: пустой селект с единственным «Все группы» занимал бы место
            в шапке, ничего не давая. */}
        {groups.length > 0 && (
          <Select
            className="w-44"
            value={groupFilter}
            onChange={(e) => updateFilters({ group: e.target.value })}
            aria-label={t("overview.filter.group")}
          >
            <option value="">{t("overview.filter.all_groups")}</option>
            <option value={GROUP_NONE}>{t("overview.filter.no_group")}</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
            {/* Фильтр указывает на группу, которой в справочнике нет (пришли по
                ссылке, группу удалили в соседней вкладке). Без этой опции
                браузер показал бы первую — «Все группы», — тогда как фильтр
                продолжает действовать и список пуст: экран читался бы как
                сломанный. Пункт называет причину честно. */}
            {unknownGroupFilter && (
              <option value={groupFilter}>{t("overview.filter.group_gone")}</option>
            )}
          </Select>
        )}
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
        {/* §44.B/§71: «под себя» — сохранить текущий период как дефолт (только
            пресет; произвольный диапазон дефолтом не имеет смысла).
            §92: в сквозном режиме кнопка тоже есть, но пишет ГЛОБАЛЬНЫЙ преф
            (teamId=""): команды здесь нет, а глобальное значение и есть ответ на
            вопрос «с чего начинать везде, где своё не задано». Прежде кнопки в
            этом режиме не было вовсе (§86.7), и настроить сквозной экран было
            нечем. */}
        {periodReady && (
          <DefaultPeriodButton
            period={period}
            savedDefault={savedDefault}
            teamId={allTeams ? "" : teamId}
            prefKey={PREF_KEY_OVERVIEW_PERIOD}
            title={
              allTeams
                ? t("overview.set_default_period_all_hint")
                : currentTeamName
                  ? t("overview.set_default_period_hint", { team: currentTeamName })
                  : t("overview.set_default_period")
            }
          />
        )}
        <div className="ml-auto flex items-center gap-2">
          {/* §86.11: ручное обновление — только иконка, подпись в title и
              aria-label. Стоит ЛЕВЕЕ «Авто»: сначала действие, затем режим.
              Состояние «Авто» не трогает: пауза остаётся паузой. */}
          <button
            type="button"
            onClick={refreshAll}
            disabled={refreshing}
            title={t("overview.refresh")}
            aria-label={t("overview.refresh")}
            className="inline-flex items-center rounded-md border border-line px-2 py-1 text-xs text-fg-muted hover:text-accent disabled:cursor-default disabled:opacity-50"
          >
            <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
          </button>
          {/* §44.C: пауза/запуск автообновления рабочего стола. */}
          <button
            type="button"
            onClick={() => setAutoRefresh((v) => !v)}
            title={autoRefresh ? t("overview.autorefresh_on") : t("overview.autorefresh_off")}
            className="inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted hover:text-accent"
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
      </div>

      {nodesQ.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {nodesQ.error && <div className="text-err">{(nodesQ.error as Error).message}</div>}
      {/* §86.4: пока метрики фильтруемых узлов не досчитаны, «Узлов пока нет» —
          неправда: статус выводится ИЗ метрик, и до их прихода под фильтр не
          попадает никто. Показываем загрузку, иначе оператор решит, что узлов
          с таким статусом не существует. */}
      {nodesQ.data && nodes.length === 0 && statusFilterPending && (
        <div className="text-fg-muted">{t("common.loading")}</div>
      )}
      {nodesQ.data && nodes.length === 0 && !statusFilterPending && (
        <div className="text-fg-muted">{t("overview.empty")}</div>
      )}

      {nodes.length > 0 &&
        (view === "table" ? (
          <NodeTable
            sections={sections}
            collapsed={collapsed}
            onToggleGroup={toggleGroup}
            throughput={throughput}
            period={period}
            ready={metricsReady}
            teamNames={teamNames}
            observe={allTeams ? visible.observe : null}
          />
        ) : (
          <NodeCards
            sections={sections}
            grouped={grouped}
            collapsed={collapsed}
            onToggleGroup={toggleGroup}
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
  sections,
  collapsed,
  onToggleGroup,
  throughput,
  period,
  ready,
  teamNames,
  observe,
}: {
  // sections — узлы, разложенные по группам (§99.5). Секция без группы всегда
  // первая и заголовка не имеет.
  sections: NodeSection[];
  collapsed: Set<string>;
  onToggleGroup: (id: string) => void;
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
  // colSpan заголовка группы обязан совпадать с числом колонок, иначе строка
  // схлопнется в первую ячейку. Колонок семь, плюс «Команда» в сквозном режиме.
  // Флаг «есть ли группы» таблице не нужен: заголовок рисуется у секции с
  // группой, а без групп секция ровно одна и безымянная — разметка та же.
  const colSpan = teamNames ? 8 : 7;
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
        {/* Секция группы — отдельный <tbody> в ОДНОЙ таблице, а не своя таблица
            на группу: у отдельных таблиц ширина колонок считается независимо, и
            столбцы разъехались бы между секциями (§99.5). */}
        {sections.map((sec) => {
          const isCollapsed = sec.group !== null && collapsed.has(sec.group.id);
          return (
        <tbody key={sec.group?.id ?? "__ungrouped__"}>
          {sec.group && (
            <tr>
              <td colSpan={colSpan} className="border-y border-line bg-bg-muted px-3 py-1.5">
                {/* Кнопка, а не ссылка: заголовок ничего не открывает, а
                    переключает состояние — урок §79.3 здесь работает в обратную
                    сторону. */}
                <button
                  type="button"
                  onClick={() => onToggleGroup(sec.group!.id)}
                  aria-expanded={!isCollapsed}
                  className="flex w-full items-center gap-2 text-left"
                >
                  {isCollapsed ? (
                    <ChevronRight className="h-4 w-4 shrink-0 text-fg-subtle" />
                  ) : (
                    <ChevronDown className="h-4 w-4 shrink-0 text-fg-subtle" />
                  )}
                  <span className="truncate text-[12.5px] font-semibold">{sec.group.name}</span>
                  {/* Счётчик считает ВИДИМЫЕ узлы, после фильтров: иначе «4 узла»
                      рядом с одной строкой читалось бы как поломка фильтра. */}
                  <span className="ml-auto shrink-0 text-[11px] text-fg-subtle">
                    {t("overview.group.count", { count: sec.nodes.length })}
                  </span>
                </button>
              </td>
            </tr>
          )}
          {!isCollapsed &&
            sec.nodes.map((n) => {
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
                    errors={m?.sparkErr}
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
          );
        })}
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
  sections,
  grouped,
  collapsed,
  onToggleGroup,
  throughput,
  period,
  ready,
  teamNames,
  observe,
}: {
  sections: NodeSection[];
  grouped: boolean;
  collapsed: Set<string>;
  onToggleGroup: (id: string) => void;
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
  // Без групп — прежняя одна сетка на весь список (§99.5): заголовок секции с
  // пустым именем добавил бы разметку ни о чём.
  const grid = "grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3";
  return (
    <div className={grouped ? "space-y-3" : undefined}>
      {sections.map((sec) => {
        const isCollapsed = sec.group !== null && collapsed.has(sec.group.id);
        return (
          <div key={sec.group?.id ?? "__ungrouped__"}>
            {sec.group && (
              <button
                type="button"
                onClick={() => onToggleGroup(sec.group!.id)}
                aria-expanded={!isCollapsed}
                className="mb-2.5 mt-1 flex w-full items-center gap-2 text-left"
              >
                {isCollapsed ? (
                  <ChevronRight className="h-4 w-4 shrink-0 text-fg-subtle" />
                ) : (
                  <ChevronDown className="h-4 w-4 shrink-0 text-fg-subtle" />
                )}
                <span className="truncate text-[12.5px] font-semibold">{sec.group.name}</span>
                <span className="shrink-0 text-[11px] text-fg-subtle">
                  {t("overview.group.count", { count: sec.nodes.length })}
                </span>
                <span className="ml-2 h-px flex-1 bg-line" aria-hidden />
              </button>
            )}
            {!isCollapsed && (
              <div className={grid}>
                {sec.nodes.map((n) => {
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
              errors={m?.sparkErr}
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
            )}
          </div>
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

// SPARK_ERR_COLOR — цвет маркера «Ошибки» в тултипе спарклайна. Совпадает с
// C_ERR графика узла (TrafficChart): один и тот же смысл обязан выглядеть
// одинаково на карточке и на странице узла.
const SPARK_ERR_COLOR = "#e85d5c";

// Sparkline — мини-график входящего трафика за период (§22, ui_cards.html).
// На каждом столбце единый тултип (§33): окно бакета + число входящих запросов.
// Radix-тултип монтирует контент лениво на hover (провайдер общий в AppShell) —
// на Overview много карточек, но накладные минимальны.
// onOpenLogs (§47.4) — клик по столбцу спарклайна открывает логи узла за окно
// этого бакета (как клик по графику на странице узла). Задаётся только для
// узлов с таблицей логов; иначе бары некликабельны.
function Sparkline({
  data,
  errors,
  variant,
  period,
  onOpenLogs,
}: {
  data: number[];
  errors?: number[];
  variant: Variant;
  period: Period;
  onOpenLogs?: (range: { from: number; to: number }) => void;
}) {
  const { t } = useTranslation();
  if (data.length === 0) {
    return <div className="h-7" />;
  }
  const max = Math.max(1, ...data);
  // Цвет столбцов НЕ зависит от статуса узла (§52-доп). Раньше весь спарклайн
  // красился по variant, и узел со статусом down выглядел так, будто не прошёл
  // ни один запрос, — при 42 ошибках из 489. Статус остаётся в бейдже и в
  // левой полосе карточки, а график показывает данные: доля ошибок в столбце.
  //
  // Исключение — состояния, где данных нет или узел выключен: там приглушённый
  // цвет по-прежнему честен.
  const muted = variant === "disabled" || variant === "paused" || variant === "unknown";
  const baseColor = muted ? "bg-fg-subtle" : "bg-accent";
  const { since, until } = periodWindow(period);
  const bucketW = (until - since) / data.length;
  const multiDay = until - since > 86_400_000;
  return (
    <div className="flex h-7 items-end gap-px">
      {data.map((v, i) => {
        const start = since + i * bucketW;
        // Доля ошибок столбца — как в TrafficChart на странице узла: клампим в
        // [0, 100] (errors может превысить count при дублях записей) и даём
        // красному сегменту минимальную видимую высоту, иначе одна ошибка на
        // тысячу схлопывается в ноль и столбец врёт «всё чисто».
        const errCount = muted ? 0 : (errors?.[i] ?? 0);
        const errShare = v > 0 && errCount > 0 ? Math.min(100, (errCount / v) * 100) : 0;
        const errPct = errShare > 0 ? Math.max(12, errShare) : 0;
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
                series={
                  errCount > 0
                    ? [
                        {
                          marker: "bar",
                          // Тот же hex, что у маркеров TrafficChart (§33):
                          // маркеры тултипа рисуются инлайн-стилем, CSS-токен
                          // сюда не доезжает.
                          color: SPARK_ERR_COLOR,
                          name: t("metrics.tooltip.errors"),
                          value: fmtNum(Math.round(errCount)),
                        },
                      ]
                    : undefined
                }
              />
            }
          >
            <span
              className={cn(
                "flex flex-1 flex-col justify-end overflow-hidden rounded-sm opacity-80",
                baseColor,
                onOpenLogs && "cursor-pointer",
              )}
              style={{ height: `${Math.max(4, (v / max) * 100)}%` }}
              onClick={
                onOpenLogs ? () => onOpenLogs({ from: start, to: start + bucketW }) : undefined
              }
            >
              {errPct > 0 && (
                <span className="w-full bg-err" style={{ height: `${errPct}%` }} aria-hidden />
              )}
            </span>
          </Tooltip>
        );
      })}
    </div>
  );
}

