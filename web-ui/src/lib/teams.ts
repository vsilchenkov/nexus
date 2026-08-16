import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { isAxiosError } from "axios";
import { useLocation } from "react-router-dom";

import { api } from "../api/client";

// Общий слой данных team-switcher'а (§18/§49): типы + хуки для членств,
// переключения текущей команды и избранного. Вынесен из Topbar, чтобы
// сайдбар (секция «Избранное») переключал команду с той же семантикой
// инвалидации кеша.

// TeamMembership — элемент списка GET /api/me/teams.
export type TeamMembership = {
  id: string;
  slug: string;
  name: string;
  ch_database: string;
  // external_url — §89.4: адрес команды снаружи контура (база, к которой
  // приклеивается путь узла). Пустая строка = не задан.
  external_url: string;
  role: "owner" | "admin" | "member";
};

// healed (§44.H): сервер переключил current_team в сессии, т.к. он больше не
// входил в членства (напр. пользователя убрали из команды, бывшей дефолтом).
// По этому флагу UI инвалидирует team-scoped кеш — иначе дашборд показывал бы
// ноды старой команды до ручного обновления.
// favorites (§49): упорядоченный список id избранных команд пользователя —
// едет вместе с членствами (один источник истины, без второго запроса).
export type MyTeamsResp = {
  items: TeamMembership[];
  current_team_id: string;
  healed?: boolean;
  favorites: string[];
};

// TEAM_INDEPENDENT_KEYS — ключи, которые смена команды НЕ трогает. Правило:
// ключ живёт здесь тогда и только тогда, когда его эндпоинт не фильтрует по
// команде на бэкенде. В scope команды остаются nodes/logs/audit/metrics — там
// currentTeamID(c) реально меняет ответ.
//
// **Раздел «Настройки» — вне скоупа команды целиком** (личный и
// административный раздел; переключатель в шапке не должен его дёргать, иначе
// одни вкладки мигают, другие нет). Это не «отключение обновления», а
// приведение кеша в соответствие с бэкендом: у всех перечисленных ниже
// эндпоинтов Настроек team-фильтра нет. Каждый ключ проверен:
//   app-settings   — глобальные настройки инстанса (General/Sentry/ClickHouse/Notifications)
//   users          — /api/users глобальный («список глобальный, без team-scope» в user_handler)
//   users-all      — тот же /api/users под вторым ключом (был освобождён и раньше)
//   teams          — uc.List(ctx), все команды
//   team-members   — скоуп берётся из :id в URL, а не из текущей команды
//   tokens         — токены пользователя по всем командам (команда видна колонкой, §18.3)
//   ch-templates / headers-catalog / headers / allowed-hosts / host-preview /
//   orphan-tables  — глобальные справочники и чистые функции
//
// НЕ добавлять сюда: "me"/"me-teams" (несут current_team_id — обязаны
// перечитываться), "node-hosts" (team-scoped через ListByNode).
export const TEAM_INDEPENDENT_KEYS = new Set([
  // public-settings, а не settings-public: ключ был записан перевёрнутым и
  // потому не работал — публичные настройки зря перечитывались при каждой
  // смене команды. Имя обязано совпадать с queryKey[0] (lib/nodeUrl.ts,
  // node/useNodeMetrics.ts, settings/General.tsx).
  "public-settings",
  "version",
  "app-settings",
  "users",
  "users-all",
  "teams",
  "team-members",
  "tokens",
  "ch-templates",
  "headers-catalog",
  "headers",
  "allowed-hosts",
  "host-preview",
  "orphan-tables",
  // §62: глобальный поиск и история — привязаны к пользователю/членствам, а не
  // к текущей команде (эндпоинты /api/search/nodes и /api/me/search-history
  // фильтруют по членствам и user_id), смена команды их не сбрасывает.
  "node-search",
  "search-history",
  // §71: персональные предпочтения. Ключ team-independent НЕ потому, что фича
  // вне команд, а наоборот — GET /api/me/prefs отдаёт префы ВСЕХ команд сразу
  // и от current_team не зависит. Инвалидация на смене команды дала бы рефетч
  // ровно тех же данных и мигание периода ровно в тот момент, когда дефолт
  // новой команды должен примениться мгновенно.
  "me-prefs",
  // §73: реестр соседних инстансов. GET /api/instances team-фильтра не имеет —
  // инстанс это инфраструктура целиком, а не ресурс команды (в таблице
  // peer_instances нет team_id). Смена команды дала бы рефетч тех же данных и
  // повторный опрос соседей по сети.
  "instances",
]);

