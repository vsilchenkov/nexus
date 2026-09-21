import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronDown, X } from "lucide-react";

import { api } from "../../api/client";
import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { clientWindowKind, type ClientWindow } from "../../lib/logsQuery";
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

type LogClient = { ip: string; host: string; count: number };
type ClientsResp = {
  items: LogClient[];
  truncated?: boolean;
  logs_configured?: boolean;
  logs_available?: boolean;
};

/** ClientValue — значение фильтра: ip у значения с IP, host — только у
 * значения без IP (внешние таблицы §64). Оба пустые — «Любой». */
export type ClientValue = { ip: string; host: string };

// LogClientFilter — фильтр «Клиент» журнала узла (§105.4), замена «Хоста
// клиента» (§67). Значения — фасет /logs/clients: пары «IP — имя» с числом
// записей за период экрана. Выбор идёт по IP: имя производно от IP и бывает
// пустым (нет PTR-записи), а фильтр по имени терял и такие IP целиком, и
// записи, у которых имя не записалось. Имя остаётся подписью.
//
// Как у LogMethodFilter, фасет грузится ТОЛЬКО при открытии и заново при каждом
// открытии: страница живёт долго, а записи прибывают.
export function LogClientFilter({
  nodeId,
  value,
  facetWindow,
  onChange,
}: {
  nodeId: string;
  value: ClientValue;
  facetWindow: ClientWindow;
  onChange: (next: ClientValue) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);

  const clientsQ = useQuery({
    queryKey: ["log-clients", nodeId, facetWindow.key],
    queryFn: ({ signal }) => {
      const w = facetWindow.get();
      const params: Record<string, string> = {};
      if (w.from !== undefined) params.from = String(w.from);
      if (w.to !== undefined) params.to = String(w.to);
      return api.get<ClientsResp>(`/api/nodes/${nodeId}/logs/clients`, params, { signal });
    },
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
  });

  function pick(next: ClientValue) {
    onChange(next);
    setOpen(false);
  }

  const items = clientsQ.data?.items ?? [];
  const active = value.ip !== "" || value.host !== "";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center gap-1 rounded-md bg-bg-muted px-3 py-1.5 text-left text-sm hover:bg-bg-3"
        >
          {active ? (
            <span className="truncate font-mono text-xs" title={value.ip || undefined}>
              {triggerLabel(value, items)}
            </span>
          ) : (
            <span className="text-fg-muted">{t("logs.advanced.client_any")}</span>
          )}
          <span className="ml-auto flex shrink-0 items-center gap-1">
            {active && (
              <X
                className="h-3.5 w-3.5 text-fg-subtle hover:text-err"
                onClick={(e) => {
                  e.stopPropagation();
                  onChange({ ip: "", host: "" });
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
          <CommandInput placeholder={t("logs.advanced.client_search")} />
          <CommandList>
            <CommandEmpty>{t("logs.advanced.client_empty")}</CommandEmpty>
            <CommandItem value="__any__" onSelect={() => pick({ ip: "", host: "" })}>
              <span className={active ? "" : "text-accent"}>{t("logs.advanced.client_any")}</span>
            </CommandItem>
            {items.map((c) => (
              <ClientItem key={`${c.ip}|${c.host}`} client={c} selected={isSelected(c, value)} onPick={pick} />
            ))}
          </CommandList>
        </Command>
        <div className="space-y-0.5 border-t border-line px-3 py-2 text-[11px] text-fg-subtle">
          {footerLines(clientsQ, facetWindow, t).map((line) => (
            <div key={line}>{line}</div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ClientItem — строка значения: имя (или «имя не определено»), под ним IP,
// справа число записей. Значение без IP и без имени выбрать нельзя: фильтра
// «IP пуст и имя пусто» у API нет, а пустой client_host означает «Любой».
function ClientItem({
  client: c,
  selected,
  onPick,
}: {
  client: LogClient;
  selected: boolean;
  onPick: (v: ClientValue) => void;
}) {
  const { t } = useTranslation();
  const selectable = c.ip !== "" || c.host !== "";
  const name = c.host || t(c.ip ? "logs.advanced.client_unnamed" : "logs.advanced.client_unknown");
  return (
    <CommandItem
      value={`${c.ip}|${c.host}`}
      keywords={[c.host, c.ip].filter(Boolean)}
      disabled={!selectable}
      onSelect={() => onPick(c.ip ? { ip: c.ip, host: "" } : { ip: "", host: c.host })}
      className="items-start"
    >
      <span className="min-w-0 flex-1">
        <span
          className={cn(
            "block whitespace-normal break-all text-[13px]",
            c.host ? "font-mono" : "italic text-fg-muted",
            selected && "text-accent",
          )}
        >
          {name}
        </span>
        {c.ip ? (
          <span className="block break-all font-mono text-[11px] text-fg-subtle">{c.ip}</span>
        ) : (
          // У неактивного пункта всплывающая подсказка не покажется (он не
          // ловит наведение), поэтому причина написана прямо в строке.
          !selectable && (
            <span className="block whitespace-normal text-[11px] text-fg-subtle">
              {t("logs.advanced.client_unknown_tip")}
            </span>
          )
        )}
      </span>
      <span className="shrink-0 pt-0.5 text-[11px] tabular-nums text-fg-muted">{fmtNum(c.count)}</span>
    </CommandItem>
  );
}

function isSelected(c: LogClient, v: ClientValue): boolean {
  return v.ip ? c.ip === v.ip : c.ip === "" && v.host !== "" && c.host === v.host;
}

// triggerLabel — в поле показывается имя, если оно известно по фасету, иначе
// IP. До первого открытия фасета (значение приехало ссылкой) имени ещё нет.
function triggerLabel(v: ClientValue, items: LogClient[]): string {
  if (!v.ip) return v.host;
  return items.find((c) => c.ip === v.ip)?.host || v.ip;
}

// footerLines — подпись под списком: за какое окно посчитаны числа и усечён ли
// список. Пункт «Любой» виден всегда, поэтому CommandEmpty загрузку и
// недоступность ClickHouse не покажет — они живут здесь.
function footerLines(
  q: { isLoading: boolean; isError: boolean; data?: ClientsResp },
  w: ClientWindow,
  t: (k: string, o?: Record<string, unknown>) => string,
): string[] {
  if (q.isLoading) return ["…"];
  if (q.isError || !q.data || q.data.logs_available === false) {
    return [t("logs.advanced.client_unavailable")];
  }
  const lines = [t(`logs.advanced.client_window_${clientWindowKind(w.get())}`)];
  if (q.data.truncated) lines.push(t("logs.advanced.client_truncated", { n: q.data.items.length }));
  return lines;
}
