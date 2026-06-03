import {
  forwardRef,
  type InputHTMLAttributes,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
  type ReactNode,
} from "react";

import { cn } from "../../lib/cn";

const fieldBase =
  "w-full bg-app border border-line rounded-md text-[13px] text-fg " +
  "placeholder:text-fg-subtle focus:outline-none focus:border-accent disabled:opacity-50";

type InputProps = InputHTMLAttributes<HTMLInputElement> & { mono?: boolean };

// Input — текстовое поле эталона (.in). mono — моноширинный вариант.
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, mono, ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      className={cn(fieldBase, "h-9 px-3", mono && "font-mono text-xs", className)}
      {...rest}
    />
  );
});

type SelectProps = SelectHTMLAttributes<HTMLSelectElement>;

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, children, ...rest },
  ref,
) {
  return (
    <select ref={ref} className={cn(fieldBase, "h-9 px-2.5", className)} {...rest}>
      {children}
    </select>
  );
});

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean };

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  function Textarea({ className, mono = true, ...rest }, ref) {
    return (
      <textarea
        ref={ref}
        className={cn(
          fieldBase,
          "px-3 py-2 leading-relaxed resize-y",
          mono && "font-mono text-xs",
          className,
        )}
        {...rest}
      />
    );
  },
);

// Field — подпись + контрол (label.fld эталона).
export function Field({
  label,
  hint,
  children,
  className,
}: {
  label?: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      {label && (
        <label className="block text-xs text-fg-muted">
          {label}
          {hint && <span className="text-fg-subtle"> · {hint}</span>}
        </label>
      )}
      {children}
    </div>
  );
}
