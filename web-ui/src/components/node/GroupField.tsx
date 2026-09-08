import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronDown, CircleOff, Plus } from "lucide-react";

import { api, type NodeGroup } from "../../api/client";
import { useRoleAtLeast } from "../../lib/useCurrentRole";
import {
  Command,
  CommandInput,
  CommandItem,
  CommandList,
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "../ui";

type ListResp = { items: NodeGroup[] };

// Длина имени — та же проверка, что на сервере (§99.3), чтобы не показывать
// «Создать» для заведомо невалидного имени. Формат НЕ ограничен: имя группы —
// свободный текст, в отличие от заголовков §24.
const MAX_NAME = 100;

// GroupField — выбор группы узла из справочника (§99.6) с созданием прямо из
// списка. Отличия от HeadersField (§24): выбор ОДИНОЧНЫЙ (у узла одна группа),
// поэтому вместо чипов — значение в кнопке-триггере, и в списке есть постоянный
// пункт «— Без группы —» для снятия привязки.
export function GroupField({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");
  // Создавать группы может только manager+ (§99.4). Для viewer/operator форма
  // узла и так недоступна, но комбобокс используется и на чтение — пункт
  // «Создать» им показывать нельзя: сервер ответит 403.
  const canCreate = useRoleAtLeast("manager");

  useEffect(() => {
    const id = setTimeout(() => setDebounced(q), 150);
    return () => clearTimeout(id);
  }, [q]);

  // Список для дропдауна — сужается поиском. Выбранная группа может в него не
  // попасть (её отфильтровал поиск), поэтому имя для кнопки-триггера берётся из
  // отдельного полного запроса: иначе при вводе в поиск подпись на кнопке
  // менялась бы на «Без группы».
  const search = useQuery({
    queryKey: ["node-groups", debounced],
    queryFn: () => api.get<ListResp>("/api/node-groups", { q: debounced, limit: 50 }),
    enabled: open,
    staleTime: 30_000,
  });
  const all = useQuery({
    queryKey: ["node-groups", ""],
    queryFn: () => api.get<ListResp>("/api/node-groups", { q: "", limit: 200 }),
    staleTime: 30_000,
  });

  const create = useMutation({
    mutationFn: (name: string) => api.post<NodeGroup>("/api/node-groups", { name }),
    onSuccess: (g) => {
      qc.invalidateQueries({ queryKey: ["node-groups"] });
      select(g.id);
    },
  });

  function select(id: string) {
    onChange(id);
    setQ("");
    setOpen(false);
  }

  const selected = all.data?.items.find((g) => g.id === value);
  const items = search.data?.items ?? [];
  const trimmed = q.trim();
  const exactExists = items.some((g) => g.name.toLowerCase() === trimmed.toLowerCase());
  const nameValid = trimmed !== "" && trimmed.length <= MAX_NAME;
  const showCreate = canCreate && trimmed !== "" && !exactExists;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={t("node.fields.group")}
          className="flex h-9 w-full items-center rounded-md border border-line bg-app px-3 text-left text-[13px] hover:border-accent"
        >
          {/* Группа не выбрана — приглушённый текст, а не пустая кнопка: пустая
              читалась бы как «не загрузилось». */}
          <span className={selected ? "truncate" : "truncate text-fg-subtle"}>
            {selected ? selected.name : t("node.group_combo.none")}
          </span>
          <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-fg-subtle" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[320px] p-0">
        <Command shouldFilter={false}>
          <CommandInput value={q} onValueChange={setQ} placeholder={t("node.group_combo.search")} />
          <CommandList>
            {/* «Без группы» — всегда первым и вне поиска: это способ СНЯТЬ
                привязку, а не запись справочника, и прятать его за поисковым
                запросом значило бы прятать единственный способ отвязать узел. */}
            <CommandItem value="__none__" onSelect={() => select("")}>
              <CircleOff className="h-4 w-4 text-fg-subtle" />
              <span className="text-fg-muted">{t("node.group_combo.none")}</span>
            </CommandItem>
            {/* «Ничего не найдено» — обычной строкой, а не CommandEmpty из cmdk:
                тот рендерится, только когда в списке нет НИ ОДНОГО пункта, а
                «— Без группы —» стоит здесь всегда, и подсказка не появилась бы
                никогда. */}
            {items.length === 0 && !showCreate && (
              <div className="px-3 py-2 text-[12px] text-fg-subtle">
                {t("node.group_combo.empty")}
              </div>
            )}
            {items.map((g) => (
              <CommandItem key={g.id} value={g.id} onSelect={() => select(g.id)}>
                <span className="truncate text-[13px]">{g.name}</span>
                <span className="ml-auto whitespace-nowrap text-[11px] text-fg-subtle">
                  {t("node.group_combo.used", { count: g.usage_count })}
                </span>
              </CommandItem>
            ))}
            {showCreate && nameValid && (
              <CommandItem
                value={"__create__" + trimmed}
                onSelect={() => create.mutate(trimmed)}
                className="text-accent"
              >
                <Plus className="h-4 w-4" />
                <span className="truncate">{t("node.group_combo.create", { name: trimmed })}</span>
              </CommandItem>
            )}
            {showCreate && !nameValid && (
              <div className="px-3 py-2 text-[11px] text-warn">
                {t("node.group_combo.invalid")}
              </div>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
