import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { resolveCustomPeriod, PRESET_RANGES, type Period, type PresetRange } from "../../lib/period";
import { DateTimeField } from "./DateTimeField";
import { Seg } from "./data";

// DEFAULT_MAX_LOOKBACK_MS — на сколько назад уходит пустое «от» там, где
// глубина хранения неизвестна (рабочий стол со многими узлами, мониторинг
// Kafka). 30 суток — самый крупный пресет периода.
const DEFAULT_MAX_LOOKBACK_MS = 30 * 24 * 3600_000;

// toLocalInput — RFC3339 → значение поля даты ("YYYY-MM-DDTHH:mm" в локальной
// зоне; формат совместим с прежним datetime-local и с DateTimeField).
function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (isNaN(+d)) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// fmtStamp — «05.08 00:00» для подписи фактического окна.
function fmtStamp(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// fmtSpan — длительность окна человеческим текстом: «2 сут 9 ч», «45 мин».
function fmtSpan(ms: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  const min = Math.max(Math.round(ms / 60_000), 0);
  if (min < 60) return t("metrics.range.span_m", { m: min });
  const h = Math.floor(min / 60);
  if (h < 24) return t("metrics.range.span_h", { h, m: min % 60 });
  return t("metrics.range.span_d", { d: Math.floor(h / 24), h: h % 24 });
}

/**
 * PeriodPicker — пресеты периода (1ч..30д) + «Произвольный» (§28 п.4).
 *
 * §84.4: произвольный период применяется КЛИКОМ ПО ДНЮ — поле даты (§48.8)
 * само закрывает поповер и отдаёт значение. Кнопки «Применить» больше нет:
 * лишний клик на каждое изменение, а обе границы приходилось заполнять
 * обязательно. Компонент один на все пять экранов, где выбирается период.
 *
 * maxLookbackMs — чем считается пустое «от». Вызывающая сторона передаёт
 * глубину хранения узла там, где узел один; без него берётся 30 суток.
 */
export function PeriodPicker({
  value,
  onChange,
  maxLookbackMs = DEFAULT_MAX_LOOKBACK_MS,
  className,
}: {
  value: Period;
  onChange: (p: Period) => void;
  maxLookbackMs?: number;
  className?: string;
}) {
  const { t } = useTranslation();
  const isCustom = value.kind === "custom";
  const [showCustom, setShowCustom] = useState(isCustom);
  // Поля календаря засеваются из value: пришедший извне произвольный период
  // (§54 — восстановление фильтров, дип-линк) должен быть виден в полях, а не
  // только применён к метрикам.
  const [from, setFrom] = useState(() => (value.kind === "custom" ? toLocalInput(value.from) : ""));
  const [to, setTo] = useState(() => (value.kind === "custom" ? toLocalInput(value.to) : ""));

  // Засева в useState мало: период может прийти уже ПОСЛЕ монтирования, без
  // ремоунта компонента (§54 — клик «Узлы» на активной странице восстанавливает
  // фильтры эффектом). Зависимости — строки, поэтому набранное в полях не
  // затирается: пользовательский ввод value не меняет.
  const customFrom = value.kind === "custom" ? toLocalInput(value.from) : null;
  const customTo = value.kind === "custom" ? toLocalInput(value.to) : null;
  useEffect(() => {
    if (customFrom === null || customTo === null) return;
    setFrom(customFrom);
    setTo(customTo);
    setShowCustom(true);
  }, [customFrom, customTo]);

  // commit — применить границы сразу, без отдельной кнопки. Незаполненная пара
  // не применяется молча: подпись ниже объясняет, чего не хватает.
  function commit(nextFrom: string, nextTo: string) {
    setFrom(nextFrom);
    setTo(nextTo);
    const r = resolveCustomPeriod(nextFrom, nextTo, maxLookbackMs);
    if (r) onChange({ kind: "custom", from: r.from, to: r.to });
  }

  const resolved = showCustom ? resolveCustomPeriod(from, to, maxLookbackMs) : null;

  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      <Seg
        value={value.kind === "preset" ? value.range : ("" as PresetRange)}
        onChange={(r) => {
          setShowCustom(false);
          onChange({ kind: "preset", range: r });
        }}
        options={PRESET_RANGES.map((r) => ({ value: r, label: t(`metrics.range.${r}`) }))}
      />
      <button
        type="button"
        onClick={() => setShowCustom((s) => !s)}
        className={cn(
          "rounded-md border border-line px-2.5 py-1 text-xs transition-colors hover:text-fg",
          isCustom ? "border-accent text-accent" : "text-fg-muted",
        )}
      >
        {t("metrics.range.custom")}
      </button>
      {showCustom && (
        <div className="flex flex-wrap items-center gap-1.5">
          <div className="w-44">
            <DateTimeField
              value={from}
              onChange={(v) => commit(v, to)}
              placeholder={t("metrics.range.from_open")}
              max={to ? new Date(to) : undefined}
            />
          </div>
          <span className="text-fg-subtle">—</span>
          <div className="w-44">
            <DateTimeField
              value={to}
              onChange={(v) => commit(from, v)}
              defaultTime="23:59"
              placeholder={t("metrics.range.to_open")}
              min={from ? new Date(from) : undefined}
            />
          </div>
          {/* Фактическое окно показывается ВСЕГДА: открытая граница без него
              означала бы, что пользователь не знает, что именно он смотрит. */}
          <span className="text-xs text-fg-muted">
            {resolved
              ? `↳ ${fmtStamp(Date.parse(resolved.from))} — ${fmtStamp(Date.parse(resolved.to))} (${fmtSpan(
                  Date.parse(resolved.to) - Date.parse(resolved.from),
                  t,
                )})`
              : t("metrics.range.need_one_bound")}
          </span>
        </div>
      )}
    </div>
  );
}
