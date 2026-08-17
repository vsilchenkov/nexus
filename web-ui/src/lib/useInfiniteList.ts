import {
  useInfiniteQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, type RefObject } from "react";

// useInfiniteList — общий механизм списка с подгрузкой по скроллу и
// keyset-курсором. Извлечён из useInfiniteLogs (§91.3), когда тот же паттерн
// понадобился журналу аудита: накопление страниц с дедупом, подгрузка у низа,
// догрузка при недоборе высоты, схлопывание кеша при возврате к верху.
//
// Ядро ничего не знает про конкретный API: чем является страница, как из неё
// достать строки и как построить курсор — решает вызыватель. Логи и аудит
// живут на разных эндпоинтах с разной формой курсора, но ведут себя одинаково.

// MAX_INFINITE_ROWS — потолок накопленных строк (§44.K). Без виртуализации
// неограниченный append раздувает DOM и вешает прокрутку; достигнут потолок —
// подгрузка останавливается, а список показывает подсказку сузить период.
export const MAX_INFINITE_ROWS = 1000;

// SCROLL_BOTTOM_THRESHOLD_PX — насколько близко к низу надо прокрутить, чтобы
// пошла подгрузка следующей страницы.
export const SCROLL_BOTTOM_THRESHOLD_PX = 200;

// SCROLL_TOP_THRESHOLD_PX — «пользователь у верха списка»: смотрит свежие
// записи, а не листает историю.
export const SCROLL_TOP_THRESHOLD_PX = 8;

export type UseInfiniteListResult<TRow, TResp> = {
  // items — плоский список всех подгруженных страниц с дедупом по id.
  items: TRow[];
  query: UseInfiniteQueryResult<InfiniteData<TResp, unknown>, unknown>;
  // atCap — накоплен потолок maxRows и подгрузка остановлена.
  atCap: boolean;
  // onScroll — обработчик прокрутки контейнера: подгружает следующую страницу,
  // когда пользователь у низа. Вешается на элемент containerRef напрямую либо
  // вызывается из собственного обработчика (журнал логов делает второе — у него
  // на прокрутке висит ещё и слежение за «у верха»).
  onScroll: () => void;
};

