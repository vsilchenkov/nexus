import { Star } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "../../lib/cn";
import { useSetPref } from "../../lib/prefs";
import type { Period } from "../../lib/period";

/**
 * DefaultPeriodButton — «По умолчанию»: сохранить текущий пресет периода как
 * стартовый для этого экрана (§92, развитие §44.B/§71).
 *
 * teamId="" означает ГЛОБАЛЬНЫЙ преф (§92.2): так кнопка работает в сквозном
 * режиме «Все команды», где команды нет, и значение становится дефолтом для
 * всех команд, где свой не задан. Явные командные префы при этом не трогаются —
 * их перекрытие важнее, чем единообразие (useTeamDefaultPeriod резолвит
 * «команда → глобальный → 24ч»).
 *
 * Произвольный период кнопкой не сохраняется и кнопку прячет: календарный
 * диапазон в роли дефолта бессмыслен — завтра он уже прошлое.
 */
export function DefaultPeriodButton({
  period,
  savedDefault,
  teamId,
  prefKey,
  title,
  className,
}: {
  period: Period;
  savedDefault: Period;
  teamId: string;
  prefKey: string;
  title?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const setPref = useSetPref();

  if (period.kind !== "preset") return null;

  // Совпадение с сохранённым — кнопка неактивна и звезда залита: нажимать
  // нечего, но состояние «этот период и есть дефолт» должно быть видно.
  const isSaved = savedDefault.kind === "preset" && savedDefault.range === period.range;

  return (
    <button
      type="button"
      onClick={() => setPref.mutate({ teamId, key: prefKey, value: period })}
      disabled={isSaved}
      title={title ?? t("overview.set_default_period")}
      className={cn(
        "inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted",
        "hover:text-accent disabled:cursor-default disabled:opacity-50",
        className,
      )}
    >
      <Star className={cn("h-3.5 w-3.5", isSaved && "fill-current text-accent")} />
      {t("overview.set_default_period")}
    </button>
  );
}
