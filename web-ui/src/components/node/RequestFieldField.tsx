import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plus, X } from "lucide-react";

import { api, type RequestFieldCatalogEntry } from "../../api/client";
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

type ListResp = { items: RequestFieldCatalogEntry[] };

// Имя HTTP-заголовка / query-параметра — то же ограничение, что в БД
// (request_fields_name_fmt / auth_dynamic_field): латиница на старте.
const FIELD_RE = /^[a-zA-Z][a-zA-Z0-9_-]*$/;

// RequestFieldField — single-select combobox имени поля запроса из справочника
// (§41). В отличие от HeadersField, хранит ОДНО значение (string) и обязателен:
// при пустом value родитель красит рамку через invalid. Источник — каталог
// /api/request-fields с автосозданием. Матчинг — без учёта регистра.
export function RequestFieldField({
  value,
  onChange,
  invalid,
}: {
  value: string;
  onChange: (next: string) => void;
  invalid?: boolean;
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
    queryKey: ["request-fields", debounced],
    queryFn: () => api.get<ListResp>("/api/request-fields", { q: debounced, limit: 20 }),
    enabled: open,
    staleTime: 30_000,
  });

  const create = useMutation({
    mutationFn: (name: string) => api.post<RequestFieldCatalogEntry>("/api/request-fields", { name }),
    onSuccess: (entry) => {
      qc.invalidateQueries({ queryKey: ["request-fields"] });
      pick(entry.name);
    },
  });

  function pick(name: string) {
    onChange(name);
    setQ("");
    setOpen(false);
  }

  const trimmed = q.trim();
  const items = search.data?.items ?? [];
  const exactExists = items.some((f) => f.name.toLowerCase() === trimmed.toLowerCase());
  const valid = trimmed !== "" && trimmed.length <= 64 && FIELD_RE.test(trimmed);
  const showCreate = trimmed !== "" && !exactExists;

  return (
    <div className="flex items-center gap-1.5">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            className={`flex h-9 w-full max-w-md items-center rounded-md border bg-app px-3 text-left text-[13px] hover:border-accent ${
              invalid ? "border-err" : "border-line"
            }`}
          >
            {value ? (
              <span className="font-mono text-fg">{value}</span>
            ) : (
              <span className="text-fg-subtle">{t("node.request_field_combo.placeholder")}</span>
            )}
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[360px] p-0">
          <Command shouldFilter={false}>
            <CommandInput value={q} onValueChange={setQ} placeholder={t("node.request_field_combo.search")} />
            <CommandList>
              {items.length === 0 && !showCreate && (
                <CommandEmpty>{t("node.request_field_combo.empty")}</CommandEmpty>
              )}
              {items.map((f) => (
                <CommandItem key={f.id} value={f.name} onSelect={() => pick(f.name)}>
                  <span className="font-mono text-[13px]">{f.name}</span>
                  {f.description && (
                    <span className="truncate text-[11px] text-fg-subtle">{f.description}</span>
                  )}
                  <span className="ml-auto whitespace-nowrap text-[11px] text-fg-subtle">
                    {t("node.request_field_combo.used", { count: f.usage_count })}
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
                  <span>{t("node.request_field_combo.create", { name: trimmed })}</span>
                </CommandItem>
              )}
              {showCreate && !valid && (
                <div className="px-3 py-2 text-[11px] text-warn">
                  {t("node.request_field_combo.invalid")}
                </div>
              )}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {value && (
        <button
          type="button"
          onClick={() => onChange("")}
          className="text-fg-subtle hover:text-err"
          aria-label={t("common.delete")}
        >
          <X className="h-4 w-4" />
        </button>
      )}
    </div>
  );
}