export const MY_TEAMS_KEY = ["me-teams"] as const;

// invalidateTeamScoped — сброс всех team-scoped ключей (после смены команды
// или самолечения сессии §44.H).
export function invalidateTeamScoped(qc: QueryClient) {
  return qc.invalidateQueries({
    predicate: (q) => !TEAM_INDEPENDENT_KEYS.has(String(q.queryKey[0])),
  });
}

// useMyTeams — членства + current_team_id + избранное текущего пользователя.
export function useMyTeams() {
  const loc = useLocation();
  return useQuery({
    queryKey: MY_TEAMS_KEY,
    queryFn: () => api.get<MyTeamsResp>("/api/me/teams"),
    enabled: loc.pathname !== "/login",
  });
}

// useCurrentTeamID — id текущей команды сессии ("" пока членства не загрузились).
// Нужен team-scoped ключам кеша (Overview): без teamId в queryKey записи разных
// команд алиасятся в один слот, и после смены команды react-query мгновенно
// отдаёт закешированный список ПРЕЖНЕЙ команды (stale-while-revalidate) —
// «стёр поиск → узлы не той команды». invalidateTeamScoped помечает stale, но
// от алиасинга ключей не спасает.
export function useCurrentTeamID(): string {
  const { data } = useMyTeams();
  return data?.current_team_id ?? "";
}

// useCurrentTeamCHDatabase — имя БД ClickHouse текущей команды (`nexus_<slug>`),
// §64. Нужно форме создания узла: подставить редактируемый префикс в поле
// «Таблица логов», чтобы оператор дописывал только имя таблицы. Пусто, пока
// членства не загрузились.
export function useCurrentTeamCHDatabase(): string {
  const { data } = useMyTeams();
  if (!data) return "";
  return data.items.find((m) => m.id === data.current_team_id)?.ch_database ?? "";
}

// useSwitchTeam — смена текущей команды сессии. 403 (членство сняли, а UI ещё
// показывает команду/избранную) → перечитываем членства: протухший пункт
// исчезнет сам (на бэке избранное каскадно удалено вместе с членством).
export function useSwitchTeam() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (team_id: string) => api.post("/api/me/switch-team", { team_id }),
    onSuccess: () => invalidateTeamScoped(qc),
    onError: (err) => {
      if (isAxiosError(err) && err.response?.status === 403) {
        void qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
      }
    },
  });
}

// useSetFavoriteTeams — полная замена списка избранного (§49): звезда и
// drag-and-drop шлют весь упорядоченный список. Optimistic update: избранное
// правится в кеше сразу (звезда/порядок без мигания), при ошибке — откат.
export function useSetFavoriteTeams() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (team_ids: string[]) =>
      api.put<{ team_ids: string[] }>("/api/me/favorite-teams", { team_ids }),
    onMutate: async (team_ids) => {
      await qc.cancelQueries({ queryKey: MY_TEAMS_KEY });
      const prev = qc.getQueryData<MyTeamsResp>(MY_TEAMS_KEY);
      if (prev) {
        qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, { ...prev, favorites: team_ids });
      }
      return { prev };
    },
    onError: (_err, _ids, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(MY_TEAMS_KEY, ctx.prev);
      }
    },
    onSettled: () => qc.invalidateQueries({ queryKey: MY_TEAMS_KEY }),
  });
}
