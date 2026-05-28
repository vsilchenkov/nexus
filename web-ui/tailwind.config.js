/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        // CSS-переменные хранят 3 числа "r g b" — это позволяет Tailwind
        // считать opacity (`bg-bg-muted/40`). Палитра — эталон §21.
        app: "rgb(var(--bg-app) / <alpha-value>)",
        bg: {
          DEFAULT: "rgb(var(--bg) / <alpha-value>)",
          elev: "rgb(var(--bg-elev) / <alpha-value>)",
          muted: "rgb(var(--bg-muted) / <alpha-value>)",
          3: "rgb(var(--bg-3) / <alpha-value>)",
        },
        fg: {
          DEFAULT: "rgb(var(--fg) / <alpha-value>)",
          muted: "rgb(var(--fg-muted) / <alpha-value>)",
          subtle: "rgb(var(--fg-subtle) / <alpha-value>)",
        },
        line: {
          DEFAULT: "rgb(var(--line) / <alpha-value>)",
          strong: "rgb(var(--line-strong) / <alpha-value>)",
        },
        accent: {
          DEFAULT: "rgb(var(--accent) / <alpha-value>)",
          hover: "rgb(var(--accent-hover) / <alpha-value>)",
        },
        // info — синоним accent (эталон: text-info = синий акцент).
        info: "rgb(var(--accent) / <alpha-value>)",
        ok: "rgb(var(--ok) / <alpha-value>)",
        warn: "rgb(var(--warn) / <alpha-value>)",
        err: "rgb(var(--err) / <alpha-value>)",
      },
      borderRadius: {
        sm: "var(--radius-sm)",
        md: "var(--radius-md)",
        lg: "var(--radius-lg)",
      },
      fontFamily: {
        sans: "var(--font-sans)",
        mono: "var(--font-mono)",
      },
      spacing: {
        sidebar: "220px",
        header: "56px",
      },
    },
  },
  plugins: [],
};
