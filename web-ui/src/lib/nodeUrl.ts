import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";

export type RouteVerb = "request" | "requestAsync";

type PublicSettings = { public_base_url: string };
type TeamMembership = { id: string; slug: string };
type MyTeamsResp = { items: TeamMembership[]; current_team_id: string };

// Сегменты-методы боевого адреса (§78.2). Регистр значим — Receiver сравнивает
// первый сегмент точно так же.
const INGRESS_SEGMENTS = new Set(["request", "requestAsync", "callback"]);

// nodeUrlOrigin — публичный адрес приложения (если задан) или origin браузера.
function nodeUrlOrigin(publicBaseUrl?: string): string {
  const base = (publicBaseUrl ?? "").trim() || window.location.origin;
  return base.replace(/\/+$/, "");
}

// buildNodeUrl собирает КОРОТКИЙ публичный адрес узла (§78.1) — без сегмента
// метода: /api/v1/<team_slug>/<path>. Синхронность определяет root_method узла,
// поэтому в адресе она не нужна.
//
// Для команды default слог опускается (/api/v1/<path>) — кроме случая, когда
// первый сегмент пути совпадает с сегментом-методом: такой адрес Receiver
// прочитал бы как legacy-форму, поэтому показываем его со слогом.
export function buildNodeUrl(opts: {
  publicBaseUrl?: string;
  teamSlug?: string;
  path: string;
}): string {
  const origin = nodeUrlOrigin(opts.publicBaseUrl);
  const slug = opts.teamSlug && opts.teamSlug !== "default" ? opts.teamSlug : "";
  const firstSegment = opts.path.split("/")[0];
  const team = slug || (INGRESS_SEGMENTS.has(firstSegment) ? "default" : "");
  return `${origin}/api/v1${team ? `/${team}` : ""}/${opts.path}`;
}

// buildLegacyNodeUrl собирает КЛАССИЧЕСКИЙ адрес узла — с сегментом метода
// (§28 Пункт 1). Выданные внешним системам адреса этой формы работают без
// ограничения срока (§78.1), поэтому она остаётся видимой в интерфейсе.
export function buildLegacyNodeUrl(opts: {
  publicBaseUrl?: string;
  teamSlug?: string;
  verb: RouteVerb;
  path: string;
}): string {
  const origin = nodeUrlOrigin(opts.publicBaseUrl);
  const team = opts.teamSlug && opts.teamSlug !== "default" ? `/${opts.teamSlug}` : "";
  return `${origin}/api/v1/${opts.verb}${team}/${opts.path}`;
}

// NodeUrls — обе формы адреса узла: короткая (основная) и классическая.
export type NodeUrls = { short: string; legacy: string };

// useNodeUrlBuilder отдаёт функцию сборки адресов узла, подтягивая публичный
// адрес приложения (/api/settings/public) и slug текущей команды
// (/api/me/teams). Обе query кешируются TanStack Query и переиспользуются.
export function useNodeUrlBuilder(): (verb: RouteVerb, path: string) => NodeUrls {
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

  return (verb, path) => {
    const p = path || "…";
    return {
      short: buildNodeUrl({ publicBaseUrl, teamSlug, path: p }),
      legacy: buildLegacyNodeUrl({ publicBaseUrl, teamSlug, verb, path: p }),
    };
  };
}
