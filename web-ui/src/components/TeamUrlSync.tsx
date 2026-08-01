import { useTranslation } from "react-i18next";
import { AlertTriangle, X } from "lucide-react";

import { useTeamUrlParam } from "../lib/teamShare";

// TeamUrlSync — единственная точка монтирования зеркала `?team=` (§76).
// Живёт в AppShell над <Outlet/>: иначе параметр работал бы только на рабочем
// столе, а ref-guard'ы хука обязаны переживать переходы между страницами.
// Отдельный компонент, а не хук прямо в AppShell: useSearchParams подписывает
// на КАЖДОЕ изменение query-строки (рабочий стол правит её на каждый коммит
// поиска/фильтра/периода), и в отдельном листе эти перерисовки не задевают
// сайдбар, топбар и содержимое страницы.
//
// Рендерит только баннер недоступной команды — в норме это null.
export function TeamUrlSync() {
  const { t } = useTranslation();
  const { unavailableSlug, dismiss } = useTeamUrlParam();

  if (!unavailableSlug) return null;

  // Тон warn, а не err: ситуация неблокирующая — мы остались в рабочей команде,
  // страница под баннером полностью работоспособна.
  return (
    <div
      role="alert"
      className="mb-4 flex items-start gap-2 rounded-md bg-warn/10 px-3 py-2 text-sm text-warn"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1 break-words">
        {t("teams.unavailable", { slug: unavailableSlug })}
      </div>
      <button
        type="button"
        onClick={dismiss}
        aria-label={t("common.close")}
        className="shrink-0 rounded p-0.5 opacity-70 transition-opacity hover:opacity-100"
      >
        <X className="h-4 w-4" />
      </button>
    </div>
  );
}
