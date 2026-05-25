/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        bg: { DEFAULT: "#0f1116", elev: "#171922", muted: "#1f2330" },
        fg: { DEFAULT: "#e3e8f0", muted: "#8d95a8", subtle: "#5b6273" },
        accent: { DEFAULT: "#5b8def", hover: "#7aa4ff" },
        ok: "#3ecf8e",
        warn: "#f5a524",
        err: "#ef4444",
      },
    },
  },
  plugins: [],
};
