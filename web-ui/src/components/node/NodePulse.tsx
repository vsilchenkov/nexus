import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";

// fmtAgo — «4 мин назад», «3 ч 20 мин назад», «2 сут 5 ч назад».
function fmtAgo(ms: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  const min = Math.max(Math.round(ms / 60_000), 0);
  if (min < 1) return t("metrics.pulse.just_now");
  if (min < 60) return t("metrics.pulse.ago_m", { m: min });
  const h = Math.floor(min / 60);
  if (h < 24) return t("metrics.pulse.ago_h", { h, m: min % 60 });
  return t("metrics.pulse.ago_d", { d: Math.floor(h / 24), h: h % 24 });
}

function fmtStamp(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * NodePulse — последняя активность узла и признак тишины (§84.6).
 *
 * Понятия last seen в системе не было вовсе: узел, молчащий из-за обрыва, и
 * узел, молчащий по расписанию, выглядели одинаково.
 *
 * Значение — «последняя активность В ВЫБРАННОМ ОКНЕ и под текущими фильтрами»,
 * а не «за всё время», и формулировка обязана быть именно такой: полный max по
 * времени без окна — скан колонки на всей таблице, а вкладка поллится каждые
 * ~12 с. «За всё время» живёт отдельным действием (см. onShowAllTime).
 *
 * Порог тишины — ТРИ ШАГА ГРАФИКА: три пустых столбца подряд ровно то, что
 * видно глазом на соседнем графике, поэтому подпись и картинка не расходятся.
 * Отдельной настройки порога не вводится — она была бы четвёртым способом
 * сказать то же самое.
 */
export function NodePulse({
  lastSeenMs,
  total,
  stepSeconds,
  available,
  now = Date.now(),
  onShowAllTime,
}: {
  lastSeenMs: number;
  total: number;
  stepSeconds: number;
  // available — доступен ли источник (ClickHouse ответил). При false показываем
  // прочерк, а не «запросов не было»: это разные состояния.
  available: boolean;
  now?: number;
  onShowAllTime?: () => void;
}) {
  const { t } = useTranslation();

  if (!available) {
    return (
      <div className="rounded-md border border-line px-3 py-2 text-xs text-fg-muted">
        {t("metrics.pulse.label")}: —
      </div>
    );
  }

  const silent = lastSeenMs <= 0;
  const ageMs = silent ? 0 : Math.max(now - lastSeenMs, 0);
  const stale = !silent && stepSeconds > 0 && ageMs > 3 * stepSeconds * 1000;
  const tone = silent || stale ? "warn" : "ok";

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border px-3 py-2 text-xs",
        tone === "warn" ? "border-warn/40 text-warn" : "border-line text-fg-muted",
      )}
    >
      <span className="flex items-center gap-1.5 font-medium">
        <span
          className={cn("inline-block h-1.5 w-1.5 rounded-full", tone === "warn" ? "bg-warn" : "bg-ok")}
        />
        {silent
          ? t("metrics.pulse.silent")
          : `${t("metrics.pulse.label")}: ${fmtAgo(ageMs, t)} · ${fmtStamp(lastSeenMs)}`}
      </span>
      <span className="text-fg-muted">
        {silent ? t("metrics.pulse.none_in_period") : t("metrics.pulse.in_period", { n: fmtNum(total) })}
      </span>
      {onShowAllTime && (
        // Кнопка, а не автозагрузка: полный max по времени читает колонку на
        // всей таблице (боевая внешняя §64 — 10,2 млн записей), и в поллинг
        // такому запросу нельзя.
        <button
          type="button"
          onClick={onShowAllTime}
          className="ml-auto text-accent underline-offset-2 hover:underline"
        >
          {t("metrics.pulse.all_time")}
        </button>
      )}
    </div>
  );
}
