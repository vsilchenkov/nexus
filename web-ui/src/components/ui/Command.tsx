import { Command as CommandPrimitive } from "cmdk";
import { Search } from "lucide-react";
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from "react";

import { cn } from "../../lib/cn";

// Command — обёртка над cmdk, стилизованная под UI-kit (§23/§24). Используется
// для combobox'ов формы узла. Серверная фильтрация → передавайте
// shouldFilter={false} и управляйте списком сами.
export const Command = forwardRef<
  ElementRef<typeof CommandPrimitive>,
  ComponentPropsWithoutRef<typeof CommandPrimitive>
>(function Command({ className, ...rest }, ref) {
  return (
    <CommandPrimitive
      ref={ref}
      className={cn("flex w-full flex-col overflow-hidden text-fg", className)}
      {...rest}
    />
  );
});

export const CommandInput = forwardRef<
  ElementRef<typeof CommandPrimitive.Input>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Input>
>(function CommandInput({ className, ...rest }, ref) {
  return (
    <div className="flex h-9 items-center gap-2 border-b border-line px-3">
      <Search size={15} className="shrink-0 text-fg-subtle" />
      {/* §90.3: cmdk сам ставит autocomplete="off", но этого мало — Chrome
          игнорирует его для полей, которые счёл контактными, а часть наших
          списков ищет по логину и email. Добавляем остальные признаки: имя без
          семантики и метки сторонних менеджеров паролей. Тип оставляем
          текстовым — Escape здесь закрывает попап, а search-поле Chrome по
          Escape очищает само. */}
      <CommandPrimitive.Input
        ref={ref}
        name="q"
        data-1p-ignore
        data-lpignore="true"
        data-form-type="other"
        className={cn(
          "h-full w-full bg-transparent text-[13px] text-fg outline-none placeholder:text-fg-subtle",
          className,
        )}
        {...rest}
      />
    </div>
  );
});

export const CommandList = forwardRef<
  ElementRef<typeof CommandPrimitive.List>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.List>
>(function CommandList({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.List
      ref={ref}
      className={cn("max-h-72 overflow-y-auto overflow-x-hidden py-1", className)}
      {...rest}
    />
  );
});

export const CommandEmpty = forwardRef<
  ElementRef<typeof CommandPrimitive.Empty>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Empty>
>(function CommandEmpty({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Empty
      ref={ref}
      className={cn("px-3 py-4 text-center text-xs text-fg-subtle", className)}
      {...rest}
    />
  );
});

export const CommandGroup = forwardRef<
  ElementRef<typeof CommandPrimitive.Group>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Group>
>(function CommandGroup({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Group
      ref={ref}
      className={cn(
        "px-1 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 " +
          "[&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase " +
          "[&_[cmdk-group-heading]]:tracking-wide [&_[cmdk-group-heading]]:text-fg-subtle",
        className,
      )}
      {...rest}
    />
  );
});

export const CommandItem = forwardRef<
  ElementRef<typeof CommandPrimitive.Item>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Item>
>(function CommandItem({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Item
      ref={ref}
      className={cn(
        "flex cursor-pointer select-none items-center gap-2.5 rounded px-2 py-1.5 text-[13px] " +
          "text-fg outline-none data-[selected=true]:bg-bg-muted aria-disabled:opacity-50 " +
          "aria-disabled:pointer-events-none",
        className,
      )}
      {...rest}
    />
  );
});
