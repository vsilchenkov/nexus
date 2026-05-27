import { useTranslation } from "react-i18next";
import { Link, useNavigate, useLocation } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "../api/client";

// MyTeamsResp / SwitchTeamResp — see /api/me/teams и /api/me/switch-team
// (Phase 10.B.1).
type TeamMembership = {
  id: string;
  slug: string;
  name: string;
  ch_database: string;
  role: "owner" | "admin" | "member";
};
type MyTeamsResp = { items: TeamMembership[]; current_team_id: string };

export function Topbar() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const loc = useLocation();
  const qc = useQueryClient();

  const myTeams = useQuery({
    queryKey: ["me-teams"],
    queryFn: () => api.get<MyTeamsResp>("/api/me/teams"),
    // Не лезем при /login — там session не существует.
    enabled: loc.pathname !== "/login",
  });

  const switchTeam = useMutation({
    mutationFn: (team_id: string) =>
      api.post("/api/me/switch-team", { team_id }),
    onSuccess: () => {
      // Все списки (nodes, audit, logs, tokens) фильтруются по
      // current_team — после switch'а сбрасываем кеш TanStack Query.
      qc.invalidateQueries();
    },
  });

  const navItems = [
    { to: "/", label: t("nav.nodes") },
    { to: "/audit", label: t("nav.audit") },
    { to: "/settings/tokens", label: t("nav.settings") },
  ];

  async function logout() {
    try {
      await api.post("/api/auth/logout");
    } catch {
      // Logout всё равно завершаем локально: даже если сервер недоступен или
      // сессия уже истекла, редирект на /login корректно сбрасывает состояние.
    }
    navigate("/login");
  }

  return (
    <header className="border-b border-bg-muted bg-bg-elev px-6 py-3 flex items-center justify-between">
      <div className="flex items-center gap-6">
        <Link to="/" className="text-lg font-semibold">
          {t("app.title")}
        </Link>
        <nav className="flex items-center gap-3 text-sm">
          {navItems.map((it) => {
            const active =
              it.to === "/" ? loc.pathname === "/" : loc.pathname.startsWith(it.to.split("/").slice(0, 2).join("/"));
            return (
              <Link
                key={it.to}
                to={it.to}
                className={
                  active ? "text-accent" : "text-fg-muted hover:text-fg transition-colors"
                }
              >
                {it.label}
              </Link>
            );
          })}
        </nav>
      </div>
      <div className="flex items-center gap-3 text-sm">
        {myTeams.data && myTeams.data.items.length > 0 && (
          <div className="flex items-center gap-2">
            <span className="text-xs uppercase tracking-wider text-fg-muted">
              {t("nav.team")}
            </span>
            <select
              className="bg-bg-muted px-2 py-1 rounded outline-none"
              value={myTeams.data.current_team_id}
              onChange={(e) => switchTeam.mutate(e.target.value)}
              disabled={switchTeam.isPending || myTeams.data.items.length <= 1}
            >
              {myTeams.data.items.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} ({t.slug})
                </option>
              ))}
            </select>
          </div>
        )}
        <select
          className="bg-bg-muted px-2 py-1 rounded outline-none"
          value={i18n.language.startsWith("ru") ? "ru" : "en"}
          onChange={(e) => i18n.changeLanguage(e.target.value)}
        >
          <option value="en">EN</option>
          <option value="ru">RU</option>
        </select>
        <button
          onClick={logout}
          className="text-fg-muted hover:text-fg transition-colors"
        >
          {t("auth.logout")}
        </button>
      </div>
    </header>
  );
}
