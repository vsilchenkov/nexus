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

// Toggle — бинарный переключатель (switch). label выводится справа от
// трека. disabled гасит контрол (например, вложенные настройки логирования
// при выключенном мастер-тумблере).
export function Toggle({
  checked,
  onChange,
  label,
  disabled,
  className,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: ReactNode;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <label
      className={cn(
        "inline-flex cursor-pointer select-none items-center gap-2 text-[13px] text-fg",
        disabled && "cursor-not-allowed opacity-50",
        className,
      )}
    >
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => !disabled && onChange(!checked)}
        className={cn(
          "relative h-5 w-9 shrink-0 rounded-full transition-colors",
          checked ? "bg-accent" : "bg-line-strong",
        )}
      >
        <span
          className={cn(
            // left-0.5 фиксирует базу бегунка у левого края трека; без явного
            // left отсчёт идёт от статической позиции (≈центр трека), и
            // translate-x-[18px] выгонял бегунок за трек на текст подписи.
            "absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform",
            checked ? "translate-x-4" : "translate-x-0",
          )}
        />
      </button>
      {label && <span>{label}</span>}
    </label>
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
