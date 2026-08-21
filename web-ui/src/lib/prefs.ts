import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { api } from "../api/client";
import { defaultPeriod, isChartStep, parsePeriodPref, type ChartStep, type Period } from "./period";

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

// PREF_KEY_NODE_PERIOD — дефолтный период вкладок узла «Обзор» и «Очередь» (§92).
// Ключ один на обе вкладки: горизонт наблюдения — свойство узла, а не вкладки,
// и раздельные ключи означали бы, что «По умолчанию» на одной вкладке ничего не
// меняет на соседней.
export const PREF_KEY_NODE_PERIOD = "node.period";

// PREF_KEY_KAFKA_PERIOD — дефолтный период монитора Kafka (§92). Отдельный от
// узлового: у брокера свой горизонт (размеры топиков смотрят сутками, трафик
// узла — часами).
export const PREF_KEY_KAFKA_PERIOD = "kafka.period";

// PREF_KEY_REJECTED_PERIOD — дефолтный период вкладки «Логи → Отказы» (§94.7).
// Отдельный от рабочего стола: горизонт разбора отказов («что стучится прямо
// сейчас» либо «что накопилось за неделю») не связан с периодом графиков.
// Скоуп — команда: журнал отказов у каждой команды свой.
export const PREF_KEY_REJECTED_PERIOD = "rejected.period";

// PREF_KEY_FAVORITE_ALL_TEAMS — «Все команды» в избранном (§86.5).
//
// Почему преф, а не user_team_favorites: у той таблицы составной внешний ключ на
// user_teams(user_id, team_id) с инвариантом «избранное ⊆ членство», и
// псевдо-идентификатора режима туда не вставить. Хранилище §71 для того и
// сделано generic'ом — новый ключ не требует ни миграции, ни правки бэкенда.
//
// Преф ГЛОБАЛЬНЫЙ (team_id = ""): режим не принадлежит ни одной команде.
export const PREF_KEY_FAVORITE_ALL_TEAMS = "teams.favorite_all";

// PREF_KEY_NODE_METRICS_VIEW_PREFIX — зеркало
// domain.PreferenceKeyNodeMetricsViewPrefix (§84.3).
export const PREF_KEY_NODE_METRICS_VIEW_PREFIX = "node.metrics.view.";

/**
 * prefKeyNodeMetricsView — ключ вида вкладки «Метрики» для конкретного узла.
 *
 * Дефисы снимаются не для красоты: формат ключа §71 их не допускает. UUID без
 * дефисов — 32 символа нижнего hex, с префиксом выходит 50 при потолке 64.
 * Зеркало domain.PreferenceKeyNodeMetricsView — расхождение здесь означало бы
 * молчаливую потерю настройки, поэтому обе стороны закрыты тестами.
 */
export function prefKeyNodeMetricsView(nodeId: string): string {
  return PREF_KEY_NODE_METRICS_VIEW_PREFIX + nodeId.replaceAll("-", "");
}

// PresetPeriod — период-пресет. В префе хранится только он: произвольный
// календарный диапазон в роли дефолта бессмыслен (завтра он уже прошлое), и
// тип это фиксирует, а не только комментарий.
export type PresetPeriod = Extract<Period, { kind: "preset" }>;

// NodeMetricsView — что запоминается для узла: период (только пресет) и шаг.
export type NodeMetricsView = { period: PresetPeriod | null; step: ChartStep | null };

/**
 * parseNodeMetricsViewPref — разбор значения префа (§84.3).
 *
 * Значение приходит с сервера как есть — он его не валидирует (§71.3), контракт
 * держит эта функция. Каждое поле разбирается независимо: испорченный шаг не
 * должен обнулять сохранённый период.
 *
 * Произвольный период в преф не попадает по построению (parsePeriodPref
 * пропускает только пресет): календарный диапазон в роли дефолта бессмыслен —
 * завтра он уже прошлое.
 */
export function parseNodeMetricsViewPref(raw: unknown): NodeMetricsView {
  if (!raw || typeof raw !== "object") return { period: null, step: null };
  const v = raw as { range?: unknown; step?: unknown };
  const parsed = parsePeriodPref({ kind: "preset", range: v.range });
  return {
    period: parsed?.kind === "preset" ? parsed : null,
    step: isChartStep(v.step) ? v.step : null,
  };
}

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
// преф → системные 24ч. Глобальный преф ставится кнопкой «По умолчанию» в
// сквозном режиме (§92) и появляется после разовой миграции прежнего
// localStorage-значения (§71.5); он служит дефолтом для команд, где
// пользователь звёздочку ещё не нажимал.
//
// key — какой экран спрашивает (§92): рабочий стол, вкладки узла или монитор
// Kafka. Дефолтное значение оставлено ради вызовов §71, которые про другие
// экраны не знают.
export function useTeamDefaultPeriod(
  teamId: string,
  key: string = PREF_KEY_OVERVIEW_PERIOD,
): PrefsState<Period> {
  const q = usePrefs();
  const settled = !q.isPending;
  const items = q.data?.items ?? [];

  const pick = (tid: string): Period | null => {
    const found = items.find((p) => p.key === key && p.team_id === tid);
    return found ? parsePeriodPref(found.value) : null;
  };

  // teamId пустой (членства ещё не загрузились) — командный преф искать не по
  // чему, но глобальный уже применим.
  const value = (teamId !== "" ? pick(teamId) : null) ?? pick("") ?? defaultPeriod;
  return { value, settled };
}

/**
 * useNodeMetricsViewPref — сохранённый вид вкладки «Метрики» ЭТОГО узла (§84.3).
 *
 * Одна строка префа на узел, поэтому здесь нет ни двухуровневого резолва (как
 * у периода рабочего стола), ни глобального запасного значения: промежуточного
 * «общего вида на все узлы» в §84.3 намеренно нет — системный дефолт после
 * §84.1 сам стал круглым и предсказуемым, а третий уровень пришлось бы
 * объяснять в интерфейсе.
 *
 * settled — «запрос завершён», а не «успешен» (та же калька §71, см. PrefsState):
 * недоступный /api/me/prefs не имеет права навсегда оставить вкладку без
 * метрик. Гейт готовности у вызывающей стороны строится именно на нём.
 */
export function useNodeMetricsViewPref(nodeId: string): PrefsState<NodeMetricsView> {
  const q = usePrefs();
  const settled = !q.isPending;
  const key = prefKeyNodeMetricsView(nodeId);
  const found = (q.data?.items ?? []).find((p) => p.key === key);
  return { value: parseNodeMetricsViewPref(found?.value), settled };
}

/**
 * useFavoriteAllTeams — лежит ли «Все команды» в избранном (§86.5).
 *
 * Значение читается терпимо: любое не-`true` считается «не в избранном».
 * Ошибка запроса префов сюда не эскалируется — избранное декорация, и ронять
 * из-за неё сайдбар нельзя (та же линия, что FavoriteTeamIDs §49.5).
 */
export function useFavoriteAllTeams(): boolean {
  const q = usePrefs();
  return (q.data?.items ?? []).some(
    (p) => p.key === PREF_KEY_FAVORITE_ALL_TEAMS && p.team_id === "" && p.value === true,
  );
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
