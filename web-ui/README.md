# web-ui

SPA-фронтенд DataBus на React + TypeScript + Vite + Tailwind (§7, §17.5–17.6 ТЗ).

В этом каркасе уже есть:
- роутинг (React Router 6) и проверка сессии через `/api/auth/me`;
- TanStack Query для серверного state'а, с автоматическим редиректом на /login при 401;
- i18n (en/ru) через `react-i18next`, переключатель в шапке;
- три ключевые страницы: Login, Overview (список узлов), NodeDetail (с логами и SSE live-tail);
- Tailwind с тёмной палитрой (`bg`, `fg`, `accent`, `ok`/`warn`/`err`).

Это **скелет**, который покрывает основной поток «вошёл → увидел узлы → открыл узел → посмотрел логи в live-режиме». Полный набор 13 страниц §7 (NodeSettings, диалоги replay/dry-run, Settings → ClickHouse/Users/API Tokens/Sentry/Language/Theme, Audit log, диалог создания ClickHouse-таблицы и т.п.) — следующая итерация фронта.

## Стек

- React 18 + TypeScript + Vite
- TanStack Query (server state)
- React Router 6
- Tailwind CSS (без shadcn-CLI; компоненты — обычные JSX в `src/components/`, при желании можно подключить shadcn/ui позднее)
- Lucide icons
- react-i18next
- react-hook-form + zod (для форм; пока используются на странице Login через useState — формы тяжелее перенесём позже)

## Установка и запуск

```bash
cd web-ui
npm install
npm run dev     # Vite на :5173, /api/* проксируется на :8000
```

В соседнем терминале:
```bash
make run-web    # Go-бэк на :8000
```

## Сборка под Go-бинарь

```bash
cd web-ui
npm run build
cp -r dist/* ../internal/web/static/
cd ..
make build-web
```

Бинарь `bin/web` содержит весь SPA через `embed.FS` (см. [internal/web/static/static.go](../internal/web/static/static.go)).

## Структура

```
/web-ui
  index.html
  package.json, vite.config.ts, tsconfig.json, tailwind.config.js, postcss.config.js
  /src
    main.tsx        # React + QueryClient + Router
    App.tsx         # роуты + защищённый wrapper
    i18n.ts         # react-i18next init
    /api
      client.ts     # axios + interceptor 401→/login
    /pages
      Login.tsx
      Overview.tsx
      NodeDetail.tsx
    /components
      Topbar.tsx
    /locales
      en.json
      ru.json
    /styles
      globals.css
```

## Что НЕ делаем

- SSR / Next.js (это админка, SEO не нужен).
- Redux / MobX / Zustand (server state покрывает TanStack Query, локальный state — `useState`).
- GraphQL (REST + Swagger закрывают все потребности).
