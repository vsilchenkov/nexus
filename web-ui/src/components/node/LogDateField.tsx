import { type CSSProperties, useState } from "react";
import { DayPicker } from "react-day-picker";
import { enUS, ru } from "react-day-picker/locale";
import { useTranslation } from "react-i18next";
import { Calendar, X } from "lucide-react";
import "react-day-picker/style.css";

import { Popover, PopoverContent, PopoverTrigger } from "../ui";

// LogDateField — выбор даты/времени фильтра логов (§48.8) на react-day-picker.
// Заменяет нативный datetime-local: месячная сетка + поле времени + быстрые
// пресеты. Значение хранится в формате "YYYY-MM-DDTHH:mm" (совместимо с прежним
// datetime-local), поэтому вызывающий код (advQueryParams) не меняется.
//
// UX (по требованию): клик по дню сразу применяет дату+время и закрывает
// поповер. Время правится полем ДО клика по дню; если дата уже выбрана,
// изменение времени применяется на лету (поповер не закрывается).

const pad = (n: number) => String(n).padStart(2, "0");

function parseValue(v: string): { date?: Date; time: string } {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(v);
  if (!m) return { time: "" };
  return { date: new Date(+m[1], +m[2] - 1, +m[3]), time: `${m[4]}:${m[5]}` };
}

function build(day: Date, time: string): string {
  const [h, mi] = (time || "00:00").split(":");
  return `${day.getFullYear()}-${pad(day.getMonth() + 1)}-${pad(day.getDate())}T${h}:${mi}`;
}

function label(v: string): string {
  const { date, time } = parseValue(v);
  if (!date) return "";
  return `${pad(date.getDate())}.${pad(date.getMonth() + 1)}.${date.getFullYear()} ${time}`;
}

export function LogDateField({
  value,
  onChange,
  min,
  max,
  defaultTime = "00:00",
  placeholder,
  onOpen,
}: {
  value: string;
  onChange: (next: string) => void;
  min?: Date;
  max?: Date;
  defaultTime?: string;
  placeholder: string;
  onOpen?: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const [timeInput, setTimeInput] = useState(defaultTime);

  const parsed = parseValue(value);
  const locale = i18n.language.startsWith("ru") ? ru : enUS;

  const disabled = [
    ...(min ? [{ before: min }] : []),
    ...(max ? [{ after: max }] : []),
  ];

  function handleOpenChange(next: boolean) {
    setOpen(next);
    if (next) {
      setTimeInput(parsed.time || defaultTime);
      onOpen?.();
    }
  }

  // Клик по дню — применить дату+время и закрыть (основной жест).
  function pickDay(day?: Date) {
    if (!day) return;
    onChange(build(day, timeInput || defaultTime));
    setOpen(false);
  }

  // Правка времени: если дата уже выбрана — применяем на лету, не закрывая.
  function changeTime(next: string) {
    setTimeInput(next);
    if (parsed.date && next) onChange(build(parsed.date, next));
  }

  // Быстрый пресет: конкретный день + дефолтное время, применить и закрыть.
  function preset(day: Date) {
    onChange(build(day, defaultTime));
    setOpen(false);
  }
  const today = new Date();
  const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);

  const shown = label(value);

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center gap-2 rounded-md bg-bg-muted px-3 py-1.5 text-left text-sm outline-none hover:bg-bg-3"
        >
          <Calendar className="h-3.5 w-3.5 shrink-0 text-fg-subtle" />
          {shown ? (
            <span className="truncate">{shown}</span>
          ) : (
            <span className="truncate text-fg-muted">{placeholder}</span>
          )}
          {value && (
            <X
              className="ml-auto h-3.5 w-3.5 shrink-0 text-fg-subtle hover:text-err"
              onClick={(e) => {
                e.stopPropagation();
                onChange("");
              }}
            />
          )}
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-auto p-2">
        <div className="mb-2 flex items-center gap-2">
          <label className="text-xs text-fg-muted">{t("logs.advanced.time")}</label>
          <input
            type="time"
            value={timeInput}
            onChange={(e) => changeTime(e.target.value)}
            className="rounded-md bg-bg-muted px-2 py-1 text-sm outline-none"
          />
        </div>
        <DayPicker
          mode="single"
          required={false}
          locale={locale}
          selected={parsed.date}
          defaultMonth={parsed.date ?? max ?? today}
          disabled={disabled}
          weekStartsOn={1}
          onSelect={pickDay}
          // Переменные задаём инлайн на .rdp-root — style.css определяет их
          // прямо на root, и переменные с родителя туда не наследуются (§48.8).
          style={
            {
              "--rdp-accent-color": "rgb(var(--accent))",
              "--rdp-accent-background-color": "rgb(var(--accent) / 0.18)",
              "--rdp-today-color": "rgb(var(--accent))",
              // Компактный размер: дефолт rdp — 2.75rem/44px на ячейку.
              "--rdp-day-width": "1.6rem",
              "--rdp-day-height": "1.6rem",
              "--rdp-day_button-width": "1.5rem",
              "--rdp-day_button-height": "1.5rem",
              "--rdp-nav_button-width": "1.3rem",
              "--rdp-nav_button-height": "1.3rem",
              "--rdp-nav-height": "1.4rem",
              "--rdp-weekday-padding": "0.1rem 0",
              fontSize: "0.7rem",
              color: "rgb(var(--fg))",
            } as CSSProperties
          }
          styles={{ month_caption: { fontSize: "0.78rem", paddingBottom: "0.2rem" } }}
        />
        <div className="mt-1 flex gap-1.5">
          <button
            type="button"
            onClick={() => preset(today)}
            className="rounded-md bg-bg-muted px-2 py-1 text-xs hover:bg-bg-3"
          >
            {t("logs.advanced.date_today")}
          </button>
          <button
            type="button"
            onClick={() => preset(yesterday)}
            className="rounded-md bg-bg-muted px-2 py-1 text-xs hover:bg-bg-3"
          >
            {t("logs.advanced.date_yesterday")}
          </button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
