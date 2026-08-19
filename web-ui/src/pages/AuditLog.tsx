import { useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { Download } from "lucide-react";

import { api } from "../api/client";
import { scopeParams, teamScopeKey, useAllTeamsScope } from "../lib/teamScope";
import { useCurrentTeamID, useMyTeams } from "../lib/teams";
import { useInfiniteList } from "../lib/useInfiniteList";
import { AuditDetailsCell } from "../components/AuditDetailsCell";
import { Button, Card, Chip, ErrorAlert, Select } from "../components/ui";

// PAGE_SIZE — размер страницы журнала. Меньше потолка накопления
// (MAX_INFINITE_ROWS = 1000), чтобы до него нужно было именно листать.
const PAGE_SIZE = 100;

type Entry = {
  id: string;
  user_login: string;
  action: string;
  // target_type/target_id/ip_address отдаются бэком с `omitempty` —
  // у событий без цели (логины и т.п.) их в JSON просто нет → undefined.
  target_type?: string;
  target_id?: string;
  details?: Record<string, unknown>;
  ip_address?: string;
  // §86.7: команда записи. omitempty — у глобальных действий admin'а (§18.1)
  // её нет вовсе.
  team_id?: string;
  created_at: string;
};
type Resp = { items: Entry[] };

// AuditCursor — keyset-курсор §91.1: метка времени и id последней показанной
// записи. Только время не годится — метки не уникальны, и на их совпадении
// записи терялись бы или дублировались на границе страницы.
type AuditCursor = { beforeTS: string; beforeID: string };

// actionTone — окраска чипа действия по префиксу/семантике.
function actionTone(action: string): "default" | "info" | "success" | "danger" | "warning" {
  if (action.endsWith(".create")) return "success";
  if (action.endsWith(".delete") || action.includes("failed") || action.includes("drop"))
    return "danger";
  if (action.endsWith(".update")) return "info";
  // §88.9: подтверждение означает состоявшуюся смену пароля. А вот запрос
  // восстановления красить «успехом» нельзя — он пишется и для
  // безрезультатных попыток, исход лежит в details.result; он остаётся
  // нейтральным по общему правилу ниже.
  if (action === "user.password_reset.confirm") return "success";
  return "default";
}

export default function AuditLog() {
  const { t } = useTranslation();
  // Фильтр хранится в URL (?action=…), чтобы переживать F5 (П12): раньше был
  // в useState и сбрасывался при перезагрузке страницы.
  const [params, setParams] = useSearchParams();
  const filter = params.get("action") ?? "";
  const setFilter = (v: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (v) next.set("action", v);
        else next.delete("action");
        return next;
      },
      { replace: true },
    );

  // teamId в ключе: /api/audit фильтруется по команде сессии — без него записи
  // разных команд алиасятся в один слот кеша (см. useCurrentTeamID в lib/teams).
  const teamId = useCurrentTeamID();
  // §86.7: сквозной режим — журнал всех команд пользователя. Скоуп обязан быть
  // в ключе, иначе выдачи режимов алиасятся в один слот кеша (правило §4.44).
  const allTeams = useAllTeamsScope();
  const scopeKey = teamScopeKey(allTeams, teamId);
  const scopeQuery = useMemo(() => scopeParams(allTeams), [allTeams]);

  // В сквозном режиме команда сессии не участвует — ждать её незачем.
  const enabled = allTeams || teamId !== "";
  // Фильтры списка и счётчика собираются в одном месте: разъехавшись, они дали
  // бы «показано N из M», где M посчитано по другому множеству.
  const filterParams = useMemo(
    () => ({ ...scopeQuery, ...(filter ? { action: filter } : {}) }),
    [scopeQuery, filter],
  );

  // §91.3: подгрузка по скроллу с keyset-курсором — тот же механизм, что у
  // логов узла. Раньше страница просила limit=200 без пагинации, и записи за
  // пределами этих двухсот были недостижимы (счётчика тоже не было, поэтому
  // обрезка выдачи ничем не показывалась).
  const wrapRef = useRef<HTMLDivElement>(null);
  const q = useInfiniteList<Entry, Resp, AuditCursor>({
    queryKey: ["audit", scopeKey, filter],
    enabled,
    containerRef: wrapRef,
    fetchPage: (cursor, signal) =>
      api.get<Resp>(
        "/api/audit",
        {
          ...filterParams,
          limit: PAGE_SIZE,
          ...(cursor ? { before_ts: cursor.beforeTS, before_id: cursor.beforeID } : {}),
        },
        { signal },
      ),
    getItems: (page) => page.items ?? [],
    getId: (row) => row.id,
    getCursor: (_page, items) => {
      if (items.length < PAGE_SIZE) return undefined; // недобор страницы = конец истории
      const oldest = items[items.length - 1]; // DESC → последняя самая старая
      return oldest ? { beforeTS: oldest.created_at, beforeID: oldest.id } : undefined;
    },
  });

  // «Показано N из M»: список отдаёт страницу, и по нему нельзя понять, есть ли
  // ещё записи. Лимит в счётчик не уходит — он отвечает «из скольких».
  const countQ = useQuery({
    queryKey: ["audit-count", scopeKey, filter],
    queryFn: () => api.get<{ count: number }>("/api/audit/count", filterParams),
    enabled,
  });

  // §86.3: имя команды резолвит клиент по членствам — сервер выдачу не
  // обогащает. Колонка появляется только в сквозном режиме.
  const teamsQ = useMyTeams();
  const teamNames = useMemo(() => {
    if (!allTeams) return null;
    const m = new Map<string, string>();
    for (const tm of teamsQ.data?.items ?? []) m.set(tm.id, tm.name);
    return m;
  }, [allTeams, teamsQ.data]);

  // Выгрузка обязана повторять скоуп экрана: иначе CSV молча отдал бы другое
  // множество записей, чем показано в таблице.
  const csvParams = new URLSearchParams();
  if (allTeams) csvParams.set("scope", "all");
  if (filter) csvParams.set("action", filter);
  const csvQuery = csvParams.toString();
  const csvHref = csvQuery ? `/api/audit/export.csv?${csvQuery}` : "/api/audit/export.csv";

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-lg font-semibold">{t("audit.title")}</h1>
        <Select className="w-44" value={filter} onChange={(e) => setFilter(e.target.value)}>
          <option value="">{t("audit.all_actions")}</option>
          <option value="node.create">node.create</option>
          <option value="node.update">node.update</option>
          <option value="node.delete">node.delete</option>
          <option value="node.replay">node.replay</option>
          <option value="node.dry_run">node.dry_run</option>
          <option value="node.copy">node.copy</option>
          <option value="user.login.success">user.login.success</option>
          <option value="user.login.failed">user.login.failed</option>
          <option value="user.password_reset.request">user.password_reset.request</option>
          <option value="user.password_reset.confirm">user.password_reset.confirm</option>
          <option value="api_token.create">api_token.create</option>
          <option value="api_token.revoke">api_token.revoke</option>
        </Select>
        <a href={csvHref} download>
          <Button sm>
            <Download className="h-3.5 w-3.5" /> {t("audit.export_csv")}
          </Button>
        </a>
      </div>

      {q.items.length > 0 && (
        <div className="text-xs text-fg-muted">
          {countQ.data
            ? t("audit.shown_of_total", { shown: q.items.length, total: countQ.data.count })
            : t("audit.shown_count", { shown: q.items.length })}
        </div>
      )}

      {!!q.query.error && <ErrorAlert />}

      {/* Карточка со скролл-контейнером рендерится всегда, а не по приходу
          данных: иначе при подгрузке структура страницы прыгала бы, а сам
          контейнер (к нему привязан обработчик скролла) появлялся бы позже
          первой страницы. */}
      <Card className="overflow-hidden p-0">
          {/* Высота считается от окна, а не фиксированные 70vh: под таблицей
              оставалась пустая полоса почти в треть экрана. 190px — это
              измеренные 165px над контейнером (шапка приложения, заголовок,
              фильтры, счётчик) плюс небольшой отступ снизу. min-h держит
              список читаемым на низких окнах, где calc дал бы слишком мало. */}
          <div
            ref={wrapRef}
            onScroll={q.onScroll}
            className="max-h-[calc(100vh-190px)] min-h-[320px] overflow-y-auto overflow-x-auto"
          >
          <table className="w-full min-w-[720px] text-[12.5px]">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
                <th className="px-3 py-2 font-medium">{t("audit.columns.when")}</th>
                {teamNames && (
                  <th className="px-3 py-2 font-medium">{t("overview.table.team")}</th>
                )}
                <th className="px-3 py-2 font-medium">{t("audit.columns.user")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.action")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.target")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.ip")}</th>
                <th className="px-3 py-2 font-medium">{t("audit.columns.details")}</th>
              </tr>
            </thead>
            <tbody>
              {q.items.map((e) => (
                <tr key={e.id} className="border-b border-line last:border-0 hover:bg-bg-muted">
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">
                    {new Date(e.created_at).toLocaleString()}
                  </td>
                  {teamNames && (
                    <td className="px-3 py-2">
                      <Chip tone="info">
                        {e.team_id ? (teamNames.get(e.team_id) ?? "—") : "—"}
                      </Chip>
                    </td>
                  )}
                  <td className="px-3 py-2">{e.user_login}</td>
                  <td className="px-3 py-2">
                    <Chip tone={actionTone(e.action)}>{e.action}</Chip>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                    {e.target_type || "—"}
                    {e.target_id ? `/${e.target_id.slice(0, 8)}` : ""}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{e.ip_address}</td>
                  <td className="max-w-lg px-3 py-2">
                    <AuditDetailsCell action={e.action} details={e.details} />
                  </td>
                </tr>
              ))}
              {q.items.length === 0 && (
                <tr>
                  {/* Колонок 6, а в сквозном режиме добавляется «Команда» (§86.7) —
                      с фиксированным colSpan строка не растягивалась на всю ширину. */}
                  <td colSpan={teamNames ? 7 : 6} className="px-3 py-6 text-center text-fg-muted">
                    {/* isPending, а не isLoading: при выключенном запросе (команда
                        сессии ещё не отрезолвлена) isLoading в react-query v5
                        равен false, и вместо загрузки показывалось «Нет записей». */}
                    {q.query.isPending ? t("common.loading") : t("audit.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>

          {/* Состояние подгрузки — под таблицей, внутри скролл-контейнера:
              иначе подсказка «больше нет» уезжает за пределы видимой области. */}
          {q.query.isFetchingNextPage && (
            <div className="px-3 py-3 text-center text-xs text-fg-muted">
              {t("common.loading")}
            </div>
          )}
          {!q.query.hasNextPage && q.items.length > 0 && (
            <div className="px-3 py-3 text-center text-xs text-fg-muted">
              {t("audit.no_more")}
            </div>
          )}
          {q.atCap && (
            <div className="px-3 py-3 text-center text-xs text-warn">
              {t("audit.cap_reached")}
            </div>
          )}
          </div>
      </Card>
    </div>
  );
}
