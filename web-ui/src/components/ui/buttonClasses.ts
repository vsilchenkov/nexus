import { cn } from "../../lib/cn";

export type ButtonVariant = "default" | "primary" | "danger" | "ghost";

const variants: Record<ButtonVariant, string> = {
  default: "bg-bg-muted border-line-strong text-fg hover:bg-bg-3",
  primary: "bg-accent border-accent text-white hover:bg-accent-hover",
  danger: "bg-err border-err text-white hover:opacity-90",
  ghost: "bg-transparent border-line text-fg hover:bg-bg-muted",
};

/**
 * buttonClasses — облик кнопки для элемента, который кнопкой быть НЕ должен.
 *
 * Нужен там, где действие — это переход по адресу: ссылку нельзя заворачивать в
 * <button> (невалидный HTML, и вложенная кнопка перехватывает клик у ссылки), а
 * выглядеть она обязана как кнопка. Button рисуется через эту же функцию —
 * второй, «похожий» набор классов рядом неизбежно разъехался бы с первым.
 *
 * Отдельный файл, а не экспорт из Button.tsx: eslint-правило
 * react-refresh/only-export-components требует, чтобы файл компонента экспортировал
 * только компоненты.
 */
export function buttonClasses(o?: {
  variant?: ButtonVariant;
  sm?: boolean;
  className?: string;
}): string {
  const { variant = "default", sm = false, className } = o ?? {};
  return cn(
    "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md border font-medium",
    "transition-colors disabled:opacity-50 disabled:cursor-not-allowed",
    sm ? "px-2.5 py-1 text-xs" : "px-3.5 py-2 text-[13px]",
    variants[variant],
    className,
  );
}
