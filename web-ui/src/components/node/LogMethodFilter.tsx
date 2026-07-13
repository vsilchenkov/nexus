import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronDown, X } from "lucide-react";

import { api } from "../../api/client";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "../ui";

type MethodsResp = { items: string[]; logs_configured?: boolean; logs_available?: boolean };

// LogMethodFilter — combobox фильтра по колонке method (§48.4). Значения —
// DISTINCT из CH-таблицы узла (/logs/methods, до 200): грузятся ТОЛЬКО при
// открытии дропдауна и заново при каждом открытии (staleTime/gcTime 0) —
// страница может жить долго, а логи прибывать. Клиентская фильтрация cmdk
// по ≤200 значениям. Пустое value = «Любой».
export function LogMethodFilter({
  nodeId,
  value,
  onChange,
}: {
  nodeId: string;
  value: string;
  onChange: (next: string) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);

  const methodsQ = useQuery({
    queryKey: ["log-methods", nodeId],
    queryFn: () => api.get<MethodsResp>(`/api/nodes/${nodeId}/logs/methods`),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
  });

  function pick(next: string) {
    onChange(next);
    setOpen(false);
  }

  const items = methodsQ.data?.items ?? [];

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center gap-1 rounded-md bg-bg-muted px-3 py-1.5 text-left text-sm hover:bg-bg-3"
        >
          {value ? (
            <span className="truncate font-mono text-xs">{value}</span>
          ) : (
            <span className="text-fg-muted">{t("logs.advanced.method_any")}</span>
          )}
          <span className="ml-auto flex shrink-0 items-center gap-1">
            {value && (
              <X
                className="h-3.5 w-3.5 text-fg-subtle hover:text-err"
                onClick={(e) => {
                  e.stopPropagation();
                  onChange("");
                }}
              />
            )}
            <ChevronDown className="h-3.5 w-3.5 text-fg-subtle" />
          </span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[280px] p-0">
        <Command>
          <CommandInput placeholder={t("logs.advanced.method_search")} />
          <CommandList>
            <CommandEmpty>
              {methodsQ.isLoading ? "…" : t("logs.advanced.method_empty")}
            </CommandEmpty>
            <CommandItem value="__any__" onSelect={() => pick("")}>
              <span className={value === "" ? "text-accent" : ""}>
                {t("logs.advanced.method_any")}
              </span>
            </CommandItem>
            {items.map((m) => (
              <CommandItem key={m} value={m} onSelect={() => pick(m)}>
                <span className={`font-mono text-[13px] ${m === value ? "text-accent" : ""}`}>
                  {m}
                </span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
