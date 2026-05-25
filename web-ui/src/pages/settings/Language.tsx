import { useTranslation } from "react-i18next";

// LanguagePanel — переключатель UI-языка, сохраняется в localStorage
// через i18next-browser-languagedetector (§7.11).
export function LanguagePanel() {
  const { t, i18n } = useTranslation();
  const current = i18n.language.startsWith("ru") ? "ru" : "en";

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t("settings.language.title")}</h2>
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
      <p className="text-fg-muted text-sm">{t("settings.language.note")}</p>
    </div>
  );
}
