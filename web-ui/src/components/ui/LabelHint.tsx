import { HelpCircle } from "lucide-react";
import { type ReactNode } from "react";

import { cn } from "../../lib/cn";
import { Tooltip } from "./Tooltip";

// LabelHint — info-иконка «?» рядом с заголовком/меткой: поясняющий тултип по
// наведению/фокусу (§25). Иконка приглушённая, курсор-help. span с tabIndex —
// чтобы тултип открывался и по фокусу с клавиатуры (SVG сам не фокусируется).
export function LabelHint({
  content,
  side = "top",
  className,
}: {
  content: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  className?: string;
}) {
  return (
    <Tooltip content={content} side={side}>
      <span
        tabIndex={0}
        aria-label="info"
        className={cn(
          "inline-flex cursor-help text-fg-subtle transition-colors hover:text-fg-muted focus-visible:text-fg-muted focus-visible:outline-none",
          className,
        )}
      >
        <HelpCircle className="h-3.5 w-3.5" />
      </span>
    </Tooltip>
  );
}
