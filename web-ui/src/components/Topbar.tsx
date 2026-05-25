import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { api } from "../api/client";

export function Topbar() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();

  async function logout() {
    try {
      await api.post("/api/auth/logout");
    } catch {}
    navigate("/login");
  }

  return (
    <header className="border-b border-bg-muted bg-bg-elev px-6 py-3 flex items-center justify-between">
      <a href="/" className="text-lg font-semibold">
        {t("app.title")}
      </a>
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
