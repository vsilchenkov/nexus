import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Конфиг Vitest (отдельный от vite.config.ts, чтобы не влиять на прод-сборку).
// jsdom — для RTL-тестов компонентов; globals — describe/it/expect без импорта.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  test: {
    environment: "jsdom",
    globals: true,
  },
});
