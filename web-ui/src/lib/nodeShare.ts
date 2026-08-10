import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import { api, isNotFound } from "../api/client";
import { shareableTabParams, type NodeTab } from "./nodeTabUrl";
import { useMyTeams, useSwitchTeam } from "./teams";

// Слой данных «Поделиться узлом» (§58). Узел жёстко привязан к команде
// (multi-tenancy §18), а team-scoped GET /api/nodes/:id отдаёт 404, если
// команда узла ≠ текущей команде сессии. Поэтому шаренная ссылка на страницу
// узла из другой команды у коллеги не открылась бы. Здесь: (1) резолв команды
// узла отдельным endpoint'ом (членство проверяет бэкенд), (2) одноразовое
// авто-переключение сессии на команду узла при открытии страницы.

// NodeTeam — ответ GET /api/nodes/:id/team: команда, которой принадлежит узел.
export type NodeTeam = {
  team_id: string;
  team_slug: string;
  team_name: string;
};

// useNodeTeam — команда узла (независимо от текущей команды сессии). 404 =
// узла нет ИЛИ пользователь не член его команды (no-leak, §58 п.3). 4xx не
// ретраятся глобальной политикой QueryClient — 404 приходит сразу.
//
// refetchOnMount:"always" — не оптимизация, а требование корректности: ответ
// управляет ПОБОЧНЫМ ЭФФЕКТОМ (переключением команды сессии), а закешированное
// значение описывает команду узла на момент прошлого запроса. Узел можно
// перенести в другую команду (Phase 11.B, POST /api/nodes/:id/move), и тогда
// кеш становится ложью: открытие узла уводило сессию в СТАРУЮ команду. Запрос
// дешёвый (узел + членства), зато решение принимается по свежим данным.
export function useNodeTeam(id: string | undefined) {
  return useQuery({
    queryKey: ["node-team", id],
    queryFn: () => api.get<NodeTeam>(`/api/nodes/${id}/team`),
    enabled: !!id,
    refetchOnMount: "always",
  });
}

// EnsureStatus — состояние подготовки страницы узла к показу (§58):
//   loading      — резолвим команду узла / ждём членства;
//   switching    — команда узла ≠ текущей, идёт авто-переключение сессии;
//   ready        — команда узла активна, узел можно грузить team-scoped GET'ом;
//   unavailable  — узла нет или его команда пользователю недоступна («Узел не доступен»).
export type EnsureStatus = "loading" | "switching" | "ready" | "unavailable";

// useEnsureNodeTeam — гарантирует, что текущая команда сессии совпадает с
// командой открываемого узла (§58, п.2). Переключение делается РОВНО ОДИН РАЗ
// на пару (узел, его команда): последующие ручные переключения команды в
// шапке пользователь делает осознанно — не откатываем их назад. После первого
// достижения ready статус залипает (latchedKey), чтобы ручное переключение
// команды на странице не сбрасывало UI в «loading» (детальная сама обработает
// 404 узла, форма — покажет баннер foreign_team).
//
// Перенос узла между командами (Move, Phase 11.B) — отдельный случай, ради
// которого guard и залипание ключуются ПАРОЙ, а решение принимается только по
// свежему ответу (см. ниже): смена команды в шапке пару не меняет (ручное
// переключение остаётся в силе), а переехавший узел приносит новую команду и
// обязан переключить сессию заново.
export function useEnsureNodeTeam(id: string | undefined): { status: EnsureStatus } {
  const teamQ = useNodeTeam(id);
  const { data: myTeams } = useMyTeams();
  const { mutate: switchTeam } = useSwitchTeam();

  // Решение принимаем по ответу, полученному после открытия страницы
  // (isFetchedAfterMount). react-query первым рендером отдаёт закешированное
  // значение, а оно могло быть снято ДО переноса узла в другую команду —
  // одноразовый guard срабатывал на устаревшей команде и переключал сессию
  // назад в неё («перенёс узел, открываю — снова старая команда»). Ждать
  // свежего ответа безопасно: страница и так показывает «loading», пока
  // команда узла не резолвится.
  //
  // Оговорка: isFetchedAfterMount поднимается и на ОШИБКЕ запроса, а data при
  // этом остаётся закешированной — то есть при недоступном резолвере (5xx,
  // сеть) решение сознательно деградирует в кеш. Это лучше, чем вечный
  // «loading»: страница открывается как до фикса. 404 сюда не попадает — он
  // разбирается ниже как unavailable (§58 п.3).
  const target = teamQ.isFetchedAfterMount ? teamQ.data?.team_id : undefined;
  const current = myTeams?.current_team_id;
  const key = id && target ? `${id}:${target}` : null;

  // Одноразовое авто-переключение на команду узла при его открытии.
  const handledKey = useRef<string | null>(null);
  useEffect(() => {
    if (!key || !target || !current) return;
    if (handledKey.current === key) return; // эту пару уже разобрали
    handledKey.current = key;
    if (target !== current) switchTeam(target);
  }, [key, target, current, switchTeam]);

  // Залипание ready: как только команда узла стала активной — держим ready,
  // даже если пользователь потом вручную сменит команду в шапке.
  const [latchedKey, setLatchedKey] = useState<string | null>(null);
  useEffect(() => {
    if (key && current && target === current) setLatchedKey(key);
  }, [key, current, target]);

  if (isNotFound(teamQ.error)) return { status: "unavailable" };
  if (!id || !target || !current) return { status: "loading" };
  if (target === current || latchedKey === key) return { status: "ready" };
  return { status: "switching" };
}

// nodePageUrl — абсолютная ссылка на страницу узла в UI для кнопки «Поделиться»
// (§58, п.1). Берём origin текущего окна (адрес UI), путь — стабильный
// id-роут /nodes/:id (team-независимый: команду резолвит useEnsureNodeTeam).
//
// §79.3: с tab ссылка ведёт сразу на нужную вкладку. Окно и фильтры журнала в
// неё НЕ попадают — это сиюминутное состояние клика по графику, а не то, чем
// делятся; «Обзор» (вкладка по умолчанию) параметром не отмечается.
//
// §84.2: с search ссылка несёт ещё и вид вкладки — период и шаг «Метрик»
// (shareableTabParams). Без этого повторялся урок §79: адрес у вкладки есть, а
// поделиться конкретным масштабом нечем. Что именно шарится — решает
// nodeTabUrl, здесь только сборка строки.
export function nodePageUrl(id: string, tab?: NodeTab, search?: URLSearchParams): string {
  const base = `${window.location.origin}/nodes/${id}`;
  const q = new URLSearchParams();
  if (tab && tab !== "overview") q.set("tab", tab);
  if (tab && search) {
    for (const key of shareableTabParams(tab)) {
      const v = search.get(key);
      if (v !== null) q.set(key, v);
    }
  }
  const s = q.toString();
  return s ? `${base}?${s}` : base;
}
