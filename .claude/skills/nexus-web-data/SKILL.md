---
name: nexus-web-data
description: Data layer for the Nexus web-ui — @tanstack/react-query v5 (useQuery / useMutation / invalidateQueries) on top of the thin typed `api` wrapper around axios in src/api/client.ts. Covers query keys, cache invalidation, the global 401 redirect, retry policy, and where shared response types live. Use when fetching or mutating server data, adding an endpoint call, wiring a list+create+delete flow, or debugging stale data / refetch behavior in web-ui.
---

# Nexus web-ui — data layer (react-query + `api`)

All server communication goes through the `api` object in
[src/api/client.ts](web-ui/src/api/client.ts), and all state-of-the-server lives in
**@tanstack/react-query v5**. There is no Redux, no global store, no bare `fetch`/`axios` in
components — always the `api` wrapper + a react-query hook.

## The `api` wrapper

`api` is a thin, generic typed facade over a shared `axios` instance (`withCredentials: true`,
30s timeout, JSON):

```ts
api.get<T>(url, params?)   // → Promise<T>  (returns r.data)
api.post<T>(url, body?)    // → Promise<T>
api.put<T>(url, body?)     // → Promise<T>
api.patch<T>(url, body?)   // → Promise<T>
api.del(url)               // → Promise<void>
```

- **Cookies carry auth** — same-origin, the Web Service serves both `/api/*` and the SPA. Never add
  `Authorization` headers in the UI.
- **Global 401 handling** is built into the axios response interceptor: a 401 (when not already on
  `/login`) sets `location.href = "/login"`. Don't re-handle 401 redirects per-call.
- **Shared response types** (`Node`, `OverviewKPI`, `CHTemplate`, `RMQStatus`, …) are declared in
  `client.ts` and imported by features. Add new server DTOs there, next to the related ones, mirror
  the Go JSON tags exactly, and mark optional fields with `?`.

## Queries

```ts
const list = useQuery({
  queryKey: ["tokens"],
  queryFn: () => api.get<ListResp>("/api/tokens"),
});
```

- **Query keys are plain arrays**, most-general segment first: `["me"]`, `["tokens"]`,
  `["node", id]`, `["metrics", "overview"]`. Parameters that change the result (id, period, filters)
  are extra array segments so the cache differentiates them.
- The global `QueryClient` ([main.tsx](web-ui/src/main.tsx)) sets `staleTime: 30_000` and a retry
  policy that **never retries 401/403** (let the interceptor redirect), otherwise up to 2 retries.
  Override `staleTime` per-query for slow-changing data (e.g. `useCurrentRole` uses 5 min — see
  [lib/useCurrentRole.ts](web-ui/src/lib/useCurrentRole.ts)).
- Render the three states explicitly: `isLoading` → skeleton/"Loading…", `isError` → error affordance
  (or `<Navigate to="/login">` for session gates, as in [App.tsx](web-ui/src/App.tsx)), else data.

## Mutations + invalidation

The canonical list/create/revoke/delete shape (from
[pages/settings/ApiTokens.tsx](web-ui/src/pages/settings/ApiTokens.tsx)):

```ts
const qc = useQueryClient();

const create = useMutation({
  mutationFn: () => api.post<CreateResp>("/api/tokens", { name, scopes }),
  onSuccess: (r) => {
    qc.invalidateQueries({ queryKey: ["tokens"] });   // refetch the list
    setShowNew(false);
  },
});

const del = useMutation({
  mutationFn: (id: string) => api.del(`/api/tokens/${id}`),
  onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
});
```

- After any write, **`invalidateQueries({ queryKey: [...] })`** the affected list — the project
  refetches rather than hand-patching the cache. Keep it simple unless a flow proves it needs
  optimistic updates (none today).
- A mutation that takes an argument types `mutationFn: (arg: T) => …` and is called as
  `del.mutate(id)`.
- Surface `mutation.isPending` to disable buttons and `mutation.isError` / `mutation.error` to show
  the failure; map server error messages through i18n where the backend returns an error key
  (see `nexus-web-i18n`).

## Rules

- One hook per server resource; reuse a custom hook (e.g. `useCurrentRole`, `useNodeMetrics`) when
  more than one component needs it, rather than copy-pasting the `useQuery`.
- Keep `queryFn` a one-liner that calls `api.*`. Business logic belongs in the component or a `lib/`
  helper, not in the fetcher.
- Never read or write `document.cookie`, `localStorage` (except theme/i18n which already own it), or
  call endpoints outside the `api` wrapper.