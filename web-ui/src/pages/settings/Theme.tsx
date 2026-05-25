import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

type Theme = "light" | "dark";

const KEY = "databus.theme";

function applyTheme(t: Theme) {
  const root = document.documentElement;
  if (t === "dark") root.classList.add("dark");
  else root.classList.remove("dark");
  localStorage.setItem(KEY, t);
}

export function ThemePanel() {
  const { t } = useTranslation();
  const [theme, setTheme] = useState<Theme>(
    () => (localStorage.getItem(KEY) as Theme) ?? "dark",
  );

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t("settings.theme.title")}</h2>
      <div className="grid grid-cols-2 gap-3 max-w-md">
        {(["light", "dark"] as const).map((mode) => (
          <button
            key={mode}
            onClick={() => setTheme(mode)}
            className={`px-4 py-6 rounded-md border ${
              theme === mode
                ? "border-accent bg-accent/10 text-accent"
                : "border-bg-muted hover:border-fg-muted"
            }`}
          >
            <div className="text-2xl mb-1">{mode === "light" ? "☀" : "🌙"}</div>
            <div className="text-sm">{t(`settings.theme.${mode}`)}</div>
          </button>
        ))}
      </div>
    </div>
  );
}
