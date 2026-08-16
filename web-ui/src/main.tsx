import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClientProvider } from "@tanstack/react-query";

import App from "./App";
import { ConfirmProvider } from "./components/ui";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { applyTheme, getTheme } from "./lib/theme";
import { queryClient } from "./lib/queryClient";
import "./i18n";
// §89.1: локальные @font-face (Inter + JetBrains Mono). Импорт из TS, а не
// `@import` в globals.css: CSS требует, чтобы @import стоял ПЕРЕД любыми
// другими правилами, то есть выше @tailwind base — ловушка на ровном месте.
// Порядок в бандле для @font-face безразличен, специфичности у него нет.
import "./assets/fonts/fonts.css";
import "./styles/globals.css";

// Применяем сохранённую тему до первого render'а (избегаем flash-of-light).
applyTheme(getTheme());

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <ConfirmProvider>
            <App />
          </ConfirmProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </ErrorBoundary>
  </React.StrictMode>,
);
