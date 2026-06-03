import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";

export type RouteVerb = "request" | "requestAsync";

type PublicSettings = { public_base_url: string };
type TeamMembership = { id: string; slug: string };
type MyTeamsResp = { items: TeamMembership[]; current_team_id: string };

// buildNodeUrl собирает полный публичный адрес узла (§28, Пункт 1).
//
// origin = publicBaseUrl, если задан в настройках приложения, иначе
// window.location.origin (адрес хоста в браузере). team_slug включается
// только для не-default команды; для default путь остаётся legacy-формой
// без слога (её Receiver резолвит через fallback, фикс #8).
export function buildNodeUrl(opts: {
  publicBaseUrl?: string;
  teamSlug?: string;
  verb: RouteVerb;
  path: string;
}): string {
  const base = (opts.publicBaseUrl ?? "").trim() || window.location.origin;
  const origin = base.replace(/\/+$/, "");
  const team = opts.teamSlug && opts.teamSlug !== "default" ? `/${opts.teamSlug}` : "";
  return `${origin}/api/v1/${opts.verb}${team}/${opts.path}`;
}

// useNodeUrlBuilder отдаёт функцию сборки адреса узла, подтягивая публичный
// адрес приложения (/api/settings/public) и slug текущей команды
// (/api/me/teams). Обе query кешируются TanStack Query и переиспользуются.
export function useNodeUrlBuilder(): (verb: RouteVerb, path: string) => string {
  const publicSettings = useQuery({
    queryKey: ["public-settings"],
    queryFn: () => api.get<PublicSettings>("/api/settings/public"),
    staleTime: 5 * 60_000,
  });
  const myTeams = useQuery({
    queryKey: ["me-teams"],
    queryFn: () => api.get<MyTeamsResp>("/api/me/teams"),
    staleTime: 5 * 60_000,
  });

  const publicBaseUrl = publicSettings.data?.public_base_url;
  const teams = myTeams.data;
  const teamSlug = teams?.items.find((m) => m.id === teams.current_team_id)?.slug;

  return (verb, path) => buildNodeUrl({ publicBaseUrl, teamSlug, verb, path: path || "…" });
}
