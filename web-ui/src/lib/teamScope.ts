import { useSyncExternalStore } from "react";

// Слой «Все команды» (§86) — сквозной режим просмотра.
//
// Это РЕЖИМ, а не команда: серверная current_team_id не меняется (§86.2), и
// сессия продолжает обслуживать всё остальное приложение. Поэтому режим живёт
// на клиенте: в адресной строке (`?team=*`, зеркало §76) и в sessionStorage.
//
// Зачем зеркало в sessionStorage, а не только URL: клик по узлу уводит на
// `/nodes/:id`, где параметр запрещён (§76.3, командой страницы владеет сам
// узел), а переход «Узлы» в сайдбаре открывает `/` вообще без query. Без зеркала
// оператор молча оказывался бы в одной команде. Приём тот же, что у строки
// глобального поиска (§62, `nexus.globalsearch.q`).

// TEAM_SCOPE_ALL — значение `?team`, включающее сквозной режим.
//
// Именно `*`, а не `all`: строка `all` проходит формат slug'а команды
// (`^[a-z][a-z0-9_]{0,31}$`, §18.1) и однажды столкнулась бы с реальной
// командой по имени «all». `*` slug'ом быть не может ни при каких условиях.
export const TEAM_SCOPE_ALL = "*";

const SCOPE_STORAGE_KEY = "nexus.team.scope";

// Реактивный микро-стор поверх sessionStorage.
//
// В проекте нет ни Context, ни zustand — состояние живёт в react-query. Но режим
// просмотра не является серверными данными: запрашивать его нечем и инвалидировать
// нечего. useSyncExternalStore — штатный способ React 18 подружить внешнее
// хранилище с рендером; альтернатива (читать sessionStorage при рендере) не
// перерисовывала бы переключатель при смене режима.
let allTeamsScope = readMirror();
const listeners = new Set<() => void>();

function readMirror(): boolean {
  try {
    return sessionStorage.getItem(SCOPE_STORAGE_KEY) === "1";
  } catch {
    // Приватный режим браузера / отключённое хранилище: режим просто не
    // переживёт переход между страницами. Ронять интерфейс из-за этого нельзя.
    return false;
  }
}

function writeMirror(all: boolean): void {
  try {
    if (all) sessionStorage.setItem(SCOPE_STORAGE_KEY, "1");
    else sessionStorage.removeItem(SCOPE_STORAGE_KEY);
  } catch {
    // см. readMirror — хранилище недоступно, режим живёт только в текущем экране.
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// setTeamScopeAll — включить/выключить сквозной режим.
//
// Параметр `?team` отсюда НЕ пишется: его единственный писатель — useTeamUrlParam
// (§76.4), и второй писатель вернул бы ровно ту гонку двух `setSearchParams`,
// которую §76.7 лечил двухпроходной записью. Здесь меняется только режим, а
// зеркало URL догоняет его своим эффектом.
export function setTeamScopeAll(all: boolean): void {
  if (allTeamsScope === all) return;
  allTeamsScope = all;
  writeMirror(all);
  listeners.forEach((l) => l());
}

// useAllTeamsScope — включён ли сквозной режим.
export function useAllTeamsScope(): boolean {
  return useSyncExternalStore(subscribe, () => allTeamsScope, () => false);
}

// teamScopeKey — часть queryKey, различающая режимы.
//
// Ключ team-scoped запроса ОБЯЗАН включать режим: иначе выдача «всех команд» и
// выдача одной команды алиасятся в один слот кеша, и react-query мгновенно
// отдаёт чужой список (правило §4.44 — из-за него уже был баг «стёр поиск →
// узлы не той команды»).
export function teamScopeKey(allTeams: boolean, teamId: string): string {
  return allTeams ? TEAM_SCOPE_ALL : teamId;
}

// scopeParams — query-параметры запроса под текущий режим (§86.3).
export function scopeParams(allTeams: boolean): Record<string, string> {
  return allTeams ? { scope: "all" } : {};
}
