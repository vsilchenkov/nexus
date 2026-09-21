import { useId, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";

import { cn } from "../../lib/cn";
import { DialogFrame } from "./DialogFrame";

type Props = {
  title: ReactNode;
  subtitle?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
};

// Modal — диалог эталона (.modal-demo): overlay + head/body/foot.
// Закрывается ТОЛЬКО явным действием — Esc, крестик X или кнопки футера
// (§21): клик по подложке НЕ закрывает, иначе случайный клик мимо окна
// молча терял введённые в форму данные (жалоба на диалоги команд/пользователей).
// Семантику диалога (роль, подпись по заголовку, фокус) даёт DialogFrame
// (§104.8); подзаголовок служит описанием окна.
export function Modal({ title, subtitle, onClose, children, footer, className }: Props) {
  const { t } = useTranslation();
  const titleId = useId();
  const subtitleId = useId();

  return (
    <DialogFrame
      onClose={onClose}
      labelledBy={titleId}
      describedBy={subtitle ? subtitleId : undefined}
      className={cn(
        // flex-колонка с ограничением высоты: заголовок/футер фиксированы,
        // тело скроллится (§3 — длинный результат dry-run не выталкивает кнопки).
        "flex max-h-[90vh] w-full max-w-lg flex-col overflow-hidden rounded-lg border border-line-strong bg-app shadow-2xl",
        className,
      )}
    >
      <div className="flex shrink-0 items-center justify-between border-b border-line px-5 py-4">
        <div>
          <h3 id={titleId} className="text-[15px] font-semibold">
            {title}
          </h3>
          {subtitle && (
            <div id={subtitleId} className="mt-0.5 text-xs text-fg-muted">
              {subtitle}
            </div>
          )}
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label={t("common.close")}
          className="grid h-8 w-8 place-items-center rounded-md text-fg-muted hover:bg-bg-muted hover:text-fg"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
      {footer && (
        <div className="flex shrink-0 justify-end gap-2 border-t border-line px-5 py-3.5">{footer}</div>
      )}
    </DialogFrame>
  );
}
