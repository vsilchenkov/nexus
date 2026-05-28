import { useTranslation } from "react-i18next";
import { Info } from "lucide-react";

import { Card, Hint } from "../../components/ui";
import { cn } from "../../lib/cn";

const LANGS = [
  { code: "en", title: "English", desc: "Default" },
  { code: "ru", title: "Русский", desc: "Русский интерфейс" },
] as const;

// LanguagePanel — переключатель UI-языка карточками с флагами (§7.11).
// Сохраняется в localStorage через i18next-browser-languagedetector.
export function LanguagePanel() {
  const { t, i18n } = useTranslation();
  const current = i18n.language.startsWith("ru") ? "ru" : "en";

  return (
    <Card className="max-w-xl">
      <div className="text-sm font-semibold">{t("settings.language.title")}</div>
      <div className="mb-3.5 mt-1 text-xs text-fg-muted">{t("settings.language.subtitle")}</div>
      <div className="grid grid-cols-2 gap-2.5">
        {LANGS.map((lng) => {
          const sel = current === lng.code;
          return (
            <button
              key={lng.code}
              type="button"
              onClick={() => i18n.changeLanguage(lng.code)}
              className={cn(
                "flex items-center gap-3 rounded-md border p-3 text-left transition-colors",
                sel ? "border-accent bg-accent/10" : "border-line hover:border-line-strong",
              )}
            >
              <Flag code={lng.code} />
              <div>
                <div className={cn("text-[13px] font-medium", sel && "text-accent")}>{lng.title}</div>
                <div className="text-[11px] text-fg-muted">{lng.desc}</div>
              </div>
            </button>
          );
        })}
      </div>
      <Hint tone="muted" icon={<Info className="h-3.5 w-3.5" />} className="mt-3">
        {t("settings.language.note")}
      </Hint>
    </Card>
  );
}

function Flag({ code }: { code: string }) {
  if (code === "ru") {
    return (
      <svg className="h-[22px] w-[30px] shrink-0 rounded-sm" viewBox="0 0 60 36">
        <rect width="60" height="12" fill="#fff" />
        <rect y="12" width="60" height="12" fill="#0039A6" />
        <rect y="24" width="60" height="12" fill="#D52B1E" />
      </svg>
    );
  }
  return (
    <svg className="h-[22px] w-[30px] shrink-0 rounded-sm" viewBox="0 0 60 36">
      <rect width="60" height="36" fill="#012169" />
      <path d="M0,0 L60,36 M60,0 L0,36" stroke="#fff" strokeWidth="7" />
      <path d="M0,0 L60,36 M60,0 L0,36" stroke="#C8102E" strokeWidth="3" />
      <path d="M30,0 V36 M0,18 H60" stroke="#fff" strokeWidth="10" />
      <path d="M30,0 V36 M0,18 H60" stroke="#C8102E" strokeWidth="6" />
    </svg>
  );
}
