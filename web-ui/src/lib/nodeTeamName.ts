import { type Node } from "../api/client";
import { useNodeTeam } from "./nodeShare";
import { useMyTeams } from "./teams";

/**
 * useNodeTeamName — человекочитаемое имя команды узла (§65) или undefined.
 *
 * Эндпоинт узла отдаёт только `team_id`, поэтому имя резолвится по членствам
 * вызывающего, а при их отсутствии — резолвером §58 (`useNodeTeam`). Второй
 * источник не запасной «на всякий случай», а обязательный: в членствах
 * администратора команды узла может не быть вовсе (§89.6).
 *
 * Оба запроса общие со страницей узла — react-query дедуплицирует их по ключу,
 * так что вызов хука в нескольких вкладках сразу не добавляет обращений к API.
 */
export function useNodeTeamName(node: Node): string | undefined {
  const myTeams = useMyTeams();
  const nodeTeamQ = useNodeTeam(node.id);
  // items? — ответ может прийти без списка (частичный мок в тестах, урезанный
  // ответ старого бэкенда). Имя команды не тот повод, чтобы ронять вкладку.
  const membership = myTeams.data?.items?.find((tm) => tm.id === node.team_id);
  return membership?.name ?? nodeTeamQ.data?.team_name;
}