export function useInfiniteList<TRow, TResp, TCursor>(o: {
  // queryKey — ПОЛНЫЙ ключ кеша. Все параметры фильтра обязаны в него входить,
  // иначе смена фильтра отдаст прежние страницы из кеша (§72.2).
  queryKey: readonly unknown[];
  enabled: boolean;
  // fetchPage — загрузка одной страницы. signal обязателен для настоящей
  // отмены: без него «Отменить» рвёт запрос только в react-query, а сервер
  // продолжает его выполнять (§77.3).
  fetchPage: (cursor: TCursor | null, signal: AbortSignal | undefined) => Promise<TResp>;
  getItems: (page: TResp) => TRow[];
  getId: (row: TRow) => string;
  // getCursor — курсор на следующую страницу; undefined означает конец истории.
  // Обычно это «недобор страницы» плюс ключ последней строки.
  getCursor: (page: TResp, items: TRow[]) => TCursor | undefined;
  refetchInterval?: number | false;
  // containerRef — скролл-контейнер списка: по нему считается близость к низу и
  // отслеживается «страница короче контейнера» (тогда скроллить нечего и
  // следующую страницу тянем сами).
  containerRef: RefObject<HTMLElement | null>;
  maxRows?: number;
  // collapseEnabled — разрешение «возврату к верху» схлопывать кеш до первой
  // страницы (§72.5). Журнал логов выключает его, пока раскрыта строка (§77.1).
  collapseEnabled?: boolean;
}): UseInfiniteListResult<TRow, TResp> {
  const { queryKey, enabled, refetchInterval = false, containerRef } = o;
  const collapseEnabled = o.collapseEnabled ?? true;
  const maxRows = o.maxRows ?? MAX_INFINITE_ROWS;
  const qc = useQueryClient();
  // Коллбэки и ключ в ref: они пересоздаются каждый рендер и в deps не годятся.
  const keyRef = useRef(queryKey);
  keyRef.current = queryKey;
  const fetchPageRef = useRef(o.fetchPage);
  fetchPageRef.current = o.fetchPage;
  const getItemsRef = useRef(o.getItems);
  getItemsRef.current = o.getItems;
  const getIdRef = useRef(o.getId);
  getIdRef.current = o.getId;
  const getCursorRef = useRef(o.getCursor);
  getCursorRef.current = o.getCursor;

  const query = useInfiniteQuery({
    queryKey,
    enabled,
    initialPageParam: null as TCursor | null,
    // pageParam приходит как unknown: react-query выводит его тип из
    // getNextPageParam, а тот у нас generic. initialPageParam задаёт настоящий
    // тип, поэтому приведение здесь безопасно.
    queryFn: ({ pageParam, signal }) => fetchPageRef.current(pageParam as TCursor | null, signal),
    getNextPageParam: (lastPage: TResp) =>
      getCursorRef.current(lastPage, getItemsRef.current(lastPage)),
    refetchInterval,
    // §48: 4xx (невалидный поисковый запрос → 400) не ретраим — покажем ошибку
    // сразу; глобальная политика ретраит всё, кроме 401/403.
    retry: (failureCount, error: unknown) => {
      const status = (error as { response?: { status?: number } })?.response?.status;
      if (status && status >= 400 && status < 500) return false;
      return failureCount < 2;
    },
  });

  // Дедуп по id: курсорная граница может быть включительной, поэтому самая
  // старая запись страницы способна прийти повторно первой записью следующей.
  const items = useMemo(() => {
    const pages = query.data?.pages ?? [];
    const seen = new Set<string>();
    const out: TRow[] = [];
    for (const p of pages) {
      for (const r of getItemsRef.current(p)) {
        const id = getIdRef.current(r);
        if (seen.has(id)) continue;
        seen.add(id);
        out.push(r);
      }
    }
    return out;
  }, [query.data]);

  const atCap = items.length >= maxRows;
  const canFetchMore = query.hasNextPage && !query.isFetchingNextPage && !atCap;
  const pageCount = query.data?.pages.length ?? 0;

  // Вернулись к верху — схлопываем кеш до первой страницы (§72.5).
  //
  // useInfiniteQuery при авто-рефетче перезапрашивает ВСЕ накопленные страницы.
  // Пользователь, пролиставший список до потолка (20 страниц) и вернувшийся
  // наверх, получал бы 20 запросов каждые несколько секунд — на одну открытую
  // вкладку. Наверху нужны только свежие записи; вниз страницы догрузятся
  // обычным путём.
  //
  // Штатный maxPages здесь не подходит: он выбрасывает ПЕРВЫЕ страницы при
  // fetchNextPage и ломает накопительный список (§44.K).
  const collapseToFirstPage = useCallback(() => {
    qc.setQueryData(keyRef.current, (d: InfiniteData<TResp, unknown> | undefined) =>
      d && d.pages.length > 1
        ? { pages: d.pages.slice(0, 1), pageParams: d.pageParams.slice(0, 1) }
        : d,
    );
  }, [qc]);

  const onScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    if (el.scrollTop <= SCROLL_TOP_THRESHOLD_PX) {
      // Гейт по числу страниц обязателен: событий скролла десятки в секунду, а
      // setQueryData уведомляет подписчиков даже когда данные не изменились —
      // без него каждое движение у верха перерисовывало бы весь список.
      // §77.1: при раскрытой строке схлопывание отложено (collapseEnabled).
      if (pageCount > 1 && collapseEnabled) collapseToFirstPage();
      return;
    }
    if (!canFetchMore) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_BOTTOM_THRESHOLD_PX) {
      query.fetchNextPage();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, canFetchMore, pageCount, collapseEnabled, collapseToFirstPage, query.fetchNextPage]);

  // Догрузка при недоборе высоты: контента меньше высоты контейнера (скроллбара
  // нет) — доскроллить нельзя, тянем следующую страницу сами.
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !canFetchMore) return;
    if (el.scrollHeight <= el.clientHeight) query.fetchNextPage();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length, canFetchMore]);

  return { items, query, atCap, onScroll };
}
