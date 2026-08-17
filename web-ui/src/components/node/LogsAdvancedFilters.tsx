import { useTranslation } from "react-i18next";

import { DateTimeField, LabelHint } from "../ui";
import { LogMethodFilter } from "./LogMethodFilter";
import { LogClientHostFilter } from "./LogClientHostFilter";

import { emptyAdvForm, type LogsAdvForm } from "../../lib/logsQuery";

// LogsAdvancedFilters — панель расширенных фильтров (§48/§67), вынесенная из
// LogsTab без изменения поведения: §79.4 подключает её ещё и на вкладку
// «Метрики», а два экземпляра одной панели неминуемо разъехались бы.
//
// §77.3 сохраняется дословно: поле «Поиск» коммитится по Enter/blur (не на
// каждую букву — полнотекст по всей истории стоит секунды), Esc возвращает
// применённое значение, комбобоксы и тумблеры применяются сразу, «Сбросить»
// гасит mousedown (иначе blur поля успел бы запустить лишний запрос ПЕРЕД
// сбросом).

export function LogsAdvancedFilters({
  nodeId,
  draft,
  applied,
  onDraft,
  onCommit,
  badQuery,
  showDates = true,
  dateRange,
  onOpenDate,
}: {
  nodeId: string;
  draft: LogsAdvForm;
  applied: LogsAdvForm;
  onDraft: (f: LogsAdvForm) => void;
  onCommit: (f: LogsAdvForm) => void;
  badQuery?: boolean;
  /** showDates=false — окно задаёт выбор периода экрана (вкладка «Метрики»,
   * §79.4): два контрола, пишущих в одни границы, затирали бы друг друга. */
  showDates?: boolean;
  dateRange?: { min: number; max: number } | null;
  onOpenDate?: () => void;
}) {
  const { t } = useTranslation();
  const minDate = dateRange && dateRange.min > 0 ? new Date(dateRange.min) : undefined;
  const maxDate = dateRange && dateRange.max > 0 ? new Date(dateRange.max) : undefined;

  return (
    <div className="grid grid-cols-1 items-end gap-3 border-b border-line px-4 py-3 md:grid-cols-12">
      {/* Ряд 1: Поиск (тумблеры Aa/ab|/.* внутри поля + подсказка) и Method (§48.4) */}
      <div className="space-y-1 md:col-span-8">
        <label className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-fg-muted">
          {t("logs.advanced.q")}
          <LabelHint
            side="right"
            content={
              <div className="max-w-xs space-y-1 text-left">
                <div>{t("logs.advanced.hint_fields")}</div>
                <div>{t("logs.advanced.hint_syntax")}</div>
                <div>{t("logs.advanced.hint_prefix")}</div>
                <div>{t("logs.advanced.hint_escape")}</div>
                <div>{t("logs.advanced.hint_modes")}</div>
                <div>{t("logs.advanced.hint_regex_note")}</div>
              </div>
            }
          />
        </label>
        <div className="relative">
          {/* §77.3: поиск запускается по завершении ввода — Enter или уход
              фокуса, НЕ на каждую букву (полнотекст по всей истории стоит
              секунды, см. §77.5). Esc возвращает применённое значение. */}
          <input
            type="text"
            autoComplete="off"
            value={draft.q}
            onChange={(e) => onDraft({ ...draft, q: e.target.value })}
            onKeyDown={(e) => {
              if (e.key === "Enter") onCommit(draft);
              if (e.key === "Escape") onDraft(applied);
            }}
            onBlur={() => onCommit(draft)}
            placeholder={t("logs.advanced.q_placeholder")}
            className="w-full rounded-md bg-bg-muted py-1.5 pl-3 pr-24 text-sm outline-none"
          />
          {/* Кнопки-тумблеры режимов как в VS Code (§48.2) */}
          <div className="absolute inset-y-0 right-1.5 flex items-center gap-0.5">
            {(
              [
                { key: "qCase", label: "Aa", title: t("logs.advanced.case_tooltip") },
                { key: "qWord", label: "ab|", title: t("logs.advanced.word_tooltip") },
                { key: "qRegex", label: ".*", title: t("logs.advanced.regex_tooltip") },
              ] as const
            ).map((b) => (
              <button
                key={b.key}
                type="button"
                title={b.title}
                aria-pressed={draft[b.key]}
                // §77.3: тумблер режима — выбор, а не ввод: применяется сразу.
                onMouseDown={(e) => e.preventDefault()} // не отбирать фокус у поля (иначе blur даст лишний коммит)
                onClick={() => onCommit({ ...draft, [b.key]: !draft[b.key] })}
                className={`rounded px-1 py-0.5 font-mono text-[11px] leading-none transition-colors ${
                  draft[b.key] ? "bg-accent/20 text-accent" : "text-fg-subtle hover:text-fg-muted"
                }`}
              >
                {b.label}
              </button>
            ))}
          </div>
        </div>
        {badQuery && <div className="text-xs text-err">{t("logs.advanced.bad_query")}</div>}
      </div>
      <div className="space-y-1 md:col-span-4">
        <label className="text-[10px] uppercase tracking-wider text-fg-muted">
          {t("logs.advanced.method")}
        </label>
        <LogMethodFilter
          nodeId={nodeId}
          value={draft.method}
          onChange={(m) => onCommit({ ...draft, method: m })}
        />
      </div>
      {/* Ряд 2 (§48.8 + §67, эскиз утверждён): «Дата с» / «Дата по» с метками
          сверху и шириной по контенту, затем «Хост клиента», кнопки — в том же
          ряду справа, без переноса. flex вместо grid-колонок — иначе поля дат
          растягивались на всю колонку. */}
      <div className="flex flex-wrap items-end gap-3 md:col-span-12">
        {showDates && (
          <>
            <div className="w-[200px] space-y-1">
              <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                {t("logs.advanced.from")}
              </label>
              <DateTimeField
                value={draft.from}
                onChange={(v) => onCommit({ ...draft, from: v })}
                min={minDate}
                max={maxDate}
                defaultTime="00:00"
                onOpen={onOpenDate}
              />
            </div>
            <div className="w-[200px] space-y-1">
              <label className="text-[10px] uppercase tracking-wider text-fg-muted">
                {t("logs.advanced.to")}
              </label>
              <DateTimeField
                value={draft.to}
                onChange={(v) => onCommit({ ...draft, to: v })}
                min={minDate}
                max={maxDate}
                defaultTime="23:59"
                onOpen={onOpenDate}
              />
            </div>
          </>
        )}
        <div className="w-[240px] space-y-1">
          <label className="text-[10px] uppercase tracking-wider text-fg-muted">
            {t("logs.advanced.client_host")}
          </label>
          <LogClientHostFilter
            nodeId={nodeId}
            value={draft.clientHost}
            onChange={(h) => onCommit({ ...draft, clientHost: h })}
          />
        </div>
        {/* §77.3: кнопки «Применить» больше нет — фильтры применяются по
            завершении ввода. «Сбросить» гасит mousedown, иначе blur поля
            «Поиск» успел бы запустить лишний запрос ПЕРЕД сбросом. */}
        <div className="ml-auto flex items-center gap-2">
          <button
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onCommit(emptyAdvForm)}
            className="rounded-md bg-bg-muted px-3 py-1.5 text-sm hover:bg-bg-3"
          >
            {t("logs.advanced.reset")}
          </button>
        </div>
      </div>
    </div>
  );
}
