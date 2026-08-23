import { NavLink, Outlet } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { cn } from "../lib/cn";
import { roleAtLeast } from "../lib/roles";

type Me = { user: { role: string } };

// LogsSection — раздел «Логи» с двумя вкладками (§94.7): служебные логи
// сервисов (§51) и журнал отказов на входе (§94).
//
// Вкладки, а не отдельный пункт меню: сайдбар несёт список избранных команд, и
// каждый новый пункт отжимает его вниз. Вкладка «Сервисы» рисуется только
// администратору — /api/logs на бэкенде admin-only и остаётся таким; оператор
// попадает сразу на «Отказы».
export default function LogsSection() {
  const { t } = useTranslation();
  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<Me>("/api/auth/me"),
  });
  const isAdmin = roleAtLeast(me.data?.user.role, "admin");

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-3 pb-2">
        <h1 className="text-lg font-semibold">{t("nav.logs")}</h1>
      </div>

      {/* Вкладки — ССЫЛКИ, а не кнопки: Ctrl+клик, средняя кнопка мыши и
          «Открыть в новой вкладке» обязаны работать (урок §79). */}
      <nav className="flex items-center gap-1 border-b border-line pb-px">
        {/* «Отказы» первыми и по умолчанию: на пункте меню горит счётчик
            непросмотренных, и клик по нему должен приводить сразу к ним.
            Служебные логи открывают осознанно, а отказы — по сигналу. */}
        <LogsTab to="/logs/rejected" label={t("logs.tabs.rejected")} />
        {isAdmin && <LogsTab to="/logs/services" label={t("logs.tabs.services")} />}
      </nav>

      <div className="min-h-0 flex-1 pt-3">
        <Outlet />
      </div>
    </div>
  );
}

function LogsTab({ to, label }: { to: string; label: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        cn(
          "-mb-px border-b-2 px-3 py-2 text-[13px] transition-colors",
          isActive
            ? "border-accent font-medium text-fg"
            : "border-transparent text-fg-muted hover:text-fg",
        )
      }
    >
      {label}
    </NavLink>
  );
}
