import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";

type Props = {
  /** Текст, который копируется в буфер обмена. */
  value: string;
  className?: string;
  /** Подпись рядом с иконкой (опционально). */
  label?: string;
};

// CopyButton — кнопка «Скопировать» с иконкой (§2). Копирует value в буфер
// обмена через navigator.clipboard и на ~1.5с показывает галочку-подтверждение.
// Есть graceful fallback на document.execCommand для не-secure контекстов.
export function CopyButton({ value, className, label }: Props) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        const ta = document.createElement("textarea");
        ta.value = value;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* буфер недоступен — молча игнорируем, пользователь скопирует вручную */
    }
  }

  const title = copied ? t("common.copied") : t("common.copy");
  return (
    <button
      type="button"
      onClick={copy}
      title={title}
      aria-label={title}
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded border border-line px-1.5 py-1",
        "text-fg-muted transition-colors hover:bg-bg-muted hover:text-fg",
        className,
      )}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-ok" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
      {label && <span className="text-[11px]">{label}</span>}
    </button>
  );
}
