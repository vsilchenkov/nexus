import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { api } from "../api/client";
import { defaultPeriod, parsePeriodPref, type Period } from "./period";

// Слой данных персональных предпочтений (§71). Префы хранятся на бэкенде
// (PostgreSQL, таблица user_preferences), привязаны к пользователю и —
// опционально — к команде. Первый и пока единственный ключ:
// overview.period — дефолтный период рабочего стола («звёздочка» §44.B).
//
// Ключ ["me-prefs"] попадает в TEAM_INDEPENDENT_KEYS (lib/teams.ts) — не
// потому, что фича вне команд, а наоборот: ответ несёт префы ВСЕХ команд
// сразу и от current_team не зависит.

export const ME_PREFS_KEY = ["me-prefs"] as const;

// PREF_KEY_OVERVIEW_PERIOD — зеркало domain.PreferenceKeyOverviewPeriod.
export const PREF_KEY_OVERVIEW_PERIOD = "overview.period";

// UserPref — одна запись предпочтений. team_id пустой = глобальный преф
// (действует во всех командах, перекрывается командным).
export type UserPref = {
  team_id: string;
  key: string;
  value: unknown;
  updated_at: string;
};

type PrefsResp = { items: UserPref[] };

// usePrefs — ВСЕ префы пользователя одним запросом.
//
// Одним запросом, а не «префы текущей команды», сознательно: при переключении
// команды её дефолт обязан быть известен в том же рендере, где меняется teamId.
// Иначе между сменой команды и приходом её префа период показывал бы чужое
// значение, а фильтры успели бы записать в URL не тот период.
//
// staleTime длиннее общего (30с в queryClient.ts): это настройка, которую
// меняет сам пользователь и только из этого же UI, — перечитывать её на каждый
// фокус окна незачем.
export function usePrefs() {
  return useQuery({
    queryKey: ME_PREFS_KEY,
    queryFn: () => api.get<PrefsResp>("/api/me/prefs"),
    staleTime: 5 * 60_000,
  });
}

// PrefsState — разрешённое значение префа плюс признак «запрос завершён».
//
// settled = !isPending, а НЕ isSuccess: при недоступном эндпоинте (5xx, сеть,
// старый бэкенд без /api/me/prefs) запрос уходит в error, и гейт по isSuccess
// навсегда оставил бы рабочий стол без метрик — личная настройка уронила бы
// основную функцию страницы. На ошибке отдаём системный дефолт и работаем.
type PrefsState<T> = { value: T; settled: boolean };

// useTeamDefaultPeriod — дефолтный период команды: преф команды → глобальный
// преф → системные 24ч. Глобальный преф появляется после разовой миграции
// прежнего localStorage-значения (§71.5) и служит дефолтом для команд, где
// пользователь звёздочку ещё не нажимал.
export function useTeamDefaultPeriod(teamId: string): PrefsState<Period> {
  const q = usePrefs();
  const settled = !q.isPending;
  const items = q.data?.items ?? [];

  const pick = (tid: string): Period | null => {
    const found = items.find((p) => p.key === PREF_KEY_OVERVIEW_PERIOD && p.team_id === tid);
    return found ? parsePeriodPref(found.value) : null;
  };

  // teamId пустой (членства ещё не загрузились) — командный преф искать не по
  // чему, но глобальный уже применим.
  const value = (teamId !== "" ? pick(teamId) : null) ?? pick("") ?? defaultPeriod;
  return { value, settled };
}

type SetPrefVars = { teamId: string; key: string; value: unknown };

// useSetPref — upsert одного префа. Optimistic: кеш правится сразу (звёздочка
// заливается без ожидания сети), при ошибке — откат. Приём useSetFavoriteTeams
// (lib/teams.ts).
export function useSetPref() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ teamId, key, value }: SetPrefVars) =>
      api.put("/api/me/prefs", { team_id: teamId, key, value }),
    onMutate: async ({ teamId, key, value }) => {
      await qc.cancelQueries({ queryKey: ME_PREFS_KEY });
      const prev = qc.getQueryData<PrefsResp>(ME_PREFS_KEY);
      if (prev) {
        const rest = prev.items.filter((p) => !(p.key === key && p.team_id === teamId));
        qc.setQueryData<PrefsResp>(ME_PREFS_KEY, {
          items: [...rest, { team_id: teamId, key, value, updated_at: "" }],
        });
      }
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(ME_PREFS_KEY, ctx.prev);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ME_PREFS_KEY }),
  });
}

// LEGACY_PERIOD_KEY — прежнее место хранения дефолтного периода (§44.B):
// localStorage, один ключ на браузер, без привязки к пользователю и команде.
// Читается только миграцией ниже.
//
// TODO(§71.5): удалить ключ и migrateLegacyPeriodPref через два релиза после
// выпуска §71 — к тому моменту у активных пользователей значение уже переедет
// на сервер.
const LEGACY_PERIOD_KEY = "nexus.overview.period";

// loadLegacyPeriod — прежний дефолтный период из localStorage, если он там
// есть и валиден. Толерантно к недоступному хранилищу (приватный режим).
function loadLegacyPeriod(): Period | null {
  try {
    const raw = localStorage.getItem(LEGACY_PERIOD_KEY);
    if (!raw) return null;
    return parsePeriodPref(JSON.parse(raw));
  } catch {
    return null;
  }
}

function clearLegacyPeriod(): void {
  try {
    localStorage.removeItem(LEGACY_PERIOD_KEY);
  } catch {
    // приватный режим — миграция всё равно уже отработала на сервере
  }
}

// useMigrateLegacyPeriodPref — разовый перенос прежнего localStorage-значения
// в ГЛОБАЛЬНЫЙ преф пользователя (§71.5).
//
// Глобальный, а не командный: старое значение было глобальным по смыслу («мой
// дефолт»), и так пользователь с пятью командами сохраняет привычный период
// везде, а звёздочка потом переопределяет отдельные команды.
//
// Условия все сразу: префы загружены (иначе перезапишем серверное значение
// протухшим из другого браузера), в localStorage есть валидный пресет, и
// глобального префа на сервере ещё нет. Ключ удаляется только после успешного
// PUT — при ошибке миграция повторится в следующей сессии (она идемпотентна).
export function useMigrateLegacyPeriodPref(): void {
  const q = usePrefs();
  const setPref = useSetPref();
  const done = useRef(false);

  const settled = !q.isPending;
  const hasGlobal = (q.data?.items ?? []).some(
    (p) => p.key === PREF_KEY_OVERVIEW_PERIOD && p.team_id === "",
  );

  useEffect(() => {
    if (done.current || !settled || hasGlobal) return;
    const legacy = loadLegacyPeriod();
    if (!legacy) return;
    done.current = true;
    setPref.mutate(
      { teamId: "", key: PREF_KEY_OVERVIEW_PERIOD, value: legacy },
      { onSuccess: () => clearLegacyPeriod() },
    );
  }, [settled, hasGlobal, setPref]);
}
