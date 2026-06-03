import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { X, Plus } from "lucide-react";

import { api, type HeaderCatalogEntry } from "../../api/client";
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

type ListResp = { items: HeaderCatalogEntry[] };

// RFC 7230 token: латиница, цифры и допустимые спецсимволы, без пробелов.
const HEADER_RE = /^[a-zA-Z0-9!#$%&'*+.^_`|~-]+$/;

// HeadersField — combobox проброса заголовков из справочника (§24). Форма
// узла хранит имена (string[]); справочник — источник автодополнения с
// автосозданием. Дедупликация и матчинг — без учёта регистра.
export function HeadersField({
  value,
  onChange,
}: {
  value: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");

  useEffect(() => {
    const id = setTimeout(() => setDebounced(q), 150);
    return () => clearTimeout(id);
  }, [q]);

  const search = useQuery({
    queryKey: ["headers", debounced],
    queryFn: () => api.get<ListResp>("/api/headers", { q: debounced, limit: 20 }),
    enabled: open,
    staleTime: 30_000,
  });

  const create = useMutation({
    mutationFn: (name: string) => api.post<HeaderCatalogEntry>("/api/headers", { name }),
    onSuccess: (entry) => {
      qc.invalidateQueries({ queryKey: ["headers"] });
      addName(entry.name);
    },
  });

  const selectedLower = new Set(value.map((v) => v.toLowerCase()));

  function addName(name: string) {
    if (!selectedLower.has(name.toLowerCase())) onChange([...value, name]);
    setQ("");
    setOpen(false);
  }
  function remove(name: string) {
    onChange(value.filter((v) => v !== name));
  }

  const trimmed = q.trim();
  const candidates = (search.data?.items ?? []).filter((h) => !selectedLower.has(h.name.toLowerCase()));
  const exactExists = (search.data?.items ?? []).some((h) => h.name.toLowerCase() === trimmed.toLowerCase());
  const valid = trimmed !== "" && trimmed.length <= 100 && HEADER_RE.test(trimmed);
  const showCreate = trimmed !== "" && !exactExists && !selectedLower.has(trimmed.toLowerCase());

  return (
    <div>
      {value.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {value.map((h) => (
            <Chip key={h}>
              <span className="font-mono">{h}</span>
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
            {t("node.headers_combo.add_placeholder")}
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[360px] p-0">
          <Command shouldFilter={false}>
            <CommandInput value={q} onValueChange={setQ} placeholder={t("node.headers_combo.search")} />
            <CommandList>
              {candidates.length === 0 && !showCreate && (
                <CommandEmpty>{t("node.headers_combo.empty")}</CommandEmpty>
              )}
              {candidates.map((h) => (
                <CommandItem key={h.id} value={h.name} onSelect={() => addName(h.name)}>
                  <span className="font-mono text-[13px]">{h.name}</span>
                  {h.description && (
                    <span className="truncate text-[11px] text-fg-subtle">{h.description}</span>
                  )}
                  <span className="ml-auto whitespace-nowrap text-[11px] text-fg-subtle">
                    {t("node.headers_combo.used", { count: h.usage_count })}
                  </span>
                </CommandItem>
              ))}
              {showCreate && valid && (
                <CommandItem
                  value={"__create__" + trimmed}
                  onSelect={() => create.mutate(trimmed)}
                  className="text-accent"
                >
                  <Plus className="h-4 w-4" />
                  <span>{t("node.headers_combo.create", { name: trimmed })}</span>
                </CommandItem>
              )}
              {showCreate && !valid && (
                <div className="px-3 py-2 text-[11px] text-warn">
                  {t("node.headers_combo.invalid")}
                </div>
              )}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  );
}
