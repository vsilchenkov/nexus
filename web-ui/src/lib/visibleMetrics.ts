import { useQueries } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { api, type NodesThroughputResp } from "../api/client";

// Порционная загрузка метрик узлов (§86.4).
//
// В режиме одной команды метрики всех узлов приезжают одним запросом вместе с
// агрегатом шапки (§44.A) — там это дёшево и менять нечего. В сквозном режиме
// узлов кратно больше, поэтому считаются только те, что реально попали в поле
// зрения: время до первого экрана перестаёт зависеть от общего числа узлов.
//
// Хук общий для ОБОИХ представлений рабочего стола (таблица и карточки): два
// независимых наблюдателя расходились бы в том, что считать видимым, и при
// переключении вида метрики загружались бы заново.

// CHUNK_SIZE — сколько узлов уходит в один запрос.
//
// Меньше серверного потолка (200, §86.4) с запасом: пачка должна закрываться
// быстро, иначе первый экран ждёт расчёта узлов, которых не видно.
const CHUNK_SIZE = 50;

// FLUSH_MS — задержка накопления перед запросом. Прокрутка выдаёт десятки
// пересечений подряд; без склейки каждая строка порождала бы свой запрос.
const FLUSH_MS = 120;

// ROOT_MARGIN — запас вокруг окна: пачка уходит чуть раньше, чем строка реально
// покажется, поэтому при спокойной прокрутке цифры уже на месте.
const ROOT_MARGIN = "300px";

export type VisibleMetricsOptions = {
  // enabled=false полностью отключает наблюдение и запросы (режим одной
  // команды, незавершённая загрузка периода).
  enabled: boolean;
  // scopeKey различает режимы в ключе кеша: без него выдача «всех команд» и
  // выдача одной команды алиасятся в один слот (правило §4.44).
  scopeKey: string;
  periodKey: string;
  // params — общие параметры запроса (период + scope).
  params: Record<string, string>;
  refetchInterval: number | false;
};

export type VisibleMetrics = {
  // items — метрики загруженных узлов, ключ — id узла (§86.7).
  items: Map<string, NodesThroughputResp["items"][number]>;
  // observe — ref-callback для строки/карточки узла.
  observe: (nodeId: string) => (el: Element | null) => void;
  // requestedCount — сколько узлов уже запрошено (для тестов и отладки).
  requestedCount: number;
};

// chunk — нарезка на пачки фиксированного размера.
function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

/**
 * useVisibleNodeMetrics — метрики узлов, попавших в поле зрения.
 *
 * Список запрошенных узлов только РАСТЁТ и хранится в порядке появления. Это
 * не мелочь: пачки нарезаются срезами этого списка, поэтому границы уже
 * созданных пачек не сдвигаются, и их ключи кеша не меняются от прокрутки.
 * Если бы список пересортировывался (например по трафику), каждая новая строка
 * переносила бы узлы между пачками и заставляла перезапрашивать всё.
 */
export function useVisibleNodeMetrics(opts: VisibleMetricsOptions): VisibleMetrics {
  const { enabled, scopeKey, periodKey, params, refetchInterval } = opts;

  const [requested, setRequested] = useState<string[]>([]);
  // Накопитель между flush'ами: держим в ref, чтобы пересечения не будили
  // рендер на каждую строку.
  const pending = useRef<Set<string>>(new Set());
  const known = useRef<Set<string>>(new Set());
  const flushTimer = useRef<number | undefined>(undefined);
  const refCache = useRef<Map<string, (el: Element | null) => void>>(new Map());
  const observer = useRef<IntersectionObserver | null>(null);
  const nodeByEl = useRef<Map<Element, string>>(new Map());

  // Смена скоупа или периода обнуляет накопленное: узлы прежнего режима к новым
  // ключам отношения не имеют, а пачки должны собраться заново.
  useEffect(() => {
    pending.current.clear();
    known.current.clear();
    setRequested([]);
  }, [scopeKey, periodKey]);

  const flush = useCallback(() => {
    if (pending.current.size === 0) return;
    const add = Array.from(pending.current);
    pending.current.clear();
    setRequested((prev) => [...prev, ...add]);
  }, []);

  const markVisible = useCallback(
    (nodeId: string) => {
      if (known.current.has(nodeId)) return;
      known.current.add(nodeId);
      pending.current.add(nodeId);
      window.clearTimeout(flushTimer.current);
      flushTimer.current = window.setTimeout(flush, FLUSH_MS);
    },
    [flush],
  );

  // Один наблюдатель на все строки: по одному на строку — это сотни объектов и
  // заметная работа на каждой прокрутке.
  useEffect(() => {
    if (!enabled || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          const id = nodeByEl.current.get(e.target);
          if (id) markVisible(id);
        }
      },
      { rootMargin: ROOT_MARGIN },
    );
    observer.current = io;
    // Подхватываем строки, отрисованные ДО появления наблюдателя.
    //
    // Это не перестраховка: наблюдатель включается по `enabled`, то есть после
    // готовности периода (§71), а строки к этому моменту уже смонтированы и свои
    // ref-callback'и отработали. Без этого прохода метрики не грузились бы
    // вообще, пока оператор не тронет прокрутку — при формально исправном
    // наблюдателе.
    for (const el of nodeByEl.current.keys()) io.observe(el);
    return () => {
      io.disconnect();
      observer.current = null;
    };
  }, [enabled, markVisible]);

  useEffect(() => () => window.clearTimeout(flushTimer.current), []);

  const observe = useCallback(
    (nodeId: string) => {
      const cached = refCache.current.get(nodeId);
      if (cached) return cached;
      const ref = (el: Element | null) => {
        if (!el) return;
        nodeByEl.current.set(el, nodeId);
        // Наблюдателя может ещё не быть (строка отрисована раньше, чем режим
        // стал активен) — такие элементы подхватывает эффект создания
        // наблюдателя, он проходит по этой же карте.
        observer.current?.observe(el);
      };
      refCache.current.set(nodeId, ref);
      return ref;
    },
    [],
  );

  const chunks = useMemo(() => chunk(requested, CHUNK_SIZE), [requested]);

  const results = useQueries({
    queries: chunks.map((ids) => {
      const key = ids.join(",");
      return {
        queryKey: ["metrics-nodes-chunk", scopeKey, periodKey, key],
        queryFn: () =>
          api.get<NodesThroughputResp>("/api/metrics/nodes", { ...params, node_ids: key }),
        refetchInterval,
        enabled,
      };
    }),
  });

  // Карта собирается на каждый рендер, без useMemo: useQueries возвращает новый
  // массив всегда, и мемоизация потребовала бы синтетического ключа с
  // подавлением правила зависимостей — цена честности выше выигрыша, а проход
  // здесь линейный по числу ВИДИМЫХ узлов.
  const items = new Map<string, NodesThroughputResp["items"][number]>();
  for (const r of results) {
    for (const it of r.data?.items ?? []) {
      if (it.node_id) items.set(it.node_id, it);
    }
  }

  return { items, observe, requestedCount: requested.length };
}
