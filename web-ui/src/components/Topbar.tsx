import { useTranslation } from "react-i18next";
import { Link, useNavigate, useLocation } from "react-router-dom";

import { api } from "../api/client";

export function Topbar() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const loc = useLocation();

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
