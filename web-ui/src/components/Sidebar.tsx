import { NavLink, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { LayoutGrid, History, Settings, ArrowLeftRight } from "lucide-react";

import { api } from "../api/client";
import { cn } from "../lib/cn";

type NavItem = { to: string; label: string; icon: React.ReactNode; match: (p: string) => boolean };

// Sidebar — постоянная левая навигация панели (§21): бренд, разделы,
// футер с текущим пользователем.
export function Sidebar() {
  const { t } = useTranslation();
  const loc = useLocation();

  const me = useQuery({
    queryKey: ["me"],
    queryFn: () =>
      api.get<{ user: { user_id: string; login?: string; role: string } }>("/api/auth/me"),
  });

  const items: NavItem[] = [
    {
      to: "/",
      label: t("nav.nodes"),
      icon: <LayoutGrid className="h-[18px] w-[18px]" />,
      match: (p) => p === "/" || p.startsWith("/nodes"),
    },
    {
      to: "/audit",
      label: t("nav.audit"),
      icon: <History className="h-[18px] w-[18px]" />,
      match: (p) => p.startsWith("/audit"),
    },
    {
      to: "/settings/tokens",
      label: t("nav.settings"),
      icon: <Settings className="h-[18px] w-[18px]" />,
      match: (p) => p.startsWith("/settings"),
    },
  ];

  const login = me.data?.user.login ?? "admin";
  const role = me.data?.user.role === "admin" ? t("role.admin") : t("role.viewer");

  return (
    <aside className="flex w-sidebar shrink-0 flex-col border-r border-line bg-bg p-3">
      <div className="flex items-center gap-2.5 px-2.5 pb-4 pt-1.5 text-[15px] font-semibold">
        <span className="grid h-[26px] w-[26px] place-items-center rounded-md bg-gradient-to-br from-accent to-ok text-white">
          <ArrowLeftRight className="h-[15px] w-[15px]" />
        </span>
        Nexus
      </div>

      <nav className="space-y-0.5">
        {items.map((it) => {
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
              {it.label}
            </NavLink>
          );
        })}
      </nav>

      <div className="mt-auto border-t border-line pt-2.5">
        <div className="flex items-center gap-2.5 px-1 text-xs text-fg-muted">
          <span className="grid h-[26px] w-[26px] place-items-center rounded-full bg-accent/15 text-[11px] font-semibold text-accent">
            {login.slice(0, 1).toUpperCase()}
          </span>
          <span className="truncate">
            {login} · {role}
          </span>
        </div>
      </div>
    </aside>
  );
}
