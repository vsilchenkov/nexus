import { useRef } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Layers, Star } from "lucide-react";
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { restrictToParentElement, restrictToVerticalAxis } from "@dnd-kit/modifiers";
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import { cn } from "../lib/cn";
import { useFavoriteAllTeams } from "../lib/prefs";
import { setTeamScopeAll, useAllTeamsScope } from "../lib/teamScope";
import { useMyTeams, useSetFavoriteTeams, useSwitchTeam, type TeamMembership } from "../lib/teams";

// SidebarFavorites — секция «Избранное» в сайдбаре (§49.2): избранные команды
// в пользовательском порядке, клик переключает текущую команду И открывает её
// «Узлы» (маршрут "/"), drag-and-drop меняет порядок (PUT полного списка,
// optimistic). Без избранных секция скрыта целиком. id, чьих команд уже нет в
// членствах (гонка с исключением), молча отфильтровываются — сервер уже удалил
// их каскадом.
export function SidebarFavorites() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const myTeams = useMyTeams();
  const switchTeam = useSwitchTeam();
  const setFavorites = useSetFavoriteTeams();

  // distance 6px: клик короче порога остаётся кликом, не 0px-драгом.
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }));

  // После реального драга браузер всё равно шлёт click по элементу — гасим
  // его флагом, который сбрасывается макротаском ПОСЛЕ этого click.
  const draggedRef = useRef(false);

  const allFavorite = useFavoriteAllTeams();
  const allTeams = useAllTeamsScope();

  const data = myTeams.data;
  const teams = (data?.favorites ?? [])
    .map((id) => data?.items.find((tm) => tm.id === id))
    .filter((tm): tm is TeamMembership => Boolean(tm));

  if (teams.length === 0 && !allFavorite) return null;

  // §86.5: «Все команды» — всегда первым и ВНЕ сортировки drag-and-drop.
  // Порядок режима не хранится: он лежит в другом хранилище (преф §71, а не
  // user_team_favorites), и сливать два источника в один упорядоченный список
  // ради одного элемента дороже, чем закрепить его сверху.
  const pickAll = () => {
    if (draggedRef.current) return;
    setTeamScopeAll(true);
    navigate("/");
  };

  // Клик по избранной команде — это переход в её рабочее пространство, а не
  // просто смена контекста: всегда ведём на «Узлы» ("/"), даже если открыт
  // другой раздел (Аудит/Логи/Настройки) или команда уже текущая — иначе на
  // не-Узлах клик выглядел «не реагирующим». Команду переключаем лишь когда она
  // отличается (switch-team дёргает инвалидацию team-scoped кеша зря при той же).
  const pick = (id: string) => {
    if (draggedRef.current) return;
    // §86: выбор конкретной команды выводит из сквозного режима — как и в
    // переключателе шапки, иначе список остался бы сквозным.
    setTeamScopeAll(false);
    if (id !== data?.current_team_id && !switchTeam.isPending) {
      switchTeam.mutate(id);
    }
    navigate("/");
  };

  const resetDragged = () => {
    setTimeout(() => {
      draggedRef.current = false;
    }, 0);
  };

  const onDragEnd = (e: DragEndEvent) => {
    resetDragged();
    const { active, over } = e;
    if (!over || active.id === over.id) return;
    const ids = teams.map((tm) => tm.id);
    const from = ids.indexOf(String(active.id));
    const to = ids.indexOf(String(over.id));
    if (from < 0 || to < 0) return;
    setFavorites.mutate(arrayMove(ids, from, to));
  };

  return (
    <nav className="mt-4 border-t border-line pt-2.5">
      <div className="px-2.5 pb-1 text-[10px] uppercase tracking-wide text-fg-subtle">
        {t("nav.favorites")}
      </div>
      <div className="max-h-[40vh] space-y-0.5 overflow-y-auto">
        {allFavorite && (
          <button
            type="button"
            onClick={pickAll}
            className={cn(
              "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-[13px] transition-colors",
              allTeams
                ? "bg-bg-muted font-medium text-fg"
                : "text-fg-muted hover:bg-bg-muted hover:text-fg",
            )}
          >
            <Layers
              className={cn(
                "h-[18px] w-[18px] shrink-0",
                allTeams ? "text-accent" : "text-fg-subtle",
              )}
            />
            <span className="min-w-0 flex-1 truncate">{t("teams.all")}</span>
          </button>
        )}
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          modifiers={[restrictToVerticalAxis, restrictToParentElement]}
          onDragStart={() => {
            draggedRef.current = true;
          }}
          onDragCancel={resetDragged}
          onDragEnd={onDragEnd}
        >
          <SortableContext items={teams.map((tm) => tm.id)} strategy={verticalListSortingStrategy}>
            {teams.map((tm) => (
              <FavoriteItem
                key={tm.id}
                team={tm}
                isCurrent={!allTeams && tm.id === data?.current_team_id}
                onPick={() => pick(tm.id)}
              />
            ))}
          </SortableContext>
        </DndContext>
      </div>
    </nav>
  );
}

// FavoriteItem — отдельный компонент, а не разметка в map: useSortable —
// хук, в теле цикла его звать нельзя.
function FavoriteItem({
  team,
  isCurrent,
  onPick,
}: {
  team: TeamMembership;
  isCurrent: boolean;
  onPick: () => void;
}) {
  const { t } = useTranslation();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: team.id,
  });
  return (
    <button
      ref={setNodeRef}
      type="button"
      title={t("teams.drag_hint")}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      onClick={onPick}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-[13px] transition-colors",
        isCurrent
          ? "bg-bg-muted font-medium text-fg"
          : "text-fg-muted hover:bg-bg-muted hover:text-fg",
        isDragging && "relative z-10 opacity-80",
      )}
      {...attributes}
      {...listeners}
    >
      <Star
        className={cn(
          "h-[18px] w-[18px] shrink-0",
          isCurrent ? "fill-current text-accent" : "text-fg-subtle",
        )}
      />
      <span className="min-w-0 flex-1 truncate">{team.name}</span>
    </button>
  );
}
