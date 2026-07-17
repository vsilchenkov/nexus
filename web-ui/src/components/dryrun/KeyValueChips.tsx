import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, X } from "lucide-react";

import { Button, Chip, Input } from "../ui";

export type KeyValuePair = { key: string; value: string };

// KeyValueChips — редактор пар «ключ → значение» для заголовков и
// query-параметров тестового запроса (§55.2).
//
// Это НЕ HeadersField из формы узла: тот — комбобокс имён из каталога (§24,
// string[]), здесь же нужны имя и значение (`Authorization: Basic …`).
export function KeyValueChips({
  value,
  onChange,
  keyPlaceholder,
  valuePlaceholder,
  suggestion,
}: {
  value: KeyValuePair[];
  onChange: (next: KeyValuePair[]) => void;
  keyPlaceholder: string;
  valuePlaceholder: string;
  // suggestion — имя, которое ожидает узел (§55.2): клик подставляет его в поле
  // ключа, чтобы оператор не гадал, что писать.
  suggestion?: string;
}) {
  const { t } = useTranslation();
  const [k, setK] = useState("");
  const [v, setV] = useState("");

  function add() {
    const key = k.trim();
    if (key === "") return;
    // Одноимённый ключ заменяем: повтор — почти всегда исправление опечатки,
    // а не намерение послать заголовок дважды.
    const rest = value.filter((p) => p.key.toLowerCase() !== key.toLowerCase());
    onChange([...rest, { key, value: v }]);
    setK("");
    setV("");
  }

  return (
    <div>
      {value.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {value.map((p) => (
            <Chip key={p.key}>
              <span className="font-mono">
                {p.key}
                {p.value !== "" && <span className="text-fg-muted">: {p.value}</span>}
              </span>
              <button
                type="button"
                onClick={() => onChange(value.filter((x) => x.key !== p.key))}
                className="ml-1 text-fg-subtle hover:text-err"
                aria-label={t("common.delete")}
              >
                <X className="h-3 w-3" />
              </button>
            </Chip>
          ))}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Input
          value={k}
          onChange={(e) => setK(e.target.value)}
          placeholder={keyPlaceholder}
          className="w-[38%] font-mono text-xs"
        />
        <Input
          value={v}
          onChange={(e) => setV(e.target.value)}
          placeholder={valuePlaceholder}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          className="flex-1 font-mono text-xs"
        />
        <Button sm variant="ghost" onClick={add} disabled={k.trim() === ""} type="button">
          <Plus className="h-3.5 w-3.5" />
        </Button>
      </div>

      {suggestion && !value.some((p) => p.key.toLowerCase() === suggestion.toLowerCase()) && (
        <button
          type="button"
          onClick={() => setK(suggestion)}
          className="mt-1.5 text-[11px] text-accent hover:underline"
        >
          {t("dryrun.use_suggestion", { name: suggestion })}
        </button>
      )}
    </div>
  );
}
