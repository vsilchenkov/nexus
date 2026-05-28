import { Routes, Route, Navigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import Login from "./pages/Login";
import Overview from "./pages/Overview";
import NodeDetail from "./pages/NodeDetail";
import NodeSettings from "./pages/NodeSettings";
import AuditLog from "./pages/AuditLog";
import Settings from "./pages/Settings";
import { AppShell } from "./components/AppShell";
import { api } from "./api/client";

// useMe — проверка текущей сессии через /api/auth/me.
// При 401 (isError) пользователь будет редиректнут на /login.
function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<{ user: { user_id: string; role: string } }>("/api/auth/me"),
  });
}

// Protected — гейт сессии + общий каркас (AppShell с сайдбаром и топбаром).
function Protected() {
  const { isLoading, isError, data } = useMe();
  if (isLoading) return <div className="grid h-screen place-items-center text-fg-muted">Loading…</div>;
  if (isError || !data) return <Navigate to="/login" replace />;
  return <AppShell />;
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
        <Route path="/audit" element={<AuditLog />} />
        <Route path="/settings/*" element={<Settings />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
