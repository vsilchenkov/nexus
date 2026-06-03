---
name: nexus-web-i18n
description: Internationalization for the Nexus web-ui — react-i18next + i18next with two locale bundles (locales/en.json, locales/ru.json) that must stay in sync, nested key namespaces, the useTranslation hook, and backend i18n parity (Go platform/i18n for server-returned error keys). Use when adding or changing any user-facing string, adding a locale key, translating a new page, or wiring server error messages to translations in web-ui.
---

# Nexus web-ui — i18n (react-i18next)

The SPA is bilingual (**en** / **ru**). Setup is in [src/i18n.ts](web-ui/src/i18n.ts): i18next +
`initReactI18next` + browser language detector, `fallbackLng: "en"`,
`supportedLngs: ["en", "ru"]`, language cached in `localStorage`. Bundles live in
[src/locales/en.json](web-ui/src/locales/en.json) and
[src/locales/ru.json](web-ui/src/locales/ru.json) under a single `translation` namespace.

## The cardinal rule: en.json and ru.json stay in sync

**Every key must exist in BOTH files** with the same nested path. Adding a string to `en.json` and
forgetting `ru.json` (or vice versa) means one language silently falls back to English (or shows the
raw key). When you add/rename/remove a key, do it in both files in the same edit.

## Keys are nested namespaces

Group by feature/domain, not flat:

```jsonc
{
  "app": { "title": "Nexus" },
  "ch_template": {
    "not_found": "clickhouse template not found",
    "in_use": "the template is used by nodes and cannot be deleted"
  },
  "auth": { /* … */ }
}
```

- Reference with dotted paths: `t("ch_template.in_use")`.
- Pick the existing top-level namespace for the feature you're touching (`auth`, `node`,
  `ch_template`, `settings`, …); only create a new namespace for a genuinely new feature area.
- Keep keys descriptive and stable; the **value** is the translatable copy, the **key** is the
  contract — don't churn keys for wording tweaks.

## Using translations in components

```tsx
import { useTranslation } from "react-i18next";

function Panel() {
  const { t } = useTranslation();
  return <h2>{t("settings.api_tokens.title")}</h2>;
}
```

- Interpolation: `t("node.deleted", { name })` with `"deleted": "node {{name}} deleted"`.
  `escapeValue` is off (React already escapes), so don't double-escape.
- Switch language via `i18n.changeLanguage("ru")` (see the Language settings tab); it persists to
  `localStorage`.
- No hard-coded user-facing English in JSX. (Some legacy panels still inline strings — when you edit
  one, migrate the strings you touch into the locale files.)

## Backend parity — server returns i18n *keys*

This is a full-stack contract. The Go services return **stable error keys**, not localized prose
(see the project `CLAUDE.md`: a new i18n key goes in **both** `internal/platform/i18n/i18n.go` and
`web-ui/src/locales/`). When a server response carries an error key (e.g. `ch_template.in_use`):

- Add the matching key to both `en.json` and `ru.json` so the UI can render it via `t(key)`.
- The key string must be **identical** on both sides — the backend key is the join point.
- When you add a server-side validation error, add its UI translations in the same change set, or
  the user sees a raw key.

## Checklist when touching strings

- [ ] Key added/changed in **both** `en.json` and `ru.json`.
- [ ] Dotted path matches an existing namespace where appropriate.
- [ ] If the string mirrors a backend error key, the Go `i18n.go` side has the same key.
- [ ] No raw user-facing string left inline in the JSX you edited.
- [ ] Rebuild the embedded bundle before committing (see `nexus-web-overview`).