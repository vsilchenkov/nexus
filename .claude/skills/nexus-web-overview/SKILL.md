---
name: nexus-web-overview
description: Orientation for the Nexus web-ui SPA (React 18 + Vite + TypeScript + Tailwind + react-query + react-i18next). Covers project layout, how the SPA is wired (QueryClient/Router/ErrorBoundary/i18n), the embed-and-rebuild contract (bundle must be rebuilt and internal/web/static committed), and the CI quality gates. Use when starting any work in web-ui/, adding a page or component, wiring routes, or before committing front-end changes.
---

# Nexus web-ui — orientation

The front end lives in [web-ui/](web-ui/) and is a single-page app served by the Go **Web Service**
from `internal/web/static/` via `embed.FS`. There is no separate Node server in production — the
SPA is a static bundle baked into the Go binary.

Stack (see [web-ui/package.json](web-ui/package.json)):

- **React 18.3** + **react-dom**, **TypeScript 5.4** (strict), **Vite 5** bundler.
- **@tanstack/react-query 5** — the entire data layer. See `nexus-web-data`.
- **axios** behind a thin `api` wrapper in [web-ui/src/api/client.ts](web-ui/src/api/client.ts).
- **react-router-dom 6** — routing.
- **Tailwind 3** + `clsx` + `tailwind-merge` (`cn`) for styling. See `nexus-web-components`.
- **react-i18next** + `i18next` with `en.json` / `ru.json`. See `nexus-web-i18n`.
- **@radix-ui** (popover, tooltip) + **cmdk** (combobox), wrapped in `components/ui/`.
- **react-hook-form** / **zod** / **@hookform/resolvers** are in `package.json` but **not used** in
  `src/` — forms are controlled `useState` inputs. Don't introduce RHF without asking.
- **vitest** + **@testing-library/react** + **jsdom** are configured, but **no tests exist yet**.
  See `nexus-web-testing`.

## Layout (`web-ui/src/`)

| Dir              | Holds                                                                 |
|------------------|----------------------------------------------------------------------|
| `api/`           | `client.ts` — the `api` wrapper + all shared response `type`s.        |
| `lib/`           | utility hooks & helpers: `cn`, `format`, `roles`, `theme`, `period`, `useCurrentRole`. |
| `components/`    | shared composite components (`AppShell`, `Sidebar`, `Topbar`, dialogs). |
| `components/ui/` | the design-system atoms (`Button`, `Input`, `Modal`, …), re-exported from `ui/index.ts`. |
| `components/node/`| node-feature components & hooks (tabs, RabbitMQ section, fields).    |
| `pages/`         | route-level pages; `pages/settings/` are the settings sub-tabs.       |
| `locales/`       | `en.json` + `ru.json` (must stay in sync).                            |
| `styles/`        | `globals.css` (CSS variables for the Tailwind token palette).         |

## How it's wired

- [src/main.tsx](web-ui/src/main.tsx): `StrictMode → ErrorBoundary → QueryClientProvider →
  BrowserRouter → App`. The `QueryClient` is configured once here (`staleTime: 30s`, no retry on
  401/403 — the axios interceptor redirects to `/login`). Theme is applied before first render to
  avoid a flash.
- [src/App.tsx](web-ui/src/App.tsx): routes. `Protected` gates on `GET /api/auth/me` and renders
  `AppShell` (sidebar + topbar + `<Outlet/>`). Add new pages here.
- Path alias `@/*` → `src/*` is configured in `vite.config.ts` and `tsconfig.json`, but existing
  code uses **relative imports**. Match the surrounding file.

## Adding a page (checklist)

1. Create `pages/MyPage.tsx` (or `pages/settings/MyTab.tsx`).
2. Register the route in [App.tsx](web-ui/src/App.tsx) (under `Protected` if it needs a session).
3. Add a nav entry in `Sidebar.tsx` / `Topbar.tsx` if user-reachable.
4. Add every visible string to **both** `locales/en.json` and `locales/ru.json` (`nexus-web-i18n`).
5. Fetch data via react-query + the `api` wrapper (`nexus-web-data`), style with the UI-kit + `cn`
   (`nexus-web-components`).

## The embed-and-rebuild contract — do not skip

The browser is served the **built** bundle from `internal/web/static/`, not your `src/` edits. A
change to `web-ui/src/` does **not** reach the user until the bundle is rebuilt and the regenerated
`internal/web/static/` is committed **in the same commit**.

```
cd web-ui && npm run build        # tsc -b && vite build → web-ui/dist/
# then copy dist → internal/web/static (the repo's `make build-ui` does both steps)
```

Then `go build ./cmd/web` and **commit the changed `internal/web/static/`** (asset names are
hashed — the old `index-<hash>.js` is replaced). Skipping this = "the feature is in `dev` but not in
the UI." This has bitten the project before (see the project's `CLAUDE.md` and the
`fix(ui): пересборка встроенного SPA` commit).

## CI gates (must pass before push)

- `cd web-ui && npm run lint` — ESLint flat config runs with **`--max-warnings=0`** in CI
  ([.gitlab-ci.yml](.gitlab-ci.yml) job `ui-build`). A single warning (e.g. a
  `react-refresh/only-export-components` or unused-var) fails the pipeline.
- `npm run build` — `tsc -b` typecheck (strict, `noUnusedLocals`/`noUnusedParameters`) + `vite build`.
- If you changed dependencies, commit `package-lock.json` too, or `npm ci` breaks in CI.
- There is no separate `npm run test` gate today (no tests exist), but `vitest run` is the command
  when you add them.