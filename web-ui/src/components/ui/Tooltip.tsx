import * as RadixTooltip from "@radix-ui/react-tooltip";
import { type ReactNode } from "react";

import { cn } from "../../lib/cn";

// TooltipProvider монтируется один раз в корне приложения (AppShell).
export const TooltipProvider = RadixTooltip.Provider;

// Tooltip — простой tooltip с задержкой 400мс по умолчанию (без задержки
// раздражает, §25). delayDuration можно уменьшить для плотных областей (чарт).
export function Tooltip({
  content,
  children,
  side = "bottom",
  className,
  delayDuration = 400,
}: {
  content: ReactNode;
  children: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  className?: string;
  delayDuration?: number;
}) {
  return (
    <RadixTooltip.Root delayDuration={delayDuration}>
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content
          side={side}
          sideOffset={6}
          className={cn(
            "z-50 rounded-sm border border-line-strong bg-bg-3 px-2.5 py-1.5 text-[11px] text-fg shadow-lg",
            className,
          )}
        >
          {content}
          <RadixTooltip.Arrow className="fill-bg-3" />
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  );
}
