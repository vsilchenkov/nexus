import { useEffect, type ReactNode } from "react";
import { X } from "lucide-react";

import { cn } from "../../lib/cn";

type Props = {
  title: ReactNode;
  subtitle?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
};

// Modal — диалог эталона (.modal-demo): overlay + head/body/foot,
// закрытие по Esc и клику вне окна.
export function Modal({ title, subtitle, onClose, children, footer, className }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className={cn(
          "w-full max-w-lg overflow-hidden rounded-lg border border-line-strong bg-app shadow-2xl",
          className,
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-line px-5 py-4">
          <div>
            <h3 className="text-[15px] font-semibold">{title}</h3>
            {subtitle && <div className="mt-0.5 text-xs text-fg-muted">{subtitle}</div>}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="grid h-8 w-8 place-items-center rounded-md text-fg-muted hover:bg-bg-muted hover:text-fg"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="px-5 py-4">{children}</div>
        {footer && (
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3.5">
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}
