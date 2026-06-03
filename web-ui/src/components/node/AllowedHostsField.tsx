import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { X, ShieldX } from "lucide-react";

import { api, type HostAllowlistEntry, type HostKind } from "../../api/client";
import {
  Chip,
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "../ui";

type ListResp = { items: HostAllowlistEntry[] };

const kindTone: Record<HostKind, "success" | "info" | "warning"> = {
  exact: "success",
  wildcard: "info",
  regex: "warning",
};

// AllowedHostsField — секция allowlist хостов в форме узла (§23). Источник
// истины — каталог: для существующего узла привязка идёт сразу через
// attach/detach API; для нового — копится локально и привязывается после
// создания узла (см. NodeSettings.save). Создание новых паттернов — на
// странице Settings → Allowed Hosts (там выбор типа и превью).
export function AllowedHostsField({
  nodeId,
  urlMode,
  pending,
  onPendingChange,
}: {
  nodeId?: string;
  urlMode: "static" | "from_request";
  pending: HostAllowlistEntry[];
  onPendingChange: (next: HostAllowlistEntry[]) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");

  const attached = useQuery({
    queryKey: ["node-hosts", nodeId],
    queryFn: () => api.get<ListResp>(`/api/nodes/${nodeId}/allowed-hosts`),
    enabled: !!nodeId,
  });

  const search = useQuery({
    queryKey: ["allowed-hosts-search", q],
    queryFn: () => api.get<ListResp>("/api/allowed-hosts", { q, limit: 20 }),
    enabled: open,
  });

  const attach = useMutation({
    mutationFn: (hostID: string) => api.post(`/api/nodes/${nodeId}/allowed-hosts`, { host_id: hostID }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["node-hosts", nodeId] }),
  });
  const detach = useMutation({
    mutationFn: (hostID: string) => api.del(`/api/nodes/${nodeId}/allowed-hosts/${hostID}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["node-hosts", nodeId] }),
  });

  const selected = nodeId ? (attached.data?.items ?? []) : pending;
  const selectedIDs = new Set(selected.map((h) => h.id));
  const candidates = (search.data?.items ?? []).filter((h) => !selectedIDs.has(h.id));

  function add(entry: HostAllowlistEntry) {
    if (nodeId) attach.mutate(entry.id);
    else onPendingChange([...pending, entry]);
    setQ("");
    setOpen(false);
  }
  function remove(entry: HostAllowlistEntry) {
    if (nodeId) detach.mutate(entry.id);
    else onPendingChange(pending.filter((h) => h.id !== entry.id));
  }

  const empty = selected.length === 0;

  return (
    <div>
      {selected.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {selected.map((h) => (
            <Chip key={h.id} tone={kindTone[h.kind]}>
              <span className="font-mono">{h.pattern}</span>
              <span className="text-[9px] uppercase opacity-70">{h.kind}</span>
              <button
                type="button"
                onClick={() => remove(h)}
                className="ml-1 text-fg-subtle hover:text-err"
                aria-label={t("common.delete")}
              >
                <X className="h-3 w-3" />
              </button>
            </Chip>
          ))}
        </div>
      )}

      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            className="flex h-9 w-full max-w-md items-center rounded-md border border-line bg-app px-3 text-left text-[13px] text-fg-subtle hover:border-accent"
          >
            {t("node.allowed_hosts.add_placeholder")}
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[360px] p-0">
          <Command shouldFilter={false}>
            <CommandInput value={q} onValueChange={setQ} placeholder={t("node.allowed_hosts.search")} />
            <CommandList>
              <CommandEmpty>{t("node.allowed_hosts.not_found")}</CommandEmpty>
              {candidates.map((h) => (
                <CommandItem key={h.id} value={h.id} onSelect={() => add(h)}>
                  <span className="font-mono text-[13px]">{h.pattern}</span>
                  <Chip tone={kindTone[h.kind]} className="ml-auto">
                    {h.kind}
                  </Chip>
                </CommandItem>
              ))}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>

      {urlMode === "from_request" && empty ? (
        <div className="mt-2 flex items-start gap-2 rounded-md border border-err/40 bg-err/10 px-3 py-2.5 text-err">
          <ShieldX className="mt-px h-4 w-4 shrink-0" />
          <div>
            <div className="text-[13px] font-semibold">{t("node.allowed_hosts.ssrf_title")}</div>
            <div className="mt-0.5 text-[11.5px] opacity-90">{t("node.allowed_hosts.ssrf_desc")}</div>
          </div>
        </div>
      ) : (
        <div className="mt-1.5 text-[11px] text-fg-subtle">{t("node.allowed_hosts.hint")}</div>
      )}
    </div>
  );
}
