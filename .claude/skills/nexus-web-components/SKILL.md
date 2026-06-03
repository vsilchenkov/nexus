---
name: nexus-web-components
description: Component & styling conventions for the Nexus web-ui — forwardRef design-system atoms in components/ui, the cn() helper (clsx + tailwind-merge), the semantic Tailwind token palette (bg-app/fg/accent/ok/warn/err driven by CSS variables), Radix + cmdk wrappers, and controlled useState forms (NOT react-hook-form). Use when building or editing a React component, styling with Tailwind, adding a UI atom, picking color tokens, or building a form/dialog in web-ui.
---

# Nexus web-ui — components & styling

## Design-system atoms live in `components/ui/`

Reusable atoms (`Button`, `Input`, `Select`, `Textarea`, `Field`, `Modal`, `Card`, `Chip`, `Pill`,
`Kpi`, `SecretInput`, `CopyButton`, pickers, `TrafficChart`, plus Radix/cmdk wrappers) are defined
in `components/ui/` and re-exported from the barrel
[components/ui/index.ts](web-ui/src/components/ui/index.ts). **Import atoms from the barrel**, build
new shared atoms there, and don't hand-roll a raw `<button>`/`<input>` when an atom exists.

Atom pattern (see [ui/Button.tsx](web-ui/src/components/ui/Button.tsx),
[ui/form.tsx](web-ui/src/components/ui/form.tsx)):

```tsx
import { forwardRef, type ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type Variant = "default" | "primary" | "danger" | "ghost";
type Props = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; sm?: boolean };

const variants: Record<Variant, string> = { /* one class string per variant */ };

export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = "default", sm = false, className, type = "button", ...rest }, ref,
) {
  return <button ref={ref} type={type} className={cn("base classes", variants[variant], className)} {...rest} />;
});
```

Conventions baked in:
- **`forwardRef`** with a named render function so Radix/portals and focus management work.
- Extend the native element props (`ButtonHTMLAttributes<…>`) and **spread `...rest`** so callers
  keep `onClick`, `disabled`, `aria-*`, etc.
- Always accept `className` and merge it **last** through `cn(...)` so callers can override.
- A **variant → class-string `Record`** for visual variants; default `type="button"` on buttons.

## `cn()` and the Tailwind token palette

Compose classes with [`cn`](web-ui/src/lib/cn.ts) (`clsx` for conditionals + `tailwind-merge` to
resolve conflicts). Never template-string class names by hand.

```tsx
className={cn("rounded-md border px-3 py-2", isActive && "bg-accent text-white", className)}
```

**Use the semantic color tokens, not raw Tailwind colors** (no `bg-gray-800`, `text-blue-500`). The
palette is defined in [tailwind.config.js](web-ui/tailwind.config.js) and backed by CSS variables in
`styles/globals.css` (stored as `r g b` triples so opacity modifiers like `bg-bg-muted/40` work).
These tokens also drive light/dark theming (`darkMode: "class"`, toggled via `lib/theme.ts`):

| Token group | Tokens | Use for |
|-------------|--------|---------|
| Surfaces    | `bg-app`, `bg`, `bg-elev`, `bg-muted`, `bg-3` | page/panel/raised backgrounds |
| Text        | `fg`, `fg-muted`, `fg-subtle` | primary / secondary / tertiary text |
| Lines       | `line`, `line-strong` | borders, dividers |
| Accent      | `accent`, `accent-hover`, `info` | primary action, links |
| Status      | `ok`, `warn`, `err` | success / warning / error states |

Layout helpers: `font-mono` for IDs/codes/paths, fixed `spacing.sidebar` (220px) / `spacing.header`
(56px), radius tokens `rounded-sm|md|lg`.

## Forms are controlled `useState` — not react-hook-form

Despite `react-hook-form` / `zod` being in `package.json`, **`src/` uses controlled inputs with
`useState`** (see [pages/settings/ApiTokens.tsx](web-ui/src/pages/settings/ApiTokens.tsx)). Follow
that pattern; do **not** introduce RHF/zod without asking.

```tsx
const [name, setName] = useState("");
<Input value={name} onChange={(e) => setName(e.target.value)} />
```

- Submit via a react-query `useMutation` (see `nexus-web-data`); clear local state in `onSuccess`.
- For checkbox groups, update arrays immutably:
  `setScopes((p) => e.target.checked ? [...p, s] : p.filter((x) => x !== s))`.
- Secrets use the `SecretInput` atom; never log or echo secret values (the backend returns only a
  `*_set` boolean for stored secrets, e.g. `rmq_password_set`).

## Radix + cmdk wrappers

Interactive primitives (popover, tooltip, combobox/command palette) are wrapped once in
`components/ui/` (`Popover`, `Tooltip`, `Command*`) over `@radix-ui/*` and `cmdk`. Compose those
wrappers; don't import the raw Radix/cmdk packages in pages/features.

## Lint discipline (CI fails on warnings)

`npm run lint` runs with `--max-warnings=0`. Common traps:
- `react-hooks/exhaustive-deps` — include every dep or justify with an explicit comment.
- `react-refresh/only-export-components` (warning → CI fail) — keep a component file exporting
  components; move non-component exports to `lib/` or a `*.ts` module.
- `@typescript-eslint/no-unused-vars` — prefix intentionally-unused with `_`.