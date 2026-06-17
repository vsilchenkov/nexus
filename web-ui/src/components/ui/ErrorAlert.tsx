import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle } from "lucide-react";

import { cn } from "../../lib/cn";

// ErrorAlert — единый блок ошибки (Phase AUD.8): до него каждая страница
// рисовала ошибку по-своему (голый текст, свой div, сырой error.message).
// Без children показывает generic-сообщение common.error.
export function ErrorAlert({ children, className }: { children?: ReactNode; className?: string }) {
  const { t } = useTranslation();
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2 rounded-md bg-err/10 px-3 py-2 text-sm text-err",
        className,
      )}
    >
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 break-words">{children ?? t("common.error")}</div>
    </div>
  );
}
