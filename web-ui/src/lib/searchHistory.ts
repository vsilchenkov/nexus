import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "../api/client";

// Слой данных истории поиска узлов (§62). История персонально хранится на
// бэкенде (последние 10 строк на пользователя, PostgreSQL) и общая для двух
// мест ввода: глобального поиска в шапке и поля «Поиск» на странице узлов.
//
// Ключ ["search-history"] попадает в TEAM_INDEPENDENT_KEYS (lib/teams.ts) —
// история привязана к пользователю, а не к команде, и смена команды её не
// сбрасывает.

export const SEARCH_HISTORY_KEY = ["search-history"] as const;

type SearchHistoryResp = { items: string[] };

// useSearchHistory — последние строки поиска пользователя (свежие первыми).
export function useSearchHistory() {
  return useQuery({
    queryKey: SEARCH_HISTORY_KEY,
    queryFn: () => api.get<SearchHistoryResp>("/api/me/search-history"),
  });
}

// useRecordSearch — сохранить строку в историю. Мусор (короткая/пустая строка)
// сервер молча игнорирует — вызывать можно без предварительной валидации.
// После записи перечитываем историю, чтобы дропдаун показал новый порядок.
export function useRecordSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (q: string) => api.post("/api/me/search-history", { q }),
    onSuccess: () => qc.invalidateQueries({ queryKey: SEARCH_HISTORY_KEY }),
  });
}

// useClearSearchHistory — очистить всю историю. Optimistic: список пустеет сразу.
export function useClearSearchHistory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.del("/api/me/search-history"),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: SEARCH_HISTORY_KEY });
      const prev = qc.getQueryData<SearchHistoryResp>(SEARCH_HISTORY_KEY);
      qc.setQueryData<SearchHistoryResp>(SEARCH_HISTORY_KEY, { items: [] });
      return { prev };
    },
    onError: (_err, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(SEARCH_HISTORY_KEY, ctx.prev);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: SEARCH_HISTORY_KEY }),
  });
}

// MIN_SEARCH_QUERY_LEN — минимум рун, при котором запрос вообще идёт на бэкенд
// (совпадает с порогом usecase §62). Короче — не грузим все команды.
export const MIN_SEARCH_QUERY_LEN = 2;

// Persistence текста глобального поиска в sessionStorage (§62): в отличие от
// фильтров Overview (§54) топбар глобален, а URL принадлежит страницам —
// поэтому текст храним не в URL, а в sessionStorage. Переживает уход со
// страницы и «Назад»; после выбора результата запрос НЕ очищается.
const GLOBAL_SEARCH_KEY = "nexus.globalsearch.q";

export function loadGlobalSearchQ(): string {
  try {
    return window.sessionStorage.getItem(GLOBAL_SEARCH_KEY) ?? "";
  } catch {
    return "";
  }
}

export function saveGlobalSearchQ(q: string): void {
  try {
    if (q) window.sessionStorage.setItem(GLOBAL_SEARCH_KEY, q);
    else window.sessionStorage.removeItem(GLOBAL_SEARCH_KEY);
  } catch {
    // sessionStorage недоступен (private mode) — persistence best-effort.
  }
}

// NodeSearchItem — элемент выдачи GET /api/search/nodes (§62).
export type NodeSearchItem = {
  id: string;
  path: string;
  target_url: string;
  root_method: string;
  status: string;
  team_id: string;
  team_slug: string;
  team_name: string;
};

// useNodeSearch — кросс-командный поиск узлов. enabled — управляет вызовом
// извне (открыт дропдаун + запрос достаточной длины).
export function useNodeSearch(q: string, enabled: boolean) {
  return useQuery({
    queryKey: ["node-search", q],
    queryFn: () => api.get<{ items: NodeSearchItem[] }>("/api/search/nodes", { q, limit: 20 }),
    enabled,
    // Прежняя выдача остаётся видимой, пока едет новая (без мигания при вводе).
    placeholderData: keepPreviousData,
  });
}
