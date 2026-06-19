import { useRef } from "react";

// useStableData — анти-мерцание метрик панели. Бэкенд при ОШИБКЕ Prometheus
// (таймаут и т.п.) штатно отдаёт пустые данные с availability-флагом=false
// (чтобы поллинг UI не спамил 500). Из-за этого графики/счётчики гасли «то есть,
// то нет» при каждом таком ответе.
//
// Хелпер держит последний УДАЧНЫЙ ответ (ok(data)===true) и возвращает его, пока
// прилетают деградированные. Реальную пустоту источника (ok===true, но данных
// нет) НЕ маскируем. `key` (узел+период и т.п.) сбрасывает запомненное при смене
// окна, чтобы не показать данные другого узла/периода.
export function useStableData<T>(
  data: T | undefined,
  key: string,
  ok: (d: T) => boolean,
): T | undefined {
  const lastGood = useRef<{ key: string; data: T } | undefined>(undefined);
  if (lastGood.current && lastGood.current.key !== key) {
    lastGood.current = undefined;
  }
  if (data && ok(data)) {
    lastGood.current = { key, data };
  }
  return data && !ok(data) && lastGood.current ? lastGood.current.data : data;
}
