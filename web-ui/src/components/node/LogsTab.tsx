import { Link } from "react-router-dom";
import { useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { RefreshCw, Settings, RotateCcw, ChevronRight, ChevronDown, Download } from "lucide-react";
import { Fragment, useEffect, useMemo, useRef, useState } from "react";

import { api, type Node } from "../../api/client";
import { useRoleAtLeast } from "../../lib/useCurrentRole";
import { fmtLogTs, fmtSize } from "../../lib/format";
import { FETCH_CHUNK, LARGE_WARN_RUNES, formatRunes, prettyMaybe } from "../../lib/logBody";
import {
  advFormEqual,
  emptyAdvForm,
  isLogOK,
  logsFilterParams,
  logsFilterSearch,
  type LogsAdvForm,
  type LogsDoneFilter,
  type LogsFilterState,
} from "../../lib/logsQuery";
import {
  MAX_INFINITE_ROWS,
  SCROLL_TOP_THRESHOLD_PX,
  useInfiniteLogs,
} from "../../lib/useInfiniteLogs";
import { Popover, PopoverAnchor, PopoverContent } from "../ui";
import { CopyButton } from "../ui/CopyButton";
import { ReplayDialog } from "../ReplayDialog";
import { LogAckBlock } from "./LogAckBlock";
import { LogsAdvancedFilters } from "./LogsAdvancedFilters";
import { type LogRow, type LogsResp, type LogDetail, type LogBodyChunk } from "./types";

type StatusFilter = "all" | "ok" | "err";
type PageSize = 50 | 100 | 200;

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
// §77.3: через сколько идущий запрос списка признаётся долгим и показывается
// строка «Поиск… [Отменить]». Порог заметно больше обычной страницы (десятки-
// сотни мс с автоокном §77.2), чтобы индикатор не мигал на каждом фильтре.
const SLOW_SEARCH_HINT_MS = 700;
// §77.3: длительность УСПЕШНОГО запроса, после которой автообновление для этого
// набора фильтров выключается (полнотекст по всей истории — до десятков секунд).
const SLOW_FILTER_MS = 2000;
// §77.1: период tail-poll — «что появилось после самой свежей загруженной строки».
const TAIL_POLL_MS = 5000;
// §77.1: запас к нижней границе tail-poll. date_request секундной точности,
// поэтому граница берётся на секунду раньше, а повторы отсекает дедуп по id.
const TAIL_POLL_OVERLAP_MS = 1000;
// Сколько ошибок SSE подряд терпим, прежде чем признать поток мёртвым.
// Между ними браузер сам переподключается (нативный retry EventSource).
const LIVE_MAX_CONSECUTIVE_ERRORS = 5;

// useSizeUnits — локализованные единицы размера тела (§42-доп): «Б|КБ|…» из
// i18n-ключа logs.size_units, сплит по «|» для fmtSize.
function useSizeUnits(): string[] {
  const { t } = useTranslation();
  return useMemo(() => t("logs.size_units").split("|"), [t]);
}

// LogsTab — вкладка «Логи» (§7.4): snapshot + SSE live-tail с буфером,
// клиентскими фильтрами, расширенным поиском и replay-меню строки.
export function LogsTab({ node, initialFilter }: { node: Node; initialFilter?: LogsInitialFilter }) {
  const { t } = useTranslation();
  const sizeUnits = useSizeUnits();
  const id = node.id;
  const hasLogsTable = !!node.clickhouse_table;

  const [pageSize, setPageSize] = useState<PageSize>(50);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>(initialFilter?.status ?? "all");
  const [doneFilter, setDoneFilter] = useState<LogsDoneFilter>(
    initialFilter?.done === "no" ? "pending" : initialFilter?.done === "yes" ? "done" : "all",
  );

  // Стартовый временной фильтр из клика по графику (§33.4). LogsTab монтируется
  // заново при переключении на вкладку, поэтому инициализация через useState ок.
  // §48: поля ip/host убраны из UI (API их по-прежнему принимает); добавлены
  // method и режимы поиска qCase/qWord/qRegex (кнопки Aa / ab| / .*).
  // §67: clientHost — фильтр по PTR-имени клиента (колонка client_host).
  // §79.4: форма панели и её сравнение переехали в LogsAdvancedFilters — панель
  // теперь общая с вкладкой «Метрики», а два экземпляра одной разметки неминуемо
  // разъехались бы.
  const initForm: LogsAdvForm = {
    ...emptyAdvForm,
    from: initialFilter?.from ?? "",
    to: initialFilter?.to ?? "",
  };
  const [showAdv, setShowAdv] = useState(!!(initialFilter?.from || initialFilter?.to));
  const [advForm, setAdvForm] = useState(initForm);
  const [appliedFilters, setAppliedFilters] = useState(initForm);

  // §77.3: кнопки «Применить» нет — фильтры применяются по завершении ввода
  // (Enter/blur у поиска; сразу — у комбобоксов, дат и тумблеров режимов).
  // Коммит идемпотентен: одинаковый черновик не перезапускает поиск — иначе
  // Enter+blur давали бы двойной запуск, а blur о кнопку «Сбросить» — лишний
  // запрос перед сбросом.
  const commitAdv = (next: LogsAdvForm) => {
    setAdvForm(next);
    setAppliedFilters((prev) => (advFormEqual(prev, next) ? prev : next));
  };

  // §48.4: min/max дат из логов узла — лениво при фокусе поля даты, каждый раз
  // заново (страница может жить долго, логи прибывают). Ошибку фетча глотаем —
  // атрибуты просто не выставляются.
  const [dateRange, setDateRange] = useState<{ min: number; max: number } | null>(null);
  const dateRangeInFlight = useRef(false);
  const fetchDateRange = () => {
    if (dateRangeInFlight.current) return;
    dateRangeInFlight.current = true;
    api
      .get<{ min_ms: number; max_ms: number }>(`/api/nodes/${id}/logs/date-range`)
      .then((r) => setDateRange({ min: r.min_ms, max: r.max_ms }))
      .catch(() => undefined)
      .finally(() => {
        dateRangeInFlight.current = false;
      });
  };

  // §72.2: ОДИН набор параметров фильтра на список, счётчик и SSE-стрим —
  // включая быстрые status/done, которые раньше применялись в браузере к уже
  // загруженной странице (отсюда «Показано 3 из 33» и мёртвый скролл).
  const filterState: LogsFilterState = useMemo(
    () => ({ ...appliedFilters, status: statusFilter, done: doneFilter }),
    [appliedFilters, statusFilter, doneFilter],
  );
  const filterParams = useMemo(() => logsFilterParams(filterState), [filterState]);

  const [live, setLive] = useState(false);
  // «У верха» скролл-контейнера. Управляет авто-рефетчем (см. logsQ ниже):
  // вверху — обновляем свежие записи каждые 5с; прокрутил вниз (листает историю)
  // — замораживаем; вернулся вверх — снова размораживаем.
  const [atTop, setAtTop] = useState(true);

  const tableWrapRef = useRef<HTMLDivElement | null>(null);
  // «Пользователь у самого верха» без ре-рендера — им пользуются и Live-эффект,
  // и tail-poll (§77.1), поэтому ref объявлен выше обоих.
  const autoScrollRef = useRef(true);

  // Раскрытая строка: тела request/response грузятся лениво только для неё
  // (GET /api/nodes/:id/log/:logId). Список этих данных не содержит — иначе
  // сотни строк с большими JSON-телами вешают фронт (§7.4.1). Объявлена ДО
  // useInfiniteLogs: пока строка раскрыта, хук не схлопывает страницы (§77.1).
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // §77.3: отмена долгого поиска и автопауза обновления на дорогих фильтрах.
  const qc = useQueryClient();
  // Пользователь нажал «Отменить»: запросы списка/счётчика оборваны (настоящий
  // AbortSignal — см. api.get) и не перезапускаются, пока не нажат «Повторить»
  // или не сменился фильтр.
  const [searchCancelled, setSearchCancelled] = useState(false);
  // Текущий запрос списка идёт дольше порога — показать «Поиск… [Отменить]».
  const [slowSearch, setSlowSearch] = useState(false);
  // Последний успешный запрос списка шёл дольше SLOW_FILTER_MS: автообновление
  // для этого фильтра выключено (тик каждые 5 с накладывал бы дорогие запросы
  // друг на друга).
  const [slowFilter, setSlowFilter] = useState(false);

  // Бесконечный скролл (§7.4): первая страница — последние pageSize записей
  // (ORDER BY date_request DESC), скролл вниз подгружает следующие pageSize
  // более старых через keyset-курсор (§72.3: общий хук с вкладкой «Очередь»).
  const logsKey = useMemo(
    // §72.2: status/done входят в ключ — смена быстрого фильтра перезапрашивает
    // список с первой страницы. Раньше ключ их не содержал, список оставался
    // прежним, и фильтр лишь прятал строки уже загруженной страницы.
    () => ["logs", id, filterParams, pageSize] as const,
    [id, filterParams, pageSize],
  );
  const countKey = useMemo(() => ["logs-count", id, filterParams] as const, [id, filterParams]);

  const logs = useInfiniteLogs({
    nodeId: id,
    queryKey: logsKey,
    params: filterParams,
    pageSize,
    // §77.3: после «Отменить» запрос не перезапускается сам — только по
    // «Повторить» или смене фильтра.
    enabled: !!id && hasLogsTable && !live && !searchCancelled, // в Live snapshot не нужен — читаем SSE-буфер
    // §77.1: авто-рефетча всего списка больше нет — свежие записи приезжают
    // tail-poll'ом и вставляются сверху, поэтому раскрытая строка не исчезает.
    refetchInterval: false,
    containerRef: tableWrapRef,
    // §77.1: пока строка раскрыта, «возврат к верху» не выбрасывает страницы 2+
    // (вместе с ними исчезала бы и раскрытая строка).
    collapseEnabled: expandedId === null,
    onPageLoaded: (ms) => setSlowFilter(ms > SLOW_FILTER_MS),
  });
  const logsQ = logs.query;
  const infiniteItems = logs.items;
  // §48: 400 от List = некорректный синтаксис/regex в поле «Поиск».
  const badQuery =
    (logsQ.error as { response?: { status?: number } } | null)?.response?.status === 400;

  // §67: точный count() под ТЕМИ ЖЕ фильтрами, что и список — счётчик
  // «Показано N из M» в шапке. Отдельный запрос: таблица рендерится сразу, «из
  // M» дорисовывается. Пересчёт — только со снапшотом, НЕ на каждое SSE-событие;
  // в Live выключен (total мгновенно устаревает — счётчик скрыт).
  // Ошибка/таймаут/CH недоступен → logs_available=false → «из M» просто нет.
  //
  // §72.2: параметры — тот же filterParams, что у списка (без limit: count не
  // постраничный). Раньше счётчик собирал их сам и добавлял status/done, а
  // список нет — два запроса считали разные множества.
  const countQ = useQuery({
    queryKey: countKey,
    enabled: !!id && hasLogsTable && !live && !searchCancelled,
    queryFn: ({ signal }) =>
      api.get<{ total: number; logs_available?: boolean }>(
        `/api/nodes/${id}/logs/count`,
        filterParams,
        { signal },
      ),
    // §77.3: на дорогом фильтре автообновление счётчика выключается вместе с
    // tail-poll'ом — иначе тик каждые 5 с накладывает тяжёлые count() друг на друга.
    refetchInterval: !live && atTop && !slowFilter ? 5_000 : false,
    retry: false, // деградация штатная — не долбим CH повторами
  });
  const totalCount =
    countQ.data && countQ.data.logs_available !== false ? countQ.data.total : null;

  // §77.3: индикатор «Поиск…» появляется только у ЗАТЯНУВШЕГОСЯ запроса —
  // обычная страница (десятки-сотни мс с автоокном §77.2) его не показывает,
  // иначе он мигал бы на каждом изменении фильтра.
  const searchPending = logsQ.isFetching && !logsQ.isFetchingNextPage;
  useEffect(() => {
    if (!searchPending) {
      setSlowSearch(false);
      return;
    }
    const t = window.setTimeout(() => setSlowSearch(true), SLOW_SEARCH_HINT_MS);
    return () => window.clearTimeout(t);
  }, [searchPending]);

  // §77.3: смена фильтра снимает «отменён» и оценку дороговизны — новый набор
  // фильтров судится заново (и заодно это тот самый «сброс текущего поиска»:
  // react-query отменит запрос прежнего ключа через AbortSignal).
  useEffect(() => {
    setSearchCancelled(false);
    setSlowFilter(false);
  }, [filterParams, pageSize]);

  // §77.3: отмена поиска. cancelQueries рвёт HTTP-запросы обоих ключей — а с
  // ними, через разрыв соединения и отмену ctx хендлера, и сами запросы в
  // ClickHouse (иначе дорогой полнотекст продолжал бы выполняться там ещё
  // десятки секунд).
  const cancelSearch = () => {
    setSearchCancelled(true);
    setSlowSearch(false);
    void qc.cancelQueries({ queryKey: logsKey });
    void qc.cancelQueries({ queryKey: countKey });
  };
  const retrySearch = () => {
    setSearchCancelled(false);
    setSlowFilter(false);
  };

  const [liveLogs, setLiveLogs] = useState<LogRow[]>([]);
  const [highlighted, setHighlighted] = useState<Set<string>>(new Set());
  // Сколько live-записей пришло, пока пользователь смотрел не на верх списка
  // (пилюля «показать новые»), и длина буфера на прошлом тике — объявлены здесь,
  // выше SSE-эффекта: он сбрасывает и то, и другое при (пере)подключении.
  const [pendingCount, setPendingCount] = useState(0);
  const lastLiveLenRef = useRef(0);
  // Поток умер окончательно (LIVE_MAX_CONSECUTIVE_ERRORS ошибок подряд) —
  // live выключен, показываем предупреждение вместо вечного спиннера.
  const [liveLost, setLiveLost] = useState(false);
  // ClickHouse временно недоступен в live-режиме (SSE-событие logs_unavailable).
  const [liveUnavailable, setLiveUnavailable] = useState(false);
  // Snapshot-запрос вернул logs_available=false (CH недоступен): показываем
  // мягкий индикатор «логи временно недоступны», а не пустой список/спиннер.
  const logsUnavailable = !logs.logsAvailable;

  // §77.1: tail-poll — инкрементальное обновление списка вместо авто-рефетча.
  //
  // Раньше список обновлялся `refetchInterval` у useInfiniteQuery: каждые 5 с
  // перезапрашивались ВСЕ накопленные страницы, react-query подменял данные,
  // граница первой страницы уезжала приехавшими записями — и строка, чьё тело
  // читал пользователь, выпадала из склейки. Теперь раз в 5 с тянутся только
  // записи НОВЕЕ самой свежей загруженной и вставляются сверху первой страницы,
  // а существующие элементы не пересоздаются: раскрытое тело остаётся открытым.
  //
  // Верхняя граница «Дата по» в прошлом означает историческое окно — новых
  // записей в нём не появится, поллить нечего.
  const newestTs = infiniteItems.length ? infiniteItems[0].date_request : null;
  const historicalWindow = !!appliedFilters.to;
  const tailEnabled =
    !!id &&
    hasLogsTable &&
    !live &&
    !searchCancelled &&
    !slowFilter &&
    atTop &&
    !historicalWindow &&
    newestTs !== null &&
    logsQ.isSuccess;
  const tailQ = useQuery({
    // newestTs в ключе нет намеренно: он меняется на каждую новую запись, а
    // ключ обязан быть стабильным — иначе react-query на каждом тике заводил бы
    // новый кеш-энтри и интервал сбрасывался бы.
    queryKey: ["logs-tail", id, filterParams, pageSize],
    enabled: tailEnabled,
    refetchInterval: TAIL_POLL_MS,
    retry: false,
    queryFn: ({ signal }) => {
      const since = new Date(newestTs as string).getTime() - TAIL_POLL_OVERLAP_MS;
      return api.get<LogsResp>(
        `/api/nodes/${id}/logs`,
        { ...filterParams, from: new Date(since).toISOString(), limit: pageSize },
        { signal },
      );
    },
  });

  // Вливание хвоста в первую страницу инфинит-кеша (§77.1). Дедуп по id:
  // граница окна взята с запасом в секунду, поэтому пересечение с уже
  // загруженным ожидаемо.
  const tailData = tailQ.data;
  useEffect(() => {
    const fresh = tailData?.items;
    if (!fresh?.length) return;
    // Хвост пришёл полной страницей — между ним и первой страницей возможна
    // дыра (за тик появилось больше записей, чем limit). Склеивать нельзя:
    // список молча потерял бы записи. Честно перечитываем с первой страницы.
    if (fresh.length >= pageSize) {
      void qc.invalidateQueries({ queryKey: logsKey });
      return;
    }
    let addedCount = 0;
    qc.setQueryData<InfiniteData<LogsResp, unknown>>(logsKey, (d) => {
      if (!d?.pages.length) return d;
      const known = new Set(d.pages.flatMap((p) => (p.items ?? []).map((r) => r.id)));
      const added = fresh.filter((r) => !known.has(r.id));
      if (!added.length) return d;
      addedCount = added.length;
      const [first, ...rest] = d.pages;
      return {
        ...d,
        pages: [{ ...first, items: [...added, ...(first.items ?? [])] }, ...rest],
      };
    });
    if (!addedCount) return;
    // Пользователь у верха — новые записи и так перед глазами; иначе копим
    // счётчик для пилюли «N новых записей ↑» (та же, что в Live).
    if (!autoScrollRef.current) setPendingCount((p) => p + addedCount);
    // logsKey/pageSize стабильны между тиками; реагируем именно на новые данные.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tailData]);

  // Таймеры снятия подсветки: чистим при unmount/перезапуске потока, иначе
  // setState стреляет по размонтированному компоненту.
  const highlightTimersRef = useRef<Set<number>>(new Set());

  // §72.2: URL стрима строится из общего filterState — быстрые status/done
  // фильтрует сервер (matchLogFilter), как и snapshot. Строка стабильна между
  // рендерами, поэтому годится в deps эффекта.
  const streamSearch = useMemo(() => logsFilterSearch(filterState), [filterState]);

  useEffect(() => {
    if (!live || !id || !hasLogsTable) return;
    // Буфер прошлого фильтра больше не дочищается на клиенте (клиентского
    // фильтра нет) — очищаем его сами, иначе после смены фильтра в Live висели
    // бы записи, ему не соответствующие.
    setLiveLogs([]);
    setHighlighted(new Set());
    setPendingCount(0);
    lastLiveLenRef.current = 0;
    const es = new EventSource(`/api/nodes/${id}/logs/stream${streamSearch}`);
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
    // streamSearch — строка со всеми применёнными фильтрами (§48/§67/§72.2):
    // её смена пересоздаёт поток, одинаковое значение — нет.
  }, [live, id, hasLogsTable, streamSearch]);

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
    // Подгрузка вниз — только не в Live (Live добавляет записи сверху); в Live
    // хук всё равно выключен через enabled.
    if (!live) logs.onScroll();
  };

  useEffect(() => {
    if (!live) {
      lastLiveLenRef.current = 0;
      // pendingCount здесь НЕ сбрасывается: вне Live им владеет tail-poll
      // (§77.1). Обнуление живёт в onScroll/scrollToTop — «пользователь
      // добрался до верха и увидел новые записи».
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

  // §72.2: строки таблицы — ровно то, что вернул сервер под текущими фильтрами (см. ниже).
  // Клиентского пост-фильтра больше нет: он резал уже загруженную страницу, из-за
  // чего «Показано N» считалось по горстке видимых строк, а «из M» — по всей
  // таблице, и скролл дальше не шёл.
  const visibleLogs = live ? liveLogs : infiniteItems;

  // id + HTTP-глагол строки: ReplayDialog по глаголу решает, требуется ли тело
  // (GET — без тела), и зеркалит выбор метода реинъекции бэкенда (ANY-узлы).
  const [replay, setReplay] = useState<{ id: string; httpMethod?: string } | null>(null);
  // §7.4.1/§58: replay пере-отправляет запрос на внешнюю цель (сайд-эффект) —
  // manager+. viewer видит кнопку disabled с tooltip «Нет прав» (бэкенд тоже
  // отдаёт 403). Скрывать не будем — так понятно, что действие существует.
  const canReplay = useRoleAtLeast("manager");

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
            {/* §67: «Показано N из M» — M только вне Live и когда count доступен. */}
            {!live && totalCount !== null
              ? t("logs.shown_of_total", {
                  n: visibleLogs.length,
                  total: totalCount.toLocaleString(),
                })
              : t("logs.shown_count", { n: visibleLogs.length })}
          </span>
          {/* §77.4: дип-линк из «Очереди» приходит с done=no. Своего
              переключателя у этого фильтра больше нет, поэтому он обязан быть
              виден и сниматься — иначе список молча показывает подмножество. */}
          {doneFilter !== "all" && (
            <button
              type="button"
              onClick={() => setDoneFilter("all")}
              title={t("logs.advanced.reset")}
              className="inline-flex items-center gap-1 rounded-full bg-accent/15 px-2 py-0.5 text-xs text-accent hover:bg-accent/25"
            >
              {doneFilter === "done" ? t("logs.filter.done_yes") : t("logs.filter.done_no")}
              <span aria-hidden>✕</span>
            </button>
          )}
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
          {/* §77.4: сегмента «Все|Завершено|В работе» здесь больше нет — он был
              доказанным дублем «OK|Ошибки» (write-path пишет done = 2xx, поэтому
              ok ≡ done=yes; замер на 17 боевых узлах — пересечения нулевые), а
              комбинация «В работе»+«OK» была заведомо пустой. Параметр done в API
              сохранён: на нём дип-линк вкладки «Очередь» — он показывается чипом
              рядом со счётчиком (см. шапку выше). */}
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
        <LogsAdvancedFilters
          nodeId={id}
          draft={advForm}
          applied={appliedFilters}
          onDraft={setAdvForm}
          onCommit={commitAdv}
          badQuery={badQuery}
          dateRange={dateRange}
          onOpenDate={fetchDateRange}
        />
      )}

      {/* §77.3: затянувшийся поиск — с возможностью прервать его. Отмена рвёт
          запрос и в браузере, и в ClickHouse (AbortSignal → разрыв соединения →
          отмена ctx хендлера), поэтому пользователь не заперт на десятки секунд
          и может уточнить фильтры. */}
      {slowSearch && !searchCancelled && (
        <div className="flex flex-wrap items-center gap-2 border-b border-line bg-bg-muted/40 px-4 py-2 text-xs text-fg-muted">
          <RefreshCw className="h-3 w-3 animate-spin text-accent" />
          <span>{t("logs.search.running")}</span>
          {appliedFilters.q && !appliedFilters.from && !appliedFilters.to && (
            <span className="text-warn">{t("logs.search.full_history_hint")}</span>
          )}
          <button
            onClick={cancelSearch}
            className="ml-auto rounded-md bg-bg-muted px-2.5 py-1 hover:bg-bg-3 hover:text-fg"
          >
            {t("logs.search.cancel")}
          </button>
        </div>
      )}
      {searchCancelled && (
        <div className="flex flex-wrap items-center gap-2 border-b border-warn/30 bg-warn/10 px-4 py-2 text-xs text-warn">
          <span>{t("logs.search.cancelled")}</span>
          <button
            onClick={retrySearch}
            className="ml-auto rounded-md bg-bg-muted px-2.5 py-1 text-fg-muted hover:bg-bg-3 hover:text-fg"
          >
            {t("logs.search.retry")}
          </button>
        </div>
      )}
      {/* §77.3: автообновление выключено, потому что запрос дорогой — иначе тик
          каждые 5 с накладывал бы такие запросы друг на друга. */}
      {slowFilter && !live && !searchCancelled && (
        <div className="flex flex-wrap items-center gap-2 border-b border-line bg-bg-muted/40 px-4 py-2 text-xs text-fg-muted">
          <span>{t("logs.autorefresh.paused_slow")}</span>
          <button
            onClick={() => {
              void logsQ.refetch();
              void countQ.refetch();
            }}
            className="ml-auto rounded-md bg-bg-muted px-2.5 py-1 hover:bg-bg-3 hover:text-fg"
          >
            {t("logs.viewer.refresh")}
          </button>
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
          {/*
            table-fixed + colgroup, а не auto-layout: иначе таблица требует ширину
            по содержимому (URL резервировал max-w 28rem, «Время» — nowrap-штамп),
            сумма перерастает контейнер, и он даёт горизонтальный скролл —
            overflow-y-auto по CSS-спеке вычисляет overflow-x в auto. Служебные
            колонки фиксированы по px, «Метод» и «URL» делят остаток и обрезаются
            (полное значение — в title, см. ячейки ниже).
          */}
          <table className="w-full table-fixed text-sm">
            <colgroup>
              <col className="w-[164px]" />
              <col className="w-[76px]" />
              <col />
              <col />
              <col className="w-[76px]" />
              <col className="w-[68px]" />
              <col className="w-[84px]" />
              <col className="w-[48px]" />
            </colgroup>
            <thead className="sticky top-0 z-10 bg-bg-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 text-left">{t("logs.col.time")}</th>
                <th className="px-2 py-2 text-left">{t("logs.col.http_method")}</th>
                <th className="px-3 py-2 text-left">{t("logs.col.method")}</th>
                <th className="px-3 py-2 text-left">{t("logs.col.url")}</th>
                <th className="px-2 py-2 text-right">{t("logs.col.status")}</th>
                <th className="px-2 py-2 text-right">{t("logs.col.ms")}</th>
                {/* §42-доп: размер тела ответа; короткий заголовок, полное имя в тултипе */}
                <th className="px-2 py-2 text-right" title={t("logs.col.resp_size_title")}>
                  {t("logs.col.resp_size")}
                </th>
                <th className="px-2 py-2" />
              </tr>
            </thead>
            <tbody>
              {visibleLogs.map((r) => {
                const isHl = highlighted.has(r.id);
                const isErr = !isLogOK(r);
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
                      <td className="truncate px-2 py-2">{r.http_method}</td>
                      {/* Метод (подпуть §39) и URL делят остаток ширины и обрезаются —
                          полное значение отдаём в title, иначе оно нечитаемо. */}
                      <td
                        className="truncate px-3 py-2 font-mono text-xs text-fg-muted"
                        title={r.method}
                      >
                        {r.method}
                      </td>
                      {/* URL обрезается по ширине колонки, поэтому полное значение
                          живёт в подсказке — вместе с кнопкой «Скопировать»
                          (нативный title скопировать не давал). */}
                      <td className="truncate px-3 py-2 font-mono text-xs text-fg-muted">
                        <LogUrlCell url={r.url} />
                      </td>
                      <td className={`px-2 py-2 text-right ${isErr ? "text-err" : "text-ok"}`}>
                        {r.status}
                      </td>
                      <td className="px-2 py-2 text-right">{r.duration_ms}</td>
                      {/* §42-доп: размер тела ответа; «—» = тела нет (0 байт) */}
                      <td className="whitespace-nowrap px-2 py-2 text-right font-mono text-xs text-fg-muted">
                        {fmtSize(r.response_size ?? 0, sizeUnits)}
                      </td>
                      <td className="px-2 py-2 text-right">
                        <button
                          type="button"
                          disabled={!canReplay}
                          title={canReplay ? t("node.actions.replay") : t("common.no_permission")}
                          onClick={(e) => {
                            e.stopPropagation();
                            setReplay({ id: r.id, httpMethod: r.http_method });
                          }}
                          className={`text-fg-muted ${
                            canReplay ? "hover:text-accent" : "cursor-not-allowed opacity-40"
                          }`}
                        >
                          <RotateCcw className="h-4 w-4" />
                        </button>
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="border-t border-line bg-bg-muted/30">
                        <td colSpan={8} className="px-3 py-3">
                          <LogBodies node={node} logId={r.id} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
              {visibleLogs.length === 0 && (
                <tr>
                  <td colSpan={8} className="px-3 py-6 text-center text-fg-muted">
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
                  <td colSpan={8} className="px-3 py-4 text-center text-xs text-fg-muted">
                    {t("common.loading")}
                  </td>
                </tr>
              )}
              {!live && !logsQ.hasNextPage && visibleLogs.length > 0 && (
                <tr>
                  <td colSpan={8} className="px-3 py-3 text-center text-[11px] text-fg-muted">
                    {t("logs.no_more")}
                  </td>
                </tr>
              )}
              {/* §44: достигнут потолок строк — дальше не подгружаем (защита от
                  зависания на узлах с сотнями тысяч логов), просим сузить поиск. */}
              {!live && logsQ.hasNextPage && logs.atCap && (
                <tr>
                  <td colSpan={8} className="px-3 py-3 text-center text-[11px] text-warn">
                    {t("logs.cap_reached", { n: MAX_INFINITE_ROWS })}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* §77.1: пилюля работает и в snapshot — tail-poll добавляет записи
          сверху, пока пользователь читает историю или раскрытое тело. */}
      {pendingCount > 0 && (
        <button
          onClick={scrollToTop}
          className="absolute bottom-6 left-1/2 z-20 -translate-x-1/2 rounded-full bg-accent px-3 py-1.5 text-xs text-white shadow-lg hover:bg-accent-hover"
        >
          {t("logs.new_records", { n: pendingCount })} ↑
        </button>
      )}

      {replay && (
        <ReplayDialog
          logId={replay.id}
          nodeId={node.id}
          httpMethod={replay.httpMethod}
          incomingMethod={node.incoming_method}
          onClose={() => setReplay(null)}
        />
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

// Задержка закрытия подсказки URL: столько миллисекунд курсор может находиться
// в зазоре между ячейкой и всплывающим блоком, не закрывая его.
const URL_POPOVER_CLOSE_DELAY_MS = 250;

// LogUrlCell — ячейка колонки «URL». Значение обрезается по ширине колонки, а
// полный адрес показывается во всплывающем блоке с кнопкой «Скопировать»
// (нативный title копировать не давал).
//
// Почему Popover, а не Tooltip: строки таблицы плотные, и у КАЖДОЙ своя ячейка
// URL. Radix Tooltip закрывается, едва курсор покидает триггер, а по дороге к
// кнопке он проходит над соседней строкой — та перехватывает hover, подсказка
// закрывается, и клик приходится уже по строке (проверено на стенде: запись
// раскрывалась, буфер оставался пустым). Popover живёт своей жизнью: соседи его
// не закрывают, а уход курсора гасит его с задержкой — успеть перевести мышь.
function LogUrlCell({ url }: { url: string }) {
  const [open, setOpen] = useState(false);
  const closeTimer = useRef<number | null>(null);

  const cancelClose = () => {
    if (closeTimer.current !== null) {
      window.clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  };
  const scheduleClose = () => {
    cancelClose();
    closeTimer.current = window.setTimeout(() => setOpen(false), URL_POPOVER_CLOSE_DELAY_MS);
  };
  // Размонтирование по ходу подгрузки/фильтрации не должно оставлять таймер.
  useEffect(() => cancelClose, []);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      {/* Anchor, а не Trigger: у Trigger свой onClick, который перехватил бы
          клик по строке (раскрытие записи). Открываем только по наведению. */}
      <PopoverAnchor asChild>
        <span
          className="block truncate"
          onMouseEnter={() => {
            cancelClose();
            setOpen(true);
          }}
          onMouseLeave={scheduleClose}
        >
          {url}
        </span>
      </PopoverAnchor>
      <PopoverContent
        side="top"
        align="start"
        sideOffset={4}
        // Открытие по hover не должно уводить фокус с таблицы, а закрытие —
        // возвращать его на ячейку (иначе прыгает скролл длинного списка).
        onOpenAutoFocus={(e) => e.preventDefault()}
        onCloseAutoFocus={(e) => e.preventDefault()}
        onMouseEnter={cancelClose}
        onMouseLeave={scheduleClose}
        // Radix рендерит контент в портал, но React-события всплывают по
        // React-дереву — без этого клик по кнопке дошёл бы до onClick строки.
        onClick={(e) => e.stopPropagation()}
        className="flex max-w-[420px] items-start gap-2 px-2.5 py-2"
      >
        <span className="min-w-0 break-all font-mono text-[11px]">{url}</span>
        <CopyButton value={url} />
      </PopoverContent>
    </Popover>
  );
}

// LogBodies — ленивая подгрузка тел request/response одной записи (§7.4.1/§42).
// Монтируется только при раскрытии строки. Get отдаёт ПРЕВЬЮ тел (первые ~64K
// рун) + полные длины — большое тело не грузится разом и не вешает фронт;
// остаток тянется по кнопке «Показать весь» или скачивается файлом.
function LogBodies({ node, logId }: { node: Node; logId: string }) {
  const nodeId = node.id;
  const { t } = useTranslation();
  const sizeUnits = useSizeUnits();
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
  // §47.2: параметры запроса (query) — показываем блок только если непусты,
  // иначе поле не занимает место. Рендер как у тела (prettyMaybe + копирование);
  // приходят в detail-ответе целиком, отдельная подгрузка чанками не нужна.
  const parameters = q.data.parameters ?? "";
  const showParams = parameters.trim().length > 0;
  // §42-доп: истинный размер тела (байты) в скобках рядом с заголовком панели —
  // «Запрос (12.0 КБ)». При 0 (нет тела / legacy-запись) скобки не показываем.
  const sizedTitle = (key: string, size: number) =>
    t(key) + (size > 0 ? ` (${fmtSize(size, sizeUnits)})` : "");
  return (
    <div className="space-y-3">
      {showReason && (
        <div className="rounded border border-warn/30 bg-warn/10 px-2 py-1.5 text-[11px] text-warn">
          <span className="font-medium">{t("logs.detail.reason")}: </span>
          <span className="break-all font-mono">{reason}</span>
        </div>
      )}
      {showParams && (
        <div className="min-w-0">
          <div className="mb-1 flex items-center justify-between gap-2">
            <span className="text-[10px] uppercase tracking-wider text-fg-muted">
              {t("logs.detail.parameters")}
            </span>
            <CopyButton value={parameters} />
          </div>
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted/50 p-2 font-mono text-[11px]">
            {prettyMaybe(parameters)}
          </pre>
        </div>
      )}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <LogBodyBlock
          nodeId={nodeId}
          logId={logId}
          which="request"
          title={sizedTitle("logs.detail.request", q.data.request_size ?? 0)}
          preview={q.data.request ?? ""}
          total={q.data.request_len ?? 0}
        />
        <LogBodyBlock
          nodeId={nodeId}
          logId={logId}
          which="response"
          title={sizedTitle("logs.detail.response", q.data.response_size ?? 0)}
          preview={q.data.response ?? ""}
          total={q.data.response_len ?? 0}
        />
      </div>
      {/* §84.9: чем шина ответила БЫ по текущему шаблону. Блока нет вовсе у
          узла без изменения ответа и у записи, прошедшей синхронным путём. */}
      <LogAckBlock node={node} logId={logId} recordType={q.data.type} />
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
