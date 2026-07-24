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

type ClientHostsResp = { items: string[]; logs_configured?: boolean; logs_available?: boolean };

// LogClientHostFilter — combobox фильтра по колонке client_host (§67, PTR-имя
// клиента). Копия поведения LogMethodFilter: значения — DISTINCT из CH-таблицы
// узла (/logs/client-hosts, до 200), грузятся ТОЛЬКО при открытии дропдауна и
// заново при каждом открытии (staleTime/gcTime 0). Внешняя таблица (§64) без
// колонки отдаёт пустой список — виден только пункт «Любой». Пустое value =
// «Любой».
export function LogClientHostFilter({
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

  const hostsQ = useQuery({
    queryKey: ["log-client-hosts", nodeId],
    queryFn: () => api.get<ClientHostsResp>(`/api/nodes/${nodeId}/logs/client-hosts`),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
  });

  function pick(next: string) {
    onChange(next);
    setOpen(false);
  }

  const items = hostsQ.data?.items ?? [];

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
            <span className="text-fg-muted">{t("logs.advanced.client_host_any")}</span>
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
      {/* FQDN бывают длинными — растём до 92vw/560px, перенос break-all. */}
      <PopoverContent align="start" className="w-[max(280px,min(92vw,560px))] p-0">
        <Command>
          <CommandInput placeholder={t("logs.advanced.client_host_search")} />
          <CommandList>
            <CommandEmpty>
              {hostsQ.isLoading ? "…" : t("logs.advanced.client_host_empty")}
            </CommandEmpty>
            <CommandItem value="__any__" onSelect={() => pick("")}>
              <span className={value === "" ? "text-accent" : ""}>
                {t("logs.advanced.client_host_any")}
              </span>
            </CommandItem>
            {items.map((h) => (
              <CommandItem key={h} value={h} onSelect={() => pick(h)} className="items-start">
                <span
                  className={`min-w-0 whitespace-normal break-all font-mono text-[13px] ${h === value ? "text-accent" : ""}`}
                >
                  {h}
                </span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
