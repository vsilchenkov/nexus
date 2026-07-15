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

// team-независимые query-ключи: их перезагрузка при смене команды не нужна
// (Phase AUD.7) — остальное живёт в scope текущей команды (nodes/logs/audit/
// metrics/tokens/teams). "me"/"me-teams" НЕ в списке: они несут current_team_id
// и обязаны перечитаться. На модульном уровне — стабильная ссылка для хуков.
export const TEAM_INDEPENDENT_KEYS = new Set(["settings-public", "version", "users-all"]);

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
