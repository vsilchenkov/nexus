import { type ReactNode } from "react";
import { Check } from "lucide-react";

import { cn } from "../../lib/cn";

export type PickOption<T extends string> = {
  value: T;
  title: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
};

// PickGroup — выбор карточками-радио эталона (.pick). cols — 1 или 2 колонки.
export function PickGroup<T extends string>({
  value,
  options,
  onChange,
  cols = 2,
  className,
}: {
  value: T;
  options: PickOption<T>[];
  onChange: (v: T) => void;
  cols?: 1 | 2;
  className?: string;
}) {
  return (
    <div
      className={cn("grid gap-2.5", cols === 2 ? "grid-cols-2" : "grid-cols-1", className)}
    >
      {options.map((o) => {
        const sel = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            onClick={() => onChange(o.value)}
            className={cn(
              "relative rounded-md border p-3 text-left transition-colors",
              sel ? "border-accent bg-accent/10" : "border-line hover:border-line-strong",
            )}
          >
            <div
              className={cn(
                "mb-0.5 flex items-center gap-1.5 text-[13px] font-medium",
                sel ? "text-accent" : "text-fg",
              )}
            >
              {o.icon}
              {o.title}
            </div>
            {o.description && (
              <div className={cn("text-[11px] leading-snug", sel ? "text-accent/80" : "text-fg-muted")}>
                {o.description}
              </div>
            )}
            {sel && <Check className="absolute right-3 top-3 h-3.5 w-3.5 text-accent" />}
          </button>
        );
      })}
    </div>
  );
}

// Toggle3 — переключатель равных сегментов эталона (.toggle3), напр. состояние узла.
export function Toggle3<T extends string>({
  value,
  options,
  onChange,
  className,
}: {
  value: T;
  options: { value: T; label: ReactNode }[];
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <div className={cn("flex gap-2", className)}>
      {options.map((o) => {
        const sel = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            onClick={() => onChange(o.value)}
            className={cn(
              "flex-1 rounded-md border px-2 py-2 text-center text-xs transition-colors",
              sel
                ? "border-accent bg-accent/10 font-medium text-accent"
                : "border-line text-fg-muted hover:text-fg",
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
