---
name: nexus-web-testing
description: Front-end testing for the Nexus web-ui with Vitest + @testing-library/react + jsdom (all configured, but no tests exist yet — greenfield). Covers test setup, rendering components inside QueryClientProvider/Router/i18n, querying by role/text, user interaction, and mocking the `api` module rather than axios. Use when adding the first tests, writing or reviewing a *.test.tsx, testing a component/hook, or setting up the vitest environment in web-ui.
---

# Nexus web-ui — testing (Vitest + RTL)

The toolchain is installed — **vitest**, **@testing-library/react**, **jsdom** — and `npm run test`
runs `vitest run`. But **there are no tests in `web-ui/src/` yet**, so adding tests is greenfield:
you'll likely create the vitest config + setup file on the first test.

## One-time setup (do this with the first test)

`vite.config.ts` doesn't yet declare a `test` block. Add the Vitest config (Vitest reads
`vite.config.ts`) with the jsdom environment and a setup file:

```ts
// vite.config.ts — add to defineConfig({...})
test: {
  environment: "jsdom",
  globals: true,
  setupFiles: "./src/test/setup.ts",
},
```

```ts
// src/test/setup.ts
import "@testing-library/jest-dom"; // only if you add this dep; otherwise use vitest matchers
```

> `@testing-library/jest-dom` is **not** currently a dependency. Either add it (devDependency, and
> commit `package-lock.json`) for matchers like `toBeInTheDocument`, or assert with plain Vitest
> (`expect(el).not.toBeNull()`). Pick one and be consistent. Co-locate tests as `Name.test.tsx`
> next to the component.

## Test through the public UI, not internals

Mirror the project's data flow: components fetch via react-query + the `api` wrapper, render with
the UI-kit and i18n. So tests must provide those providers and assert on what the user sees.

```tsx
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import "../i18n";

function renderWithProviders(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}
```

- Build a **fresh `QueryClient` per test** with `retry: false` so failures surface immediately and
  caches don't leak between tests.
- Wrap in `MemoryRouter` for anything using router hooks/links; seed routes with
  `initialEntries={["/nodes/123"]}` when testing a route param.
- Import `../i18n` (or a test-scoped init) so `t(...)` resolves; assert against `en` copy or the key.

## Query the way users perceive the UI

Prefer role/label/text queries over test-ids:

```tsx
await userEvent.click(screen.getByRole("button", { name: /new token/i }));
expect(screen.getByText(/copy this token now/i)).toBeTruthy();
```

- Use `@testing-library/user-event` for interaction (add it as a devDependency if you want it;
  otherwise `fireEvent`).
- Use `findBy*` / `waitFor` for anything that arrives after an async query resolves.

## Mock at the `api` boundary, not axios

Since every component calls the `api` wrapper ([src/api/client.ts](web-ui/src/api/client.ts)), mock
that module — it's the seam:

```tsx
import { vi } from "vitest";
vi.mock("../api/client", () => ({
  api: {
    get: vi.fn().mockResolvedValue({ items: [] }),
    post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn(),
  },
}));
```

- Mocking `api` keeps tests fast and deterministic and avoids fighting axios/jsdom networking.
- For a custom hook (e.g. `useNodeMetrics`, `useCurrentRole`) test it via `renderHook` from RTL
  inside the same providers, asserting on the returned data after the mocked query resolves.

## What's worth testing here

- Stateful flows: list → create → optimistic disable → invalidate (e.g. token creation, node
  enable/disable, allowed-hosts add/remove).
- Conditional affordances gated by role (`useRoleAtLeast`) — that the control is hidden/shown.
- Pure `lib/` helpers (`format`, `period`, `roles`, `nodeUrl`) — easy, high-value unit tests with no
  providers needed.
- Don't assert on Tailwind class strings or internal state; assert on rendered, user-visible output.

## Running

```
cd web-ui && npm run test          # vitest run (CI-style, one shot)
npx vitest                          # watch mode while developing
```

If you add tests to CI, wire `npm run test` into the `ui-build` job alongside lint+build.