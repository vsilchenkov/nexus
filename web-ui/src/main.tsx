import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import App from "./App";
import { ConfirmProvider } from "./components/ui";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { applyTheme, getTheme } from "./lib/theme";
import "./i18n";
import "./styles/globals.css";

// Применяем сохранённую тему до первого render'а (избегаем flash-of-light).
applyTheme(getTheme());

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error: any) => {
        // Не retry-им 401/403 — пусть глобальный interceptor редиректит на login.
        const status = error?.response?.status;
        if (status === 401 || status === 403) return false;
        return failureCount < 2;
      },
      staleTime: 30_000,
    },
  },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={qc}>
        <BrowserRouter>
          <ConfirmProvider>
            <App />
          </ConfirmProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </ErrorBoundary>
  </React.StrictMode>,
);
