import { NavLink, Route, Routes } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Topbar } from "../components/Topbar";
import { ApiTokensPanel } from "./settings/ApiTokens";
import { LanguagePanel } from "./settings/Language";
import { ThemePanel } from "./settings/Theme";

const tabs = [
  { to: "tokens", label: "API tokens" },
  { to: "language", label: "language" },
  { to: "theme", label: "theme" },
];

export default function Settings() {
  const { t } = useTranslation();

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 flex gap-6">
        <aside className="w-48 shrink-0 space-y-1">
          <div className="text-xs uppercase tracking-wider text-fg-muted mb-2">
            settings
          </div>
          {tabs.map((tab) => (
            <NavLink
              key={tab.to}
              to={tab.to}
              className={({ isActive }) =>
                `block px-3 py-2 rounded-md text-sm ${
                  isActive
                    ? "bg-accent/15 text-accent"
                    : "hover:bg-bg-elev text-fg-muted hover:text-fg"
                }`
              }
            >
              {tab.label}
            </NavLink>
          ))}
        </aside>

        <section className="flex-1 bg-bg-elev rounded-xl border border-bg-muted p-5">
          <Routes>
            <Route path="tokens" element={<ApiTokensPanel />} />
            <Route path="language" element={<LanguagePanel />} />
            <Route path="theme" element={<ThemePanel />} />
            <Route path="*" element={<div className="text-fg-muted">{t("common.loading")}</div>} />
          </Routes>
        </section>
      </main>
    </div>
  );
}
