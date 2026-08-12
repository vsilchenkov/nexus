import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { Download } from "lucide-react";

import { api } from "../api/client";
import { scopeParams, teamScopeKey, useAllTeamsScope } from "../lib/teamScope";
import { useCurrentTeamID, useMyTeams } from "../lib/teams";
import { AuditDetailsCell } from "../components/AuditDetailsCell";
import { Button, Card, Chip, ErrorAlert, Select } from "../components/ui";

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

// actionTone — окраска чипа действия по префиксу/семантике.
function actionTone(action: string): "default" | "info" | "success" | "danger" | "warning" {
  if (action.endsWith(".create")) return "success";
  if (action.endsWith(".delete") || action.includes("failed") || action.includes("drop"))
    return "danger";
  if (action.endsWith(".update")) return "info";
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
  const q = useQuery({
    queryKey: ["audit", scopeKey, filter],
    queryFn: () =>
      api.get<Resp>("/api/audit", {
        ...scopeQuery,
        ...(filter ? { action: filter, limit: 200 } : { limit: 200 }),
      }),
    // В сквозном режиме команда сессии не участвует — ждать её незачем.
    enabled: allTeams || teamId !== "",
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
          <option value="api_token.create">api_token.create</option>
          <option value="api_token.revoke">api_token.revoke</option>
        </Select>
        <a href={csvHref} download>
          <Button sm>
            <Download className="h-3.5 w-3.5" /> {t("audit.export_csv")}
          </Button>
        </a>
      </div>

      {q.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {q.error && <ErrorAlert />}

      {q.data && (
        <Card className="overflow-hidden p-0">
          <div className="overflow-x-auto">
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
              {q.data.items.map((e) => (
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
              {q.data.items.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center text-fg-muted">
                    {t("audit.empty")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
          </div>
        </Card>
      )}
    </div>
  );
}
