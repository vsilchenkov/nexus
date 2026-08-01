import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, ChevronDown, Share2, Star } from "lucide-react";

import { cn } from "../lib/cn";
import { copyToClipboard } from "../lib/clipboard";
import { teamPageUrl } from "../lib/teamShare";
import { useMyTeams, useSetFavoriteTeams, useSwitchTeam, type TeamMembership } from "../lib/teams";
import { Popover, PopoverTrigger, PopoverContent, Tooltip } from "./ui";

// TeamSwitcher — переключатель команды в Topbar (§18.6, переработан в §49).
// Триггер показывает отображаемое имя текущей команды (без slug — §49.1);
// в выпадающем списке — имена команд, звезда добавляет/убирает из избранного
// (секция «Избранное» в сайдбаре, §49.2). Кастомный Popover вместо нативного
// <select>: <option> не умеет ни звезду, ни разметку.
export function TeamSwitcher() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const myTeams = useMyTeams();
  const switchTeam = useSwitchTeam();
  const setFavorites = useSetFavoriteTeams();

  const data = myTeams.data;
  if (!data || data.items.length === 0) return null;
  const current = data.items.find((tm) => tm.id === data.current_team_id);
  const favorites = data.favorites ?? [];

  const pick = (id: string) => {
    if (id !== data.current_team_id && !switchTeam.isPending) {
      switchTeam.mutate(id);
    }
    setOpen(false);
  };

  const toggleFavorite = (id: string) => {
    const next = favorites.includes(id)
      ? favorites.filter((f) => f !== id)
      : [...favorites, id];
    setFavorites.mutate(next);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Tooltip content={t("teams.switcher_label")}>
        <PopoverTrigger
          aria-label={t("teams.switcher_label")}
          className={cn(
            "flex h-8 items-center gap-1.5 rounded-md border border-line bg-app px-2.5 text-[13px] text-fg outline-none",
            "transition-colors hover:bg-bg-muted data-[state=open]:border-accent",
          )}
        >
          <span className="max-w-[200px] truncate">{current?.name ?? ""}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-fg-subtle" />
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent align="end" className="min-w-[260px] p-1.5">
        <div className="px-3 py-1.5 text-[10px] uppercase tracking-wide text-fg-subtle">
          {t("nav.team")}
        </div>
        {data.items.map((tm) => (
          <TeamRow
            key={tm.id}
            team={tm}
            isCurrent={tm.id === data.current_team_id}
            isFavorite={favorites.includes(tm.id)}
            onPick={() => pick(tm.id)}
            onToggleFavorite={() => toggleFavorite(tm.id)}
          />
        ))}
      </PopoverContent>
    </Popover>
  );
}

// TeamRow — строка команды: кнопка выбора (имя + галка текущей), кнопка-звезда
// и кнопка «Поделиться» (§76). Соседние кнопки, а не вложенные (невалидный
// HTML) — и звезда со «Поделиться» не переключают команду без всяких
// stopPropagation. Попап при копировании остаётся открытым по той же причине:
// onPick не срабатывает, а Radix закрывает Popover только по outside-pointerdown.
function TeamRow({
  team,
  isCurrent,
  isFavorite,
  onPick,
  onToggleFavorite,
}: {
  team: TeamMembership;
  isCurrent: boolean;
  isFavorite: boolean;
  onPick: () => void;
  onToggleFavorite: () => void;
}) {
  const { t } = useTranslation();
  const favLabel = isFavorite ? t("teams.favorite_remove") : t("teams.favorite_add");
  return (
    <div className="flex items-center gap-0.5">
      <button
        type="button"
        onClick={onPick}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2 rounded px-3 py-2 text-left text-[13px] hover:bg-bg-muted",
          isCurrent ? "font-medium text-fg" : "text-fg-muted hover:text-fg",
        )}
      >
        <span className="min-w-0 flex-1 truncate">{team.name}</span>
        {isCurrent && <Check className="h-3.5 w-3.5 shrink-0 text-accent" />}
      </button>
      <Tooltip content={favLabel}>
        <button
          type="button"
          aria-label={favLabel}
          aria-pressed={isFavorite}
          onClick={onToggleFavorite}
          className={cn(
            "grid h-7 w-7 shrink-0 place-items-center rounded transition-colors hover:bg-bg-muted",
            isFavorite ? "text-accent" : "text-fg-subtle hover:text-fg",
          )}
        >
          <Star className={cn("h-3.5 w-3.5", isFavorite && "fill-current")} />
        </button>
      </Tooltip>
      <ShareTeamButton slug={team.slug} />
    </div>
  );
}

// ShareTeamButton — копирует ссылку на рабочее пространство команды (§76.2).
// Подтверждение inline (иконка на 1.5 с меняется на галочку), как у кнопки
// «Поделиться» узла: тостов в панели нет. Доступна всем ролям — шаринг ссылки
// ничего не мутирует, а получатель всё равно увидит только свои команды.
function ShareTeamButton({ slug }: { slug: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  // Попап часто закрывают раньше, чем истечёт таймер — размонтированной строке
  // setState уже не нужен.
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const share = async () => {
    if (!(await copyToClipboard(teamPageUrl(slug)))) return;
    setCopied(true);
    timer.current = window.setTimeout(() => setCopied(false), 1500);
  };

  const label = copied ? t("common.copied") : t("teams.share");
  return (
    <Tooltip content={label}>
      <button
        type="button"
        aria-label={label}
        onClick={share}
        className={cn(
          "grid h-7 w-7 shrink-0 place-items-center rounded transition-colors hover:bg-bg-muted",
          copied ? "text-ok" : "text-fg-subtle hover:text-fg",
        )}
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Share2 className="h-3.5 w-3.5" />}
      </button>
    </Tooltip>
  );
}
