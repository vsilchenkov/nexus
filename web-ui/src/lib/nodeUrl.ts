import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";

export type RouteVerb = "request" | "requestAsync";

type PublicSettings = { public_base_url: string };
type TeamMembership = { id: string; slug: string; external_url?: string };
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

// buildExternalNodeUrl собирает ВНЕШНИЙ адрес узла (§89.4): внешняя ссылка
// команды + путь узла. Пустая строка, если ссылка у команды не задана — вызов
// сам решает, показывать строку или нет.
//
// Хвостовые слеши базы и краевые слеши пути срезаются: база нормализуется и на
// бэкенде (domain.NormalizeTeamExternalURL), но значение может прийти из старой
// записи или из формы до сохранения, а «//» посередине адреса глазами не видно.
export function buildExternalNodeUrl(opts: { externalBase?: string; path: string }): string {
  const base = (opts.externalBase ?? "").trim().replace(/\/+$/, "");
  if (base === "") return "";
  return `${base}/${opts.path.replace(/^\/+|\/+$/g, "")}`;
}

// NodeUrls — формы адреса узла: короткая (основная), классическая и внешняя
// (§89.4; пустая строка, когда у команды нет внешней ссылки).
export type NodeUrls = { short: string; legacy: string; external: string };

// NodeUrlTeam — команда, ОТ ИМЕНИ которой строится адрес.
export type NodeUrlTeam = { slug?: string; externalUrl?: string };

// useNodeUrlBuilder отдаёт функцию сборки адресов узла, подтягивая публичный
// адрес приложения (/api/settings/public) и данные команды.
//
// Команду можно задать явно (team) — и это не украшение, а исправление §89.6:
// по умолчанию берётся slug ТЕКУЩЕЙ команды сессии, тогда как адрес принадлежит
// команде УЗЛА. На странице узла расхождение маскирует гейт useEnsureNodeTeam,
// но на форме СОЗДАНИЯ с явным выбором команды (§86.6) превью показывало адрес
// с чужим слагом: текущая alpha, выбрана webhook → «…/api/v1/alpha/<path>», а
// узел создавался доступным по «…/api/v1/webhook/<path>». Без аргумента
// поведение прежнее — текущая команда сессии.
export function useNodeUrlBuilder(team?: NodeUrlTeam): (verb: RouteVerb, path: string) => NodeUrls {
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
  const current = teams?.items.find((m) => m.id === teams.current_team_id);
  const teamSlug = team?.slug ?? current?.slug;
  const externalBase = team ? team.externalUrl : current?.external_url;

  return (verb, path) => {
    const p = path || "…";
    return {
      short: buildNodeUrl({ publicBaseUrl, teamSlug, path: p }),
      legacy: buildLegacyNodeUrl({ publicBaseUrl, teamSlug, verb, path: p }),
      // Путь-заглушка «…» во внешний адрес не подставляем: строка «внешняя»
      // показывается только у сохранённого узла с реальным путём.
      external: path ? buildExternalNodeUrl({ externalBase, path }) : "",
    };
  };
}
