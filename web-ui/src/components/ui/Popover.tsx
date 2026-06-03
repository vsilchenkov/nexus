import * as RadixPopover from "@radix-ui/react-popover";
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from "react";

import { cn } from "../../lib/cn";

// Popover — обёртка над Radix Popover, стилизованная под UI-kit (§23/§25).
// Доступность (role, aria-expanded, focus-trap) — встроена в Radix.
export const Popover = RadixPopover.Root;
export const PopoverTrigger = RadixPopover.Trigger;
export const PopoverAnchor = RadixPopover.Anchor;

export const PopoverContent = forwardRef<
  ElementRef<typeof RadixPopover.Content>,
  ComponentPropsWithoutRef<typeof RadixPopover.Content>
>(function PopoverContent({ className, align = "end", sideOffset = 8, ...rest }, ref) {
  return (
    <RadixPopover.Portal>
      <RadixPopover.Content
        ref={ref}
        align={align}
        sideOffset={sideOffset}
        className={cn(
          "z-50 rounded-md border border-line-strong bg-bg-elev shadow-2xl outline-none",
          className,
        )}
        {...rest}
      />
    </RadixPopover.Portal>
  );
});
