import { Routes, Route, Navigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import Login from "./pages/Login";
import Overview from "./pages/Overview";
import NodeDetail from "./pages/NodeDetail";
import { api } from "./api/client";

// useMe — проверка текущей сессии через /api/auth/me.
// При 401 (isError) пользователь будет редиректнут на /login.
function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<{ user: { user_id: string; role: string } }>("/api/auth/me"),
  });
}

function Protected({ children }: { children: React.ReactNode }) {
  const { data, isLoading, isError } = useMe();
  if (isLoading) return <div className="p-8 text-fg-muted">Loading…</div>;
  if (isError || !data) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <Protected>
            <Overview />
          </Protected>
        }
      />
      <Route
        path="/nodes/:id"
        element={
          <Protected>
            <NodeDetail />
          </Protected>
        }
      />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
