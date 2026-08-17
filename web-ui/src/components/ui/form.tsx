import {
  forwardRef,
  type InputHTMLAttributes,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
  type ReactNode,
} from "react";

import { cn } from "../../lib/cn";
import { LabelHint } from "./LabelHint";

const fieldBase =
  "w-full bg-app border border-line rounded-md text-[13px] text-fg " +
  "placeholder:text-fg-subtle focus:outline-none focus:border-accent disabled:opacity-50";

type InputProps = InputHTMLAttributes<HTMLInputElement> & { mono?: boolean };

// Input — текстовое поле эталона (.in). mono — моноширинный вариант.
//
// §90.3: autoComplete по умолчанию выключён. В админ-панели почти нет полей,
// куда уместно подставлять личные данные, зато есть поиск и фильтры — на них
// браузер разворачивает попап менеджера паролей, который перекрывает список
// под полем и предлагает вписать чужой логин. Атрибут стоит ДО {...rest},
// поэтому формы входа и смены пароля переопределяют его своим
// `username`/`current-password`/`new-password`.
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, mono, ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      autoComplete="off"
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

// Field — подпись + контрол (label.fld эталона). help — необязательная
// контекстная справка: иконка-«вопросик» с тултипом рядом с лейблом (вне
// <label>, чтобы клик по иконке не фокусировал контрол). hint — короткая
// текстовая приписка через «·», может сосуществовать с help.
export function Field({
  label,
  hint,
  help,
  labelRight,
  children,
  className,
}: {
  label?: ReactNode;
  hint?: ReactNode;
  help?: ReactNode;
  // labelRight — блок у правого края строки подписи (§88.8.1: «Забыли
  // пароль?» напротив «Пароль»). Рендерится РЯДОМ с <label>, а не внутри:
  // вложенный интерактивный элемент делает разметку невалидной (<label> не
  // допускает вложенных labelable-элементов) и ловится jsx-a11y.
  labelRight?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      {label && (
        <div className={cn("flex items-center gap-1", labelRight && "justify-between")}>
          <span className="flex items-center gap-1">
            <label className="block text-xs text-fg-muted">
              {label}
              {hint && <span className="text-fg-subtle"> · {hint}</span>}
            </label>
            {help && <LabelHint content={help} />}
          </span>
          {labelRight}
        </div>
      )}
      {children}
    </div>
  );
}
