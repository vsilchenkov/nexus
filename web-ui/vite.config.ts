import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Vite-конфиг dev-сервера + сборки SPA (§17.5 ТЗ).
//   - алиас "@" → src/
//   - dev-прокси /api/* и /metrics → Go-бэк (:8000)
//   - производственный бандл выкладывается в dist/, который потом копируется
//     в internal/web/static/ перед сборкой Go-бинаря.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8000",
      "/swagger": "http://localhost:8000",
      "/metrics": "http://localhost:8000",
      "/health": "http://localhost:8000",
      "/ready": "http://localhost:8000",
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
    chunkSizeWarningLimit: 1000,
    // §89.1: шрифты НИКОГДА не инлайним. Правило Vite — «размер < лимита» без
    // исключений для url() внутри CSS, а jetbrains-mono-*-cyrillic-ext.woff2
    // весит ~1,6 КБ при пороге 4096 — то есть по умолчанию он уехал бы в base64
    // внутрь render-blocking CSS. Цена: +33 % байт, unicode-range перестаёт
    // экономить (все сабсеты качаются до первой отрисовки), и главное — CSP
    // после §89.1 запрещает `font-src data:`, так что инлайн ломал бы шрифт
    // молча. undefined = поведение по умолчанию для всего остального.
    assetsInlineLimit: (file: string) => (file.endsWith(".woff2") ? false : undefined),
  },
});
