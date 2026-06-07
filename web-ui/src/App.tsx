import { Routes, Route, Navigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import Login from "./pages/Login";
import Overview from "./pages/Overview";
import NodeDetail from "./pages/NodeDetail";
import NodeSettings from "./pages/NodeSettings";
import AuditLog from "./pages/AuditLog";
import KafkaMonitor from "./pages/KafkaMonitor";
import Settings from "./pages/Settings";
import ForcePasswordChange from "./pages/ForcePasswordChange";
import { AppShell } from "./components/AppShell";
import { api } from "./api/client";
import { roleAtLeast } from "./lib/roles";

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

// AuditRoute — журнал действий доступен только manager+ (§26, П6). Viewer,
// открывший /audit по прямой ссылке, редиректится на список узлов (пункт меню
// для него скрыт, бэкенд всё равно вернул бы 403).
function AuditRoute() {
  const { data } = useMe();
  if (data && !roleAtLeast(data.user.role, "manager")) return <Navigate to="/" replace />;
  return <AuditLog />;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route element={<Protected />}>
        <Route path="/" element={<Overview />} />
        <Route path="/nodes/new" element={<NodeSettings />} />
        <Route path="/nodes/:id" element={<NodeDetail />} />
        <Route path="/nodes/:id/edit" element={<NodeSettings />} />
        <Route path="/audit" element={<AuditRoute />} />
        <Route path="/kafka" element={<KafkaMonitor />} />
        <Route path="/settings/*" element={<Settings />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
