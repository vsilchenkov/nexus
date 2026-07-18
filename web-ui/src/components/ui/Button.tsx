import { forwardRef, type ButtonHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

type Variant = "default" | "primary" | "danger" | "ghost";

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  sm?: boolean;
};

const variants: Record<Variant, string> = {
  default: "bg-bg-muted border-line-strong text-fg hover:bg-bg-3",
  primary: "bg-accent border-accent text-white hover:bg-accent-hover",
  danger: "bg-err border-err text-white hover:opacity-90",
  ghost: "bg-transparent border-line text-fg hover:bg-bg-muted",
};

// Button — кнопка по эталону (§21): варианты primary/danger/ghost/default + sm.
export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = "default", sm = false, className, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cn(
        "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md border font-medium",
        "transition-colors disabled:opacity-50 disabled:cursor-not-allowed",
        sm ? "px-2.5 py-1 text-xs" : "px-3.5 py-2 text-[13px]",
        variants[variant],
        className,
      )}
      {...rest}
    />
  );
});
