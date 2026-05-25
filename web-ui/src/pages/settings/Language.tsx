import { useTranslation } from "react-i18next";

export function LanguagePanel() {
  const { i18n } = useTranslation();
  const current = i18n.language.startsWith("ru") ? "ru" : "en";

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">language</h2>
      <div className="grid grid-cols-2 gap-3 max-w-md">
        {["en", "ru"].map((lng) => (
          <button
            key={lng}
            onClick={() => i18n.changeLanguage(lng)}
            className={`px-4 py-6 rounded-md border ${
              current === lng
                ? "border-accent bg-accent/10 text-accent"
                : "border-bg-muted hover:border-fg-muted"
            }`}
          >
            <div className="text-2xl mb-1">{lng === "en" ? "🇬🇧" : "🇷🇺"}</div>
            <div className="text-sm">{lng === "en" ? "English" : "Русский"}</div>
          </button>
        ))}
      </div>
      <p className="text-fg-muted text-sm">
        applies to UI only; logs in ClickHouse, stderr, and Prometheus stay in English.
      </p>
    </div>
  );
}
