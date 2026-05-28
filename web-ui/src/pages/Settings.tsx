import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";
import { cn } from "../lib/cn";
import { ApiTokensPanel } from "./settings/ApiTokens";
import { LanguagePanel } from "./settings/Language";
import { SentryPanel } from "./settings/Sentry";
import { ClickHousePanel } from "./settings/ClickHouse";
import { UsersPanel } from "./settings/Users";
import { TeamsPanel } from "./settings/Teams";
import { NotificationsPanel } from "./settings/Notifications";

type Tab = { to: string; labelKey: string; adminOnly?: boolean };

const tabs: Tab[] = [
  { to: "users", labelKey: "settings.users.title", adminOnly: true },
  { to: "teams", labelKey: "settings.teams.title", adminOnly: true },
  { to: "tokens", labelKey: "settings.tokens.title" },
  { to: "language", labelKey: "settings.language.title" },
  { to: "sentry", labelKey: "settings.sentry.title", adminOnly: true },
  { to: "clickhouse", labelKey: "settings.clickhouse.title", adminOnly: true },
  { to: "notifications", labelKey: "settings.notifications.title", adminOnly: true },
];

function useRole() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<{ user: { user_id: string; role: string } }>("/api/auth/me"),
  });
}

export default function Settings() {
  const { t } = useTranslation();
  const me = useRole();
  const isAdmin = me.data?.user.role === "admin";

  const visibleTabs = tabs.filter((tab) => !tab.adminOnly || isAdmin);

  return (
    <div className="mx-auto flex max-w-6xl gap-6">
      <aside className="w-52 shrink-0 space-y-0.5">
        <div className="mb-2 px-2 text-[10px] uppercase tracking-wider text-fg-subtle">
          {t("nav.settings")}
        </div>
        {visibleTabs.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            className={({ isActive }) =>
              cn(
                "block rounded-md px-2.5 py-2 text-[13px] transition-colors",
                isActive
                  ? "bg-bg-muted font-medium text-fg"
                  : "text-fg-muted hover:bg-bg-muted hover:text-fg",
              )
            }
          >
            {t(tab.labelKey)}
          </NavLink>
        ))}
      </aside>

      <section className="min-w-0 flex-1">
        <Routes>
          {isAdmin && <Route path="users" element={<UsersPanel />} />}
          {isAdmin && <Route path="teams" element={<TeamsPanel />} />}
          <Route path="tokens" element={<ApiTokensPanel />} />
          <Route path="language" element={<LanguagePanel />} />
          {isAdmin && <Route path="sentry" element={<SentryPanel />} />}
          {isAdmin && <Route path="clickhouse" element={<ClickHousePanel />} />}
          {isAdmin && <Route path="notifications" element={<NotificationsPanel />} />}
          <Route path="*" element={<Navigate to="tokens" replace />} />
        </Routes>
      </section>
    </div>
  );
}
