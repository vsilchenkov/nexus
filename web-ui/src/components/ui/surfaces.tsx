import { forwardRef, type ReactNode } from "react";

import { cn } from "../../lib/cn";

// Card — карточка эталона (.card).
//
// Проброшена через forwardRef (§86.4): порционная загрузка метрик вешает
// на карточку узла ref-callback IntersectionObserver'а. Обёртка вокруг Card
// сломала бы сетку (h-full у карточки считается от ячейки грида).
export const Card = forwardRef<HTMLDivElement, { children: ReactNode; className?: string }>(
  function Card({ children, className }, ref) {
    return (
      <div ref={ref} className={cn("bg-bg border border-line rounded-lg p-4", className)}>
        {children}
      </div>
    );
  },
);

// SectionHead — заголовок секции формы (.section-head) с иконкой слева.
export function SectionHead({
  icon,
  children,
  className,
}: {
  icon?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-2 text-[13px] font-semibold text-fg mb-3",
        className,
      )}
    >
      {icon && <span className="text-fg-muted">{icon}</span>}
      {children}
    </div>
  );
}
