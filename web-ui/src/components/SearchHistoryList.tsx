import { Clock, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useClearSearchHistory, useSearchHistory } from "../lib/searchHistory";

// SearchHistoryList — выпадающий список истории поиска для поля «Поиск» на
// странице узлов (§62). Простые кнопки (без cmdk-навигации, в отличие от
// объединённого списка GlobalSearch): здесь дропдаун несёт только историю.
// История общая с глобальным поиском — тот же источник /api/me/search-history.
// Пустая история → null (родитель не открывает пустой попап).
export function SearchHistoryList({ onPick }: { onPick: (entry: string) => void }) {
  const { t } = useTranslation();
  const historyQ = useSearchHistory();
  const clearHistory = useClearSearchHistory();
  const items = historyQ.data?.items ?? [];
  if (items.length === 0) return null;

  return (
    <div className="py-1">
      <div className="px-3 py-1.5 text-[10px] uppercase tracking-wide text-fg-subtle">
        {t("search.history")}
      </div>
      {items.map((entry) => (
        <button
          key={entry}
          type="button"
          // onMouseDown (не onClick): срабатывает до blur инпута, иначе Popover
          // успевает закрыться по blur раньше клика.
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(entry);
          }}
          className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-fg hover:bg-bg-muted"
        >
          <Clock size={14} className="shrink-0 text-fg-subtle" />
          <span className="truncate">{entry}</span>
        </button>
      ))}
      <button
        type="button"
        onMouseDown={(e) => {
          e.preventDefault();
          clearHistory.mutate();
        }}
        className="mt-1 flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px] text-fg-subtle hover:bg-bg-muted hover:text-fg"
      >
        <Trash2 size={13} />
        {t("search.clear_history")}
      </button>
    </div>
  );
}
