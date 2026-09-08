import { useTranslation } from "react-i18next";
import { Download, RefreshCw, Radio, Pause } from "lucide-react";

import { cn } from "../../lib/cn";
import { SERVICES, LEVELS, type LevelName, type ServiceName } from "../../lib/logsUtils";
import { Button, buttonClasses, SearchInput, Seg, type SegOption } from "../ui";

type Props = {
  services: ServiceName[];
  onServicesChange: (s: ServiceName[]) => void;
  level: LevelName;
  onLevelChange: (l: LevelName) => void;
  levelPending?: boolean;
  query: string;
  onQueryChange: (q: string) => void;
  live: boolean;
  onLiveToggle: () => void;
  limit: number;
  onLimitChange: (n: number) => void;
  onRefresh: () => void;
  downloadHref: string;
};

const LIMITS = [200, 500, 1000, 2000];

// LogsToolbar — липкий тулбар консоли «Логи» (§51.6): чипсы сервисов
// (клиентский фильтр), сегментированный уровень (меняет РЕАЛЬНЫЙ порог через
// PUT /api/settings/app), поиск, Live-переключатель, количество, обновить,
// скачать.
export function LogsToolbar({
  services,
  onServicesChange,
  level,
  onLevelChange,
  levelPending = false,
  query,
  onQueryChange,
  live,
  onLiveToggle,
  limit,
  onLimitChange,
  onRefresh,
  downloadHref,
}: Props) {
  const { t } = useTranslation();

  const allSelected = services.length === 0 || services.length === SERVICES.length;
  const toggleService = (s: ServiceName) => {
    if (allSelected) {
      onServicesChange([s]);
      return;
    }
    if (services.includes(s)) {
      const next = services.filter((x) => x !== s);
      onServicesChange(next.length === 0 ? [] : next); // пусто = все
    } else {
      onServicesChange([...services, s]);
    }
  };

  const chipClass = (active: boolean) =>
    cn(
      "rounded-md px-2.5 py-1 text-xs transition-colors",
      active ? "bg-bg-3 font-medium text-fg" : "text-fg-muted hover:text-fg",
    );

  const levelOptions: SegOption<LevelName>[] = LEVELS.map((l) => ({
    value: l,
    label: t(`logs.viewer.level.${l}`),
  }));

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-line bg-bg px-3 py-2">
      <div className="inline-flex rounded-md bg-bg-muted p-0.5" role="group" aria-label={t("logs.viewer.services")}>
        <button type="button" className={chipClass(allSelected)} onClick={() => onServicesChange([])}>
          {t("logs.viewer.all_services")}
        </button>
        {SERVICES.map((s) => (
          <button
            key={s}
            type="button"
            className={chipClass(!allSelected && services.includes(s))}
            onClick={() => toggleService(s)}
          >
            {s}
          </button>
        ))}
      </div>

      {/* Сегмент уровня — side effect: PUT /api/settings/app {logging:{level}}. */}
      <div className={cn(levelPending && "pointer-events-none opacity-60")} title={t("logs.viewer.level_hint")}>
        <Seg value={level} options={levelOptions} onChange={onLevelChange} />
      </div>

      <SearchInput
        className="w-52"
        value={query}
        onChange={(e) => onQueryChange(e.target.value)}
        placeholder={t("logs.viewer.search_placeholder")}
        aria-label={t("logs.viewer.search_placeholder")}
      />

      <select
        className="rounded-md border border-line bg-bg px-2 py-1.5 text-xs text-fg"
        value={limit}
        onChange={(e) => onLimitChange(Number(e.target.value))}
        aria-label={t("logs.viewer.limit")}
      >
        {LIMITS.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>

      <div className="ml-auto flex items-center gap-2">
        <Button sm variant={live ? "primary" : "default"} onClick={onLiveToggle}>
          {live ? <Radio className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
          {live ? t("logs.viewer.live") : t("logs.viewer.paused")}
        </Button>
        <Button sm onClick={onRefresh} aria-label={t("logs.viewer.refresh")}>
          <RefreshCw className="h-3.5 w-3.5" />
        </Button>
        {/* Ссылка со стилями кнопки: <Button> внутри <a> — невалидный HTML,
            и вложенная кнопка перехватывает клик у ссылки. */}
        <a href={downloadHref} download className={buttonClasses({ sm: true })}>
          <Download className="h-3.5 w-3.5" /> {t("logs.viewer.download")}
        </a>
      </div>
    </div>
  );
}
