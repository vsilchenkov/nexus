import { type InfiniteData, type UseInfiniteQueryResult } from "@tanstack/react-query";
import { useRef, type RefObject } from "react";

import { api } from "../api/client";
import { type LogRow, type LogsResp } from "../components/node/types";
import { useInfiniteList } from "./useInfiniteList";

// useInfiniteLogs — постраничное чтение логов узла с keyset-курсором (§7.4/§44,
// §72.3). Один механизм на все списки логов: журнал узла (вкладка «Логи») и
// «Неудачные доставки» вкладки «Очередь», где до §72.3 стоял жёсткий limit=50
// вообще без пагинации — при 340 неудачах пользователь видел 50 и упирался.
//
// Курсор — пара (date_request, id) самой старой загруженной строки: `to` +
// `before_id`. Простого `to` мало, потому что date_request имеет секундную
// точность, и на «плотных» секундах страница возвращала те же записи → дедуп →
// стопор скролла (§44/45-fix).
//
// §91.3: общая механика (накопление страниц, дедуп, подгрузка у низа,
// схлопывание кеша) вынесена в useInfiniteList и переиспользуется журналом
// аудита. Здесь остались только специфика логов узла: адрес, форма курсора,
// признак доступности ClickHouse и замер длительности страницы.

export {
  MAX_INFINITE_ROWS,
  SCROLL_BOTTOM_THRESHOLD_PX,
  SCROLL_TOP_THRESHOLD_PX,
} from "./useInfiniteList";

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
  // params — query-параметры фильтра БЕЗ limit: размер страницы приходит
  // отдельным pageSize и добавляется здесь. Так его нельзя забыть — а без него
  // признак конца истории (items.length < limit) сравнивался бы с NaN, всегда
  // давал бы false, и список догружался бы до потолка на пустом месте.
  params: Record<string, string | number>;
  pageSize: number;
  enabled: boolean;
  refetchInterval?: number | false;
  containerRef: RefObject<HTMLElement | null>;
  maxRows?: number;
  collapseEnabled?: boolean;
  // onPageLoaded — длительность успешно загруженной страницы, мс (§77.3):
  // журнал логов по ней выключает автообновление на дорогих фильтрах (>2 с).
  onPageLoaded?: (ms: number) => void;
}): UseInfiniteLogsResult {
  const { nodeId, params, pageSize } = o;
  // Коллбэк в ref: он пересоздаётся каждый рендер и в deps queryFn не годится.
  const onPageLoadedRef = useRef(o.onPageLoaded);
  onPageLoadedRef.current = o.onPageLoaded;

  const list = useInfiniteList<LogRow, LogsResp, LogsCursor>({
    queryKey: o.queryKey,
    enabled: o.enabled,
    refetchInterval: o.refetchInterval,
    containerRef: o.containerRef,
    maxRows: o.maxRows,
    collapseEnabled: o.collapseEnabled,
    fetchPage: async (cursor, signal) => {
      const p: Record<string, string | number> = { ...params, limit: pageSize };
      if (cursor) {
        p.to = cursor.to;
        p.before_id = cursor.beforeId;
      }
      const t0 = performance.now();
      const resp = await api.get<LogsResp>(`/api/nodes/${nodeId}/logs`, p, { signal });
      onPageLoadedRef.current?.(performance.now() - t0);
      return resp;
    },
    getItems: (page) => page.items ?? [],
    getId: (row) => row.id,
    getCursor: (_page, items) => {
      // LIMIT в ClickHouse применяется ПОСЛЕ WHERE, поэтому недобор страницы —
      // это конец отфильтрованной истории, а не «фильтр выел строки».
      if (items.length < pageSize) return undefined;
      const oldest = items[items.length - 1]; // DESC → последний самый старый
      return oldest
        ? { to: new Date(oldest.date_request).getTime(), beforeId: oldest.id }
        : undefined;
    },
  });

  return {
    items: list.items,
    query: list.query,
    logsAvailable: list.query.data?.pages?.[0]?.logs_available !== false,
    atCap: list.atCap,
    onScroll: list.onScroll,
  };
}
