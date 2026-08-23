import { Routes, Route, Navigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import Login from "./pages/Login";
import ResetPassword from "./pages/ResetPassword";
import Overview from "./pages/Overview";
import NodeDetail from "./pages/NodeDetail";
import NodeSettings from "./pages/NodeSettings";
import AuditLog from "./pages/AuditLog";
import KafkaMonitor from "./pages/KafkaMonitor";
import LogsSection from "./pages/Logs";
import ServiceLogsTab from "./pages/logs/ServiceLogsTab";
import RejectedTab from "./pages/logs/RejectedTab";
import Settings from "./pages/Settings";
import ForcePasswordChange from "./pages/ForcePasswordChange";
import { AppShell } from "./components/AppShell";
import { api } from "./api/client";
import { roleAtLeast } from "./lib/roles";
import { useDocumentTitle } from "./lib/useDocumentTitle";

// useMe — проверка текущей сессии через /api/auth/me.
// При 401 (isError) пользователь будет редиректнут на /login.
function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () =>
      api.get<{ user: { user_id: string; role: string; must_change_password?: boolean } }>(
        "/api/auth/me",
      ),
  });
}

// Protected — гейт сессии + общий каркас (AppShell с сайдбаром и топбаром).
// Если у пользователя стоит must_change_password (П18), показываем обязательный
// экран смены пароля вместо приложения — бэкенд всё равно блокирует API 403.
function Protected() {
  const { isLoading, isError, data } = useMe();
  if (isLoading) return <div className="grid h-screen place-items-center text-fg-muted">Loading…</div>;
  if (isError || !data) return <Navigate to="/login" replace />;
  if (data.user.must_change_password) return <ForcePasswordChange />;
  return <AppShell />;
}

// AuditRoute — журнал действий доступен operator+ (§26, П6; §87 — оператору
// журнал нужен, чтобы понять, кто и что менял до инцидента). Viewer, открывший
// /audit по прямой ссылке, редиректится на список узлов (пункт меню для него
// скрыт, бэкенд всё равно вернул бы 403).
function AuditRoute() {
  const { data } = useMe();
  if (data && !roleAtLeast(data.user.role, "operator")) return <Navigate to="/" replace />;
  return <AuditLog />;
}

// LogsRoute — раздел «Логи» (§94.7). Доступен operator+: внутри две вкладки, и
// «Отказы» нужны оператору и менеджеру для своей команды. Viewer, открывший
// /logs по прямой ссылке, редиректится на список узлов.
function LogsRoute() {
  const { data } = useMe();
  if (data && !roleAtLeast(data.user.role, "operator")) return <Navigate to="/" replace />;
  return <LogsSection />;
}

// LogsIndexRoute — /logs открывает «Отказы», причём ВСЕМ, включая
// администратора.
//
// Раньше администратора уводило на служебные логи — «привычнее». Но на пункте
// меню горит счётчик непросмотренных отказов, и клик по горящему счётчику
// приводил не туда, куда он зовёт: приходилось делать второй переход. Служебные
// логи открывают осознанно, отказы — по сигналу, поэтому первой стоит вкладка,
// которая этот сигнал и породила.
function LogsIndexRoute() {
  const { data } = useMe();
  if (!data) return null;
  return <Navigate to="/logs/rejected" replace />;
}

// ServiceLogsRoute — консоль служебных логов (§51) остаётся admin-only: бэкенд
// вернул бы 403 на /api/logs, поэтому прямой заход уводим на «Отказы», а не
// показываем пустой экран с ошибкой.
function ServiceLogsRoute() {
  const { data } = useMe();
  if (data && !roleAtLeast(data.user.role, "admin")) return <Navigate to="/logs/rejected" replace />;
  return <ServiceLogsTab />;
}

export default function App() {
  // §89.2: код ноды в заголовке вкладки. Здесь, а не в AppShell: /api/version
  // публичный, и вкладка ЛОГИНА обязана называться так же — иначе при
  // нескольких открытых нодах вкладки «Nexus» неотличимы ровно в тот момент,
  // когда человек выбирает, куда вводить пароль.
  useDocumentTitle();

  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      {/* §88.8.3: публичная страница задания нового пароля по ссылке из
          письма. Объявлена ЯВНО: catch-all ниже иначе увёл бы прямой переход
          из письма на «/». */}
      <Route path="/reset-password" element={<ResetPassword />} />
      <Route element={<Protected />}>
        <Route path="/" element={<Overview />} />
        <Route path="/nodes/new" element={<NodeSettings />} />
        <Route path="/nodes/:id" element={<NodeDetail />} />
        <Route path="/nodes/:id/edit" element={<NodeSettings />} />
        <Route path="/audit" element={<AuditRoute />} />
        <Route path="/kafka" element={<KafkaMonitor />} />
        <Route path="/logs" element={<LogsRoute />}>
          <Route index element={<LogsIndexRoute />} />
          <Route path="services" element={<ServiceLogsRoute />} />
          <Route path="rejected" element={<RejectedTab />} />
        </Route>
        <Route path="/settings/*" element={<Settings />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
