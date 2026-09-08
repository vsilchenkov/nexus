import { forwardRef, type ButtonHTMLAttributes } from "react";

import { buttonClasses, type ButtonVariant } from "./buttonClasses";

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  sm?: boolean;
};

// Button — кнопка по эталону (§21): варианты primary/danger/ghost/default + sm.
// Облик берётся из buttonClasses — той же функции, которой пользуются ссылки,
// выглядящие кнопками (переход по адресу обязан быть ссылкой, §79.3).
export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = "default", sm = false, className, type = "button", ...rest },
  ref,
) {
  return <button ref={ref} type={type} className={buttonClasses({ variant, sm, className })} {...rest} />;
});
