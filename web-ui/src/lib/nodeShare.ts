import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import { api, isNotFound } from "../api/client";
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
export function useNodeTeam(id: string | undefined) {
  return useQuery({
    queryKey: ["node-team", id],
    queryFn: () => api.get<NodeTeam>(`/api/nodes/${id}/team`),
    enabled: !!id,
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
// на открытие узла (guard по id): последующие ручные переключения команды в
// шапке пользователь делает осознанно — не откатываем их назад. После первого
// достижения ready статус залипает (latchedId), чтобы ручное переключение
// команды на странице не сбрасывало UI в «loading» (детальная сама обработает
// 404 узла, форма — покажет баннер foreign_team).
export function useEnsureNodeTeam(id: string | undefined): { status: EnsureStatus } {
  const teamQ = useNodeTeam(id);
  const { data: myTeams } = useMyTeams();
  const { mutate: switchTeam } = useSwitchTeam();

  const target = teamQ.data?.team_id;
  const current = myTeams?.current_team_id;

  // Одноразовое авто-переключение на команду узла при его открытии.
  const handledId = useRef<string | null>(null);
  useEffect(() => {
    if (!id || !target || !current) return;
    if (handledId.current === id) return; // этот узел уже разобрали
    handledId.current = id;
    if (target !== current) switchTeam(target);
  }, [id, target, current, switchTeam]);

  // Залипание ready: как только команда узла стала активной — держим ready,
  // даже если пользователь потом вручную сменит команду в шапке.
  const [latchedId, setLatchedId] = useState<string | null>(null);
  useEffect(() => {
    if (id && target && current && target === current) setLatchedId(id);
  }, [id, target, current]);

  if (isNotFound(teamQ.error)) return { status: "unavailable" };
  if (!id || !target || !current) return { status: "loading" };
  if (target === current || latchedId === id) return { status: "ready" };
  return { status: "switching" };
}

// nodePageUrl — абсолютная ссылка на страницу узла в UI для кнопки «Поделиться»
// (§58, п.1). Берём origin текущего окна (адрес UI), путь — стабильный
// id-роут /nodes/:id (team-независимый: команду резолвит useEnsureNodeTeam).
export function nodePageUrl(id: string): string {
  return `${window.location.origin}/nodes/${id}`;
}
