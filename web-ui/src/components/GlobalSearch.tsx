import { Command as CommandPrimitive } from "cmdk";
import { Clock, Search, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { cn } from "../lib/cn";
import {
  loadGlobalSearchQ,
  MIN_SEARCH_QUERY_LEN,
  saveGlobalSearchQ,
  useClearSearchHistory,
  useNodeSearch,
  useRecordSearch,
  useSearchHistory,
  type NodeSearchItem,
} from "../lib/searchHistory";
import {
  Chip,
  Command,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandList,
  Popover,
  PopoverAnchor,
  PopoverContent,
} from "./ui";

// GlobalSearch — глобальный поиск узлов в шапке (§62). Ищет по всем командам
// пользователя (GET /api/search/nodes); выбор результата ведёт на страницу узла,
// а команда сессии переключается сама (существующий useEnsureNodeTeam в
// NodeDetail, §58). При пустом вводе показывает историю поиска (общую с полем
// «Поиск» на странице узлов); текст запроса переживает уход со страницы через
// sessionStorage и после выбора результата НЕ очищается.
//
// Рецепт cmdk-в-popover: <Command> оборачивает и инпут (PopoverAnchor), и
// список (PopoverContent). Radix-портал сохраняет React-контекст, поэтому
// клавиатурная навигация cmdk работает сквозь него; onOpenAutoFocus гасится,
// чтобы фокус оставался в инпуте.
export function GlobalSearch() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);

  const [q, setQ] = useState(() => loadGlobalSearchQ());
  const [debouncedQ, setDebouncedQ] = useState(q);
  const [open, setOpen] = useState(false);

  // Debounce запроса (как поле поиска Overview, §54.4): бэкенд не дёргается на
  // каждый keystroke. Текст сразу зеркалим в sessionStorage.
  useEffect(() => {
    saveGlobalSearchQ(q);
    const id = setTimeout(() => setDebouncedQ(q.trim()), 300);
    return () => clearTimeout(id);
  }, [q]);

  const showResults = debouncedQ.length >= MIN_SEARCH_QUERY_LEN;
  const searchQ = useNodeSearch(debouncedQ, open && showResults);
  const historyQ = useSearchHistory();
  const recordSearch = useRecordSearch();
  const clearHistory = useClearSearchHistory();

  const results = searchQ.data?.items ?? [];
  const history = historyQ.data?.items ?? [];

  // Фокус по хоткею "/" или Ctrl/⌘+K (когда фокус не в поле ввода).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inField =
        e.target instanceof HTMLElement &&
        (e.target.tagName === "INPUT" ||
          e.target.tagName === "TEXTAREA" ||
          e.target.isContentEditable);
      const hotkey = e.key === "/" || ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k");
      if (hotkey && !inField) {
        e.preventDefault();
        inputRef.current?.focus();
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const pickResult = (n: NodeSearchItem) => {
    recordSearch.mutate(q.trim());
    setOpen(false);
    navigate(`/nodes/${n.id}`);
    // q намеренно НЕ очищаем — пользователь может искать ещё раз.
  };

  const pickHistory = (entry: string) => {
    setQ(entry);
    setDebouncedQ(entry.trim());
    inputRef.current?.focus();
  };

  const emptyHint = useMemo(() => {
    if (showResults) return t("search.no_results");
    return t("search.hint_min_chars");
  }, [showResults, t]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Command shouldFilter={false} className="w-full max-w-md">
        <PopoverAnchor asChild>
          <div
            className={cn(
              "flex h-8 items-center gap-2 rounded-md border border-line bg-app px-2.5",
              "transition-colors focus-within:border-accent",
            )}
          >
            <Search size={15} className="shrink-0 text-fg-subtle" />
            <CommandPrimitive.Input
              ref={inputRef}
              value={q}
              onValueChange={setQ}
              onFocus={() => setOpen(true)}
              placeholder={t("search.placeholder")}
              className="h-full w-full bg-transparent text-[13px] text-fg outline-none placeholder:text-fg-subtle"
            />
          </div>
        </PopoverAnchor>

        <PopoverContent
          align="start"
          sideOffset={6}
          onOpenAutoFocus={(e) => e.preventDefault()}
          className="w-[--radix-popover-trigger-width] min-w-[320px] p-0"
        >
          <CommandList>
            {showResults ? (
              <>
                {results.length === 0 && !searchQ.isFetching && (
                  <CommandEmpty>{emptyHint}</CommandEmpty>
                )}
                {results.map((n) => (
                  <CommandItem key={n.id} value={n.id} onSelect={() => pickResult(n)}>
                    <div className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate font-mono text-[13px] text-fg">{n.path}</span>
                      <span className="truncate text-[11px] text-fg-subtle">{n.target_url}</span>
                    </div>
                    <Chip tone="info" className="ml-2 shrink-0">
                      {n.team_name}
                    </Chip>
                  </CommandItem>
                ))}
              </>
            ) : history.length > 0 ? (
              <CommandGroup heading={t("search.history")}>
                {history.map((entry) => (
                  <CommandItem key={entry} value={"h:" + entry} onSelect={() => pickHistory(entry)}>
                    <Clock size={14} className="shrink-0 text-fg-subtle" />
                    <span className="truncate">{entry}</span>
                  </CommandItem>
                ))}
                <button
                  type="button"
                  onClick={() => clearHistory.mutate()}
                  className="mt-1 flex w-full items-center gap-2 rounded px-2 py-1.5 text-[12px] text-fg-subtle hover:bg-bg-muted hover:text-fg"
                >
                  <Trash2 size={13} />
                  {t("search.clear_history")}
                </button>
              </CommandGroup>
            ) : (
              <CommandEmpty>{emptyHint}</CommandEmpty>
            )}
          </CommandList>
        </PopoverContent>
      </Command>
    </Popover>
  );
}
