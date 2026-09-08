import { useMemo } from "react";
import { useSearchParams } from "react-router-dom";

import { type LogsInitialFilter } from "../components/node/LogsTab";
import { type NodeTab, withFailedLogs, withNodeTab } from "./nodeTabUrl";

// Адреса переходов между вкладками узла — для <Link>, а не для обработчика.
//
// Раньше переходы жили колбэками в NodeDetail (setSearchParams(withNodeTab(…))),
// а дочерние компоненты рисовали <button>, стилизованный под ссылку. Адрес при
// этом существовал, но открыть его в новой вкладке было нечем — тот же дефект,
// что §79.3 нашёл у вкладок узла и §89.3 у списка команд. Ссылке адрес нужен на
// рендере, поэтому его строит сам компонент, а колбэки уходят.
//
// nodeTabUrl остаётся чистым модулем без роутера (его тесты про разбор адреса,
// а не про React) — этот хук лишь связывает его с текущими параметрами.

export type NodeTabLinks = {
  // tab — переключение вкладки: /nodes/:id?tab=…
  tab: (t: NodeTab) => { search: string };
  // failedLogs — «Очередь → Логи» с окном и фильтром недоставленных (§35).
  failedLogs: (f: LogsInitialFilter) => { search: string };
};

/**
 * useNodeTabTo — построитель адресов вкладок текущего узла.
 *
 * Только search: pathname у вкладок общий, а чужие ключи адреса обязаны
 * выживать (правило §54) — их приносит текущий URLSearchParams.
 *
 * Момент вычисления смещается с клика на рендер. Значимо это ровно там, где
 * адрес зависит от «сейчас» (окно периода в «Открыть в логах»): href обновится
 * с ближайшей перерисовкой, а вкладка «Очередь» перерисовывается по
 * 15-секундному поллингу. Если поллинг оттуда когда-нибудь уберут, окно в
 * ссылке замрёт — и это придётся решать явно.
 */
export function useNodeTabTo(): NodeTabLinks {
  const [params] = useSearchParams();
  return useMemo(
    () => ({
      tab: (t: NodeTab) => ({ search: `?${withNodeTab(params, t)}` }),
      failedLogs: (f: LogsInitialFilter) => ({ search: `?${withFailedLogs(params, f)}` }),
    }),
    [params],
  );
}
