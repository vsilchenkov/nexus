import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";
import { cn } from "../lib/cn";
import { GeneralPanel } from "./settings/General";
import { ApiTokensPanel } from "./settings/ApiTokens";
import { LanguagePanel } from "./settings/Language";
import { SentryPanel } from "./settings/Sentry";
import { ClickHousePanel } from "./settings/ClickHouse";
import { UsersPanel } from "./settings/Users";
import { TeamsPanel } from "./settings/Teams";
import { NotificationsPanel } from "./settings/Notifications";
import { MailPanel } from "./settings/Mail";
import { AllowedHostsPanel } from "./settings/AllowedHosts";
import { HeadersPanel } from "./settings/Headers";
import { NodeGroupsPanel } from "./settings/NodeGroups";
import { LogMasksPanel } from "./settings/LogMasks";
import { InstancesPanel } from "./settings/Instances";
import { PasswordPanel } from "./settings/Password";
import { roleAtLeast, type Role } from "../lib/roles";

// minRole — минимальная роль для вкладки (§26). Без поля — доступна всем.
type Tab = { to: string; labelKey: string; minRole?: Role };

const tabs: Tab[] = [
  { to: "general", labelKey: "settings.general.title", minRole: "admin" },
  { to: "users", labelKey: "settings.users.title", minRole: "admin" },
  { to: "teams", labelKey: "settings.teams.title", minRole: "admin" },
  { to: "tokens", labelKey: "settings.tokens.title" },
  { to: "allowed-hosts", labelKey: "settings.allowed_hosts.title", minRole: "manager" },
  // §99: группы узлов — manager, как «Разрешённые хосты»: менеджер и так
  // создаёт группы из формы узла, прятать от него страницу управления незачем.
  { to: "groups", labelKey: "settings.groups.title", minRole: "manager" },
  { to: "headers", labelKey: "settings.headers.title", minRole: "admin" },
  // §95: маскирование секретов в логах узлов по regex-шаблонам.
  { to: "log-masks", labelKey: "settings.log_masks.title", minRole: "admin" },
  // §73: реестр соседних инстансов Nexus (не «узлов» — это маршруты шины).
  { to: "instances", labelKey: "settings.instances.title", minRole: "admin" },
  { to: "password", labelKey: "settings.password.title" },
  { to: "language", labelKey: "settings.language.title" },
  { to: "sentry", labelKey: "settings.sentry.title", minRole: "admin" },
  { to: "clickhouse", labelKey: "settings.clickhouse.title", minRole: "admin" },
  // §88: почта — соседствует с остальными внешними интеграциями.
  { to: "mail", labelKey: "settings.mail.title", minRole: "admin" },
  { to: "notifications", labelKey: "settings.notifications.title", minRole: "admin" },
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
  const role = me.data?.user.role;
  const isAdmin = roleAtLeast(role, "admin");
  const isManager = roleAtLeast(role, "manager");

  const visibleTabs = tabs.filter((tab) => !tab.minRole || roleAtLeast(role, tab.minRole));

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
          {isAdmin && <Route path="general" element={<GeneralPanel />} />}
          {isAdmin && <Route path="users" element={<UsersPanel />} />}
          {isAdmin && <Route path="teams" element={<TeamsPanel />} />}
          <Route path="tokens" element={<ApiTokensPanel />} />
          {isManager && <Route path="allowed-hosts" element={<AllowedHostsPanel />} />}
          {isManager && <Route path="groups" element={<NodeGroupsPanel />} />}
          {isAdmin && <Route path="headers" element={<HeadersPanel />} />}
          {isAdmin && <Route path="log-masks" element={<LogMasksPanel />} />}
          {isAdmin && <Route path="instances" element={<InstancesPanel />} />}
          <Route path="password" element={<PasswordPanel />} />
          <Route path="language" element={<LanguagePanel />} />
          {isAdmin && <Route path="sentry" element={<SentryPanel />} />}
          {isAdmin && <Route path="clickhouse" element={<ClickHousePanel />} />}
          {isAdmin && <Route path="mail" element={<MailPanel />} />}
          {isAdmin && <Route path="notifications" element={<NotificationsPanel />} />}
          <Route path="*" element={<Navigate to="tokens" replace />} />
        </Routes>
      </section>
    </div>
  );
}
