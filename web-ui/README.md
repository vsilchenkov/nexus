# web-ui

SPA-фронтенд DataBus. **Полная реализация — Phase 3 frontend (см. план).**

Сейчас в проекте только REST API; index.html-заглушка с документацией
эндпоинтов лежит в [internal/web/static/index.html](../internal/web/static/index.html)
и встроена в бинарь Web через `embed.FS`.

## Технологический стек (по §17.5–17.6 ТЗ)

- React 18 + TypeScript
- Vite (dev-server и сборка)
- TanStack Query (server state, кеширование, инвалидация)
- React Router 6 (routing)
- shadcn/ui + Tailwind CSS (компонентная библиотека)
- Lucide icons
- react-i18next (en / ru)
- react-hook-form + zod (формы и валидация)
- Vitest + React Testing Library + Playwright (тесты)

## Структура (когда будет реализован)

```
/web-ui
  package.json
  vite.config.ts
  tsconfig.json
  tailwind.config.js
  /public
  /src
    main.tsx
    App.tsx
    /api          # HTTP-клиент, типы (генерируются из swagger через openapi-typescript)
    /pages        # Login, Overview, NodeDetail, NodeSettings, Settings/*, AuditLog
    /components   # UI-примитивы (shadcn) + кастомные (NodeCard, LogTable, ...)
    /hooks        # useNodes, useLogs, useLiveTail (SSE), useAuth
    /lib          # форматирование, валидация
    /locales      # en.json, ru.json
    /styles
```

## Когда появится

После реализации фронтенда:
1. `cd web-ui && npm install && npm run build`
2. Полученный `web-ui/dist/` копируется в `internal/web/static/`,
   либо меняется `//go:embed` в [static.go](../internal/web/static/static.go).
3. `go build ./cmd/web` — бинарь содержит весь UI.

Dev-режим:
- Go-бэк на :8000 (`make run-web`)
- Vite dev-server на :5173 (`npm run dev` с proxy `/api/* → localhost:8000`)

## Что НЕ делаем в этом проекте

- SSR / Next.js (это админка, SEO не нужен).
- Redux / MobX / Zustand (server state покрывает TanStack Query, local state — useState).
- GraphQL (REST + Swagger закрывают все потребности).
