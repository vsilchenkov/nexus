import { useTranslation } from "react-i18next";
import { useNavigate, useLocation } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ChevronRight, Languages, Moon, Sun, LogOut } from "lucide-react";

import { api } from "../api/client";
import { cn } from "../lib/cn";
import { getTheme, setTheme, type Theme } from "../lib/theme";

type TeamMembership = {
  id: string;
  slug: string;
  name: string;
  ch_database: string;
  role: "owner" | "admin" | "member";
};
type MyTeamsResp = { items: TeamMembership[]; current_team_id: string };

// crumbsFor — хлебные крошки из маршрута (секция + при наличии второй уровень).
function crumbsFor(path: string, t: (k: string) => string): string[] {
  if (path === "/" || (path.startsWith("/nodes") && !path.includes("/", 6))) {
    if (path === "/nodes/new") return [t("nav.nodes"), t("overview.new_node")];
    return [t("nav.nodes")];
  }
  if (path.startsWith("/nodes")) {
    if (path.endsWith("/edit")) return [t("nav.nodes"), t("common.edit")];
    if (path.endsWith("/new")) return [t("nav.nodes"), t("overview.new_node")];
    return [t("nav.nodes")];
  }
  if (path.startsWith("/audit")) return [t("nav.audit")];
  if (path.startsWith("/settings")) return [t("nav.settings")];
  return [t("nav.nodes")];
}

// Topbar — тонкая верхняя панель (§21): крошки слева; справа — переключатель
// команды, языка, темы и выход. Рендерится внутри AppShell.
export function Topbar() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const loc = useLocation();
  const qc = useQueryClient();
  const [theme, setThemeState] = useState<Theme>(getTheme());

  const myTeams = useQuery({
    queryKey: ["me-teams"],
    queryFn: () => api.get<MyTeamsResp>("/api/me/teams"),
    enabled: loc.pathname !== "/login",
  });

  const switchTeam = useMutation({
    mutationFn: (team_id: string) => api.post("/api/me/switch-team", { team_id }),
    onSuccess: () => qc.invalidateQueries(),
  });

  function toggleLang() {
    i18n.changeLanguage(i18n.language.startsWith("ru") ? "en" : "ru");
  }

  function toggleTheme() {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    setThemeState(next);
  }

  async function logout() {
    try {
      await api.post("/api/auth/logout");
    } catch {
      // Локальный logout завершаем в любом случае.
    }
    navigate("/login");
  }

  const crumbs = crumbsFor(loc.pathname, t);

  return (
    <header className="flex h-header shrink-0 items-center gap-3.5 border-b border-line bg-bg px-5">
      <div className="flex items-center gap-2 text-[13px] text-fg-muted">
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-2">
            {i > 0 && <ChevronRight className="h-3.5 w-3.5 text-fg-subtle" />}
            <span className={i === crumbs.length - 1 ? "text-fg" : ""}>{c}</span>
          </span>
        ))}
      </div>

      <div className="flex-1" />

      {myTeams.data && myTeams.data.items.length > 0 && (
        <select
          className="rounded-md border border-line bg-app px-2 py-1 text-xs text-fg outline-none focus:border-accent"
          value={myTeams.data.current_team_id}
          onChange={(e) => switchTeam.mutate(e.target.value)}
          disabled={switchTeam.isPending || myTeams.data.items.length <= 1}
        >
          {myTeams.data.items.map((tm) => (
            <option key={tm.id} value={tm.id}>
              {tm.name} ({tm.slug})
            </option>
          ))}
        </select>
      )}

      <IconBtn onClick={toggleLang} title={t("nav.language")}>
        <Languages className="h-[18px] w-[18px]" />
        <span className="ml-1 text-[11px] uppercase">
          {i18n.language.startsWith("ru") ? "ru" : "en"}
        </span>
      </IconBtn>

      <IconBtn onClick={toggleTheme} title={t("nav.theme")}>
        {theme === "dark" ? <Moon className="h-[18px] w-[18px]" /> : <Sun className="h-[18px] w-[18px]" />}
      </IconBtn>

      <IconBtn onClick={logout} title={t("auth.logout")}>
        <LogOut className="h-[18px] w-[18px]" />
      </IconBtn>
    </header>
  );
}

function IconBtn({
  children,
  onClick,
  title,
}: {
  children: React.ReactNode;
  onClick: () => void;
  title: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className={cn(
        "flex h-8 items-center rounded-md px-2 text-fg-muted",
        "transition-colors hover:bg-bg-muted hover:text-fg",
      )}
    >
      {children}
    </button>
  );
}
