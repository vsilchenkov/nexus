import { NavLink, Route, Routes } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";

import { Topbar } from "../components/Topbar";
import { api } from "../api/client";
import { ApiTokensPanel } from "./settings/ApiTokens";
import { LanguagePanel } from "./settings/Language";
import { ThemePanel } from "./settings/Theme";
import { SentryPanel } from "./settings/Sentry";
import { ClickHousePanel } from "./settings/ClickHouse";

type Tab = { to: string; labelKey: string; adminOnly?: boolean };

const tabs: Tab[] = [
  { to: "tokens", labelKey: "settings.tokens.title" },
  { to: "language", labelKey: "settings.language.title" },
  { to: "theme", labelKey: "settings.theme.title" },
  { to: "sentry", labelKey: "settings.sentry.title", adminOnly: true },
  { to: "clickhouse", labelKey: "settings.clickhouse.title", adminOnly: true },
];

function useRole() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () =>
      api.get<{ user: { user_id: string; role: string } }>("/api/auth/me"),
  });
}

export default function Settings() {
  const { t } = useTranslation();
  const me = useRole();
  const isAdmin = me.data?.user.role === "admin";

  const visibleTabs = tabs.filter((tab) => !tab.adminOnly || isAdmin);

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-6xl mx-auto p-6 flex gap-6">
        <aside className="w-48 shrink-0 space-y-1">
          <div className="text-xs uppercase tracking-wider text-fg-muted mb-2">
            {t("nav.settings")}
          </div>
          {visibleTabs.map((tab) => (
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
              {t(tab.labelKey)}
            </NavLink>
          ))}
        </aside>

        <section className="flex-1 bg-bg-elev rounded-xl border border-bg-muted p-5">
          <Routes>
            <Route path="tokens" element={<ApiTokensPanel />} />
            <Route path="language" element={<LanguagePanel />} />
            <Route path="theme" element={<ThemePanel />} />
            {isAdmin && <Route path="sentry" element={<SentryPanel />} />}
            {isAdmin && (
              <Route path="clickhouse" element={<ClickHousePanel />} />
            )}
            <Route
              path="*"
              element={
                <div className="text-fg-muted">{t("common.loading")}</div>
              }
            />
          </Routes>
        </section>
      </main>
    </div>
  );
}
