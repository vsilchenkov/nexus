import {
  useInfiniteQuery,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, type RefObject } from "react";

import { api } from "../api/client";
import { type LogRow, type LogsResp } from "../components/node/types";

// useInfiniteLogs — постраничное чтение логов узла с keyset-курсором (§7.4/§44,
// §72.3). Один механизм на все списки логов: журнал узла (вкладка «Логи») и
// «Неудачные доставки» вкладки «Очередь», где до §72.3 стоял жёсткий limit=50
// вообще без пагинации — при 340 неудачах пользователь видел 50 и упирался.
//
// Курсор — пара (date_request, id) самой старой загруженной строки: `to` +
// `before_id`. Простого `to` мало, потому что date_request имеет секундную
// точность, и на «плотных» секундах страница возвращала те же записи → дедуп →
// стопор скролла (§44/45-fix).

// MAX_INFINITE_ROWS — потолок накопленных строк (§44.K). Без виртуализации
// неограниченный append раздувает DOM и вешает прокрутку; достигнут потолок —
// подгрузка останавливается, а список показывает подсказку сузить период.
export const MAX_INFINITE_ROWS = 1000;

// SCROLL_BOTTOM_THRESHOLD_PX — насколько близко к низу надо прокрутить, чтобы
// пошла подгрузка следующей страницы.
export const SCROLL_BOTTOM_THRESHOLD_PX = 200;

// LogsCursor — keyset-курсор: время самой старой загруженной строки (мс) и её
// id как тай-брейкер.
type LogsCursor = { to: number; beforeId: string };

export type UseInfiniteLogsResult = {
  // items — плоский список всех подгруженных страниц с дедупом по id.
  items: LogRow[];
  query: UseInfiniteQueryResult<InfiniteData<LogsResp, unknown>, unknown>;
  // logsAvailable — false, когда ClickHouse недоступен (бэкенд деградирует
  // мягко, отдавая 200 + logs_available=false вместо 500).
  logsAvailable: boolean;
  // atCap — накоплен потолок MAX_INFINITE_ROWS и подгрузка остановлена.
  atCap: boolean;
  // onScroll — обработчик прокрутки контейнера: подгружает следующую страницу,
  // когда пользователь у низа. Вешается на элемент containerRef напрямую либо
  // вызывается из собственного обработчика (журнал логов делает второе — у него
  // на прокрутке висит ещё и слежение за «у верха»).
  onScroll: () => void;
};

export function useInfiniteLogs(o: {
  nodeId: string;
  // queryKey — ПОЛНЫЙ ключ кеша. Все параметры фильтра обязаны в него входить,
  // иначе смена фильтра отдаст прежние страницы из кеша (§72.2).
  queryKey: readonly unknown[];
  // params — query-параметры запроса, включая limit (размер страницы).
  params: Record<string, string | number>;
  enabled: boolean;
  refetchInterval?: number | false;
  // containerRef — скролл-контейнер списка: по нему считается близость к низу и
  // отслеживается «страница короче контейнера» (тогда скроллить нечего и
  // следующую страницу тянем сами).
  containerRef: RefObject<HTMLElement | null>;
  maxRows?: number;
}): UseInfiniteLogsResult {
  const { nodeId, queryKey, params, enabled, refetchInterval = false, containerRef } = o;
  const maxRows = o.maxRows ?? MAX_INFINITE_ROWS;
  const limit = Number(params.limit);

  const query = useInfiniteQuery({
    queryKey,
    enabled,
    initialPageParam: null as LogsCursor | null,
    queryFn: ({ pageParam }) => {
      const p: Record<string, string | number> = { ...params };
      if (pageParam) {
        p.to = pageParam.to;
        p.before_id = pageParam.beforeId;
      }
      return api.get<LogsResp>(`/api/nodes/${nodeId}/logs`, p);
    },
    getNextPageParam: (lastPage) => {
      const items = lastPage.items ?? [];
      // LIMIT в ClickHouse применяется ПОСЛЕ WHERE, поэтому недобор страницы —
      // это конец отфильтрованной истории, а не «фильтр выел строки».
      if (items.length < limit) return undefined;
      const oldest = items[items.length - 1]; // DESC → последний самый старый
      return oldest
        ? { to: new Date(oldest.date_request).getTime(), beforeId: oldest.id }
        : undefined;
    },
    refetchInterval,
    // §48: 4xx (невалидный поисковый запрос → 400) не ретраим — покажем ошибку
    // сразу; глобальная политика ретраит всё, кроме 401/403.
    retry: (failureCount, error: unknown) => {
      const status = (error as { response?: { status?: number } })?.response?.status;
      if (status && status >= 400 && status < 500) return false;
      return failureCount < 2;
    },
  });

  // Дедуп по id: курсорная граница включительна, поэтому самая старая запись
  // страницы может прийти повторно первой записью следующей.
  const items = useMemo(() => {
    const pages = query.data?.pages ?? [];
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
  }, [query.data]);

  const atCap = items.length >= maxRows;
  const canFetchMore = query.hasNextPage && !query.isFetchingNextPage && !atCap;

  const onScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el || !canFetchMore) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_BOTTOM_THRESHOLD_PX) {
      query.fetchNextPage();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, canFetchMore, query.fetchNextPage]);

  // Догрузка при недоборе высоты: контента меньше высоты контейнера (скроллбара
  // нет) — доскроллить нельзя, тянем следующую страницу сами.
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !canFetchMore) return;
    if (el.scrollHeight <= el.clientHeight) query.fetchNextPage();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length, canFetchMore]);

  return {
    items,
    query,
    logsAvailable: query.data?.pages?.[0]?.logs_available !== false,
    atCap,
    onScroll,
  };
}
