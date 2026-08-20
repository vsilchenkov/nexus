import { NavLink, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { LayoutGrid, History, Settings, ArrowLeftRight, Activity, ScrollText } from "lucide-react";

import { api } from "../api/client";
import { cn } from "../lib/cn";
import { roleAtLeast } from "../lib/roles";
import { SidebarFavorites } from "./SidebarFavorites";

type NavItem = {
  to: string;
  label: string;
  icon: React.ReactNode;
  match: (p: string) => boolean;
  // badge — число неразобранных групп отказов (§94.7). Пока раздел не открыт,
  // это единственный признак, что в шину кто-то стучится мимо.
  badge?: number;
};

// Sidebar — постоянная левая навигация панели (§21): бренд, разделы,
// футер с текущим пользователем.
export function Sidebar() {
  const { t } = useTranslation();
  const loc = useLocation();

  const me = useQuery({
    queryKey: ["me"],
    queryFn: () =>
      api.get<{ user: { user_id: string; login?: string; name?: string; role: string } }>(
        "/api/auth/me",
      ),
  });

  // §30/§34.3: версия приложения (публичный эндпоинт). commit/build_date —
  // в tooltip; version может быть переопределена в dev (override_allowed).
  const version = useQuery({
    queryKey: ["version"],
    queryFn: () =>
      api.get<{
        version: string;
        commit?: string;
        build_date?: string;
        override_allowed?: boolean;
      }>("/api/version"),
    staleTime: Infinity,
  });

  // §94.7: бейдж пункта «Логи» — неразобранные группы отказов за сутки.
  // Запрос лёгкий (агрегат в PostgreSQL) и идёт только у тех, кому раздел
  // доступен; при выключенном журнале сервер отдаёт нули, и бейдж исчезает.
  const rejectedSummary = useQuery({
    queryKey: ["rejected", "summary", "sidebar"],
    queryFn: () =>
      api.get<{ unresolved: number }>("/api/rejected/summary", {
        from: new Date(Date.now() - 24 * 3600_000).toISOString(),
      }),
    enabled: roleAtLeast(me.data?.user.role, "operator"),
    refetchInterval: 60_000,
    staleTime: 30_000,
  });
  const unresolvedRejects = rejectedSummary.data?.unresolved ?? 0;

  // Пункт «Kafka» (§4 spec) — в блоке Аудита (рядом с Audit log), не в
  // «Настройках»; виден только админам (роль admin), как и сам раздел.
  const isAdmin = roleAtLeast(me.data?.user.role, "admin");
  // §94.7: журнал отказов доступен operator+ — от этого зависит и видимость
  // пункта «Логи», внутри которого он живёт.
  const canSeeRejected = roleAtLeast(me.data?.user.role, "operator");
  // Журнал действий (§26/§87) доступен operator+ — viewer получал 403 (П6).
  const canSeeAudit = roleAtLeast(me.data?.user.role, "operator");

  // Основная навигация (оперативные разделы) — вверху сайдбара.
  const items: NavItem[] = [
    {
      to: "/",
      label: t("nav.nodes"),
      icon: <LayoutGrid className="h-[18px] w-[18px]" />,
      match: (p) => p === "/" || p.startsWith("/nodes"),
    },
    ...(canSeeAudit
      ? [
          {
            to: "/audit",
            label: t("nav.audit"),
            icon: <History className="h-[18px] w-[18px]" />,
            match: (p: string) => p.startsWith("/audit"),
          },
        ]
      : []),
    ...(isAdmin
      ? [
          {
            to: "/kafka",
            label: t("nav.kafka"),
            icon: <Activity className="h-[18px] w-[18px]" />,
            match: (p: string) => p.startsWith("/kafka"),
          },
        ]
      : []),
    // §94.7: раздел «Логи» — две вкладки: служебные логи сервисов (§51,
    // admin-only) и журнал отказов на входе (operator+). Пункт виден
    // operator+, потому что вторая вкладка адресована именно ему; вкладку
    // «Сервисы» рисует только администратор.
    ...(canSeeRejected
      ? [
          {
            to: "/logs",
            label: t("nav.logs"),
            icon: <ScrollText className="h-[18px] w-[18px]" />,
            match: (p: string) => p.startsWith("/logs"),
            badge: unresolvedRejects,
          },
        ]
      : []),
  ];

  // §34.1: «Настройки» — вспомогательный раздел, опущен в подвал сайдбара
  // (отдельно от оперативной навигации, рядом с пользователем/версией).
  const settingsItem: NavItem = {
    to: "/settings/tokens",
    label: t("nav.settings"),
    icon: <Settings className="h-[18px] w-[18px]" />,
    match: (p) => p.startsWith("/settings"),
  };

  // renderNavLink — единый рендер пункта (классы не дублируются между блоками).
  const renderNavLink = (it: NavItem) => {
    const active = it.match(loc.pathname);
    return (
      <NavLink
        key={it.to}
        to={it.to}
        className={cn(
          "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] transition-colors",
          active
            ? "bg-bg-muted font-medium text-fg"
            : "text-fg-muted hover:bg-bg-muted hover:text-fg",
        )}
      >
        {it.icon}
        <span className="flex-1">{it.label}</span>
        {!!it.badge && it.badge > 0 && (
          <span className="rounded-full bg-warn/20 px-1.5 py-0.5 text-[10px] font-medium text-warn">
            {it.badge > 99 ? "99+" : it.badge}
          </span>
        )}
      </NavLink>
    );
  };

  // §66: в чипе показываем отображаемое имя (фолбэк на логин для старых
  // ответов /api/auth/me без name).
  const displayName = me.data?.user.name || me.data?.user.login || "admin";
  // role.{admin,manager,operator,viewer} — §26, §87.
  const role = t(`role.${me.data?.user.role ?? "viewer"}`);

  return (
    <aside className="flex w-sidebar shrink-0 flex-col border-r border-line bg-bg p-3">
      <div className="flex items-center gap-2.5 px-2.5 pb-4 pt-1.5 text-[15px] font-semibold">
        <span className="grid h-[26px] w-[26px] place-items-center rounded-md bg-gradient-to-br from-accent to-ok text-white">
          <ArrowLeftRight className="h-[15px] w-[15px]" />
        </span>
        Nexus
      </div>

      <nav className="space-y-0.5">{items.map(renderNavLink)}</nav>

      {/* §49: избранные команды — клик переключает команду, drag меняет порядок. */}
      <SidebarFavorites />

      {/* Подвал: «Настройки» + пользователь/версия — прижаты к низу (§34.1). */}
      <nav className="mt-auto space-y-0.5 border-t border-line pt-2.5">
        {renderNavLink(settingsItem)}
      </nav>

      <div className="mt-2.5 border-t border-line pt-2.5">
        <div className="flex items-center gap-2.5 px-1 text-xs text-fg-muted">
          <span className="grid h-[26px] w-[26px] place-items-center rounded-full bg-accent/15 text-[11px] font-semibold text-accent">
            {displayName.slice(0, 1).toUpperCase()}
          </span>
          <span className="truncate">
            {displayName} · {role}
          </span>
        </div>
        {version.data?.version && (
          <div
            className="mt-1.5 px-1 text-[11px] text-fg-subtle"
            title={[
              version.data.commit && `commit: ${version.data.commit}`,
              version.data.build_date && `build: ${version.data.build_date}`,
            ]
              .filter(Boolean)
              .join("\n")}
          >
            v{version.data.version}
          </div>
        )}
      </div>
    </aside>
  );
}
