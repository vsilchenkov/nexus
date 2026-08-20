import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Download } from "lucide-react";

import { api } from "../../api/client";
import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { useCurrentTeamID } from "../../lib/teams";
import { useRoleAtLeast } from "../../lib/useCurrentRole";
import { PREF_KEY_REJECTED_PERIOD, useTeamDefaultPeriod } from "../../lib/prefs";
import {
  REJECT_REASONS,
  reasonLabelKey,
  type RejectedGroup,
  type RejectedListResp,
  type RejectedSummary,
} from "../../lib/rejected";
import { RejectedDrawer } from "../../components/logs/RejectedDrawer";
import {
  Button,
  Card,
  DefaultPeriodButton,
  ErrorAlert,
  PeriodPicker,
  SearchInput,
  Select,
  periodKey,
  periodWindow,
  type Period,
} from "../../components/ui";

const PAGE_SIZE = 100;

// windowParams — период в границы last_seen для API (§94.6). RFC3339, как у
// остальных эндпоинтов с периодом.
function windowParams(p: Period): { from: string; to: string } {
  const w = periodWindow(p);
  return { from: new Date(w.since).toISOString(), to: new Date(w.until).toISOString() };
}

// RejectedTab — журнал отказов на входе (§94.7): группы «куда и почему»,
// карточка с клиентами и последними запросами.
export default function RejectedTab() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const isAdmin = useRoleAtLeast("admin");
  const teamID = useCurrentTeamID();

  // Период — тот же механизм, что на остальных экранах (§92): преф команды →
  // глобальный преф → системные 24ч. Отдельный ключ: горизонт разбора отказов
  // не связан с периодом графиков рабочего стола.
  const { value: savedDefault, settled: periodReady } = useTeamDefaultPeriod(
    teamID,
    PREF_KEY_REJECTED_PERIOD,
  );
  const [picked, setPicked] = useState<Period | null>(null);
  const period = picked ?? savedDefault;

  const [reason, setReason] = useState("");
  const [query, setQuery] = useState("");
  const [includeResolved, setIncludeResolved] = useState(false);
  const [openID, setOpenID] = useState<string | null>(null);

  const params = useMemo(() => {
    const p: Record<string, string> = { ...windowParams(period), limit: String(PAGE_SIZE) };
    if (reason) p.reasons = reason;
    if (query.trim()) p.q = query.trim();
    if (includeResolved) p.include_resolved = "true";
    return p;
  }, [period, reason, query, includeResolved]);

  const pk = periodKey(period);
  const listQ = useQuery({
    queryKey: ["rejected", "list", pk, reason, query.trim(), includeResolved],
    queryFn: () => api.get<RejectedListResp>("/api/rejected", params),
    enabled: periodReady,
  });
  const summaryQ = useQuery({
    queryKey: ["rejected", "summary", pk],
    queryFn: () => api.get<RejectedSummary>("/api/rejected/summary", windowParams(period)),
    enabled: periodReady,
  });

  // Выгрузка обязана повторять фильтры экрана: иначе CSV молча отдал бы другое
  // множество групп, чем показано в таблице (тот же приём, что в аудите).
  const csvHref = "/api/rejected/export.csv?" + new URLSearchParams(params).toString();

  const groups = listQ.data?.groups ?? [];
  const collecting = listQ.data?.collecting ?? true;
  const retentionDays = listQ.data?.retention_days ?? 0;

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["rejected"] });
  };

  return (
    <div className="space-y-3">
      {!collecting && (
        <Card className="border-warn/40 bg-warn/5 p-3 text-[13px]">
          {t("rejected.disabled_hint")}{" "}
          <a className="text-accent hover:underline" href="/settings/general">
            {t("rejected.disabled_link")}
          </a>
        </Card>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-auto text-[13px] text-fg-muted">
          {summaryQ.data ? (
            <span>
              {t("rejected.summary", {
                total: fmtNum(summaryQ.data.count),
                clients: fmtNum(summaryQ.data.clients),
                groups: fmtNum(summaryQ.data.groups),
              })}
              {summaryQ.data.unresolved > 0 && (
                <span className="ml-2 text-warn">
                  {t("rejected.unresolved", { n: fmtNum(summaryQ.data.unresolved) })}
                </span>
              )}
            </span>
          ) : (
            <span>{collecting ? t("common.loading") : ""}</span>
          )}
        </div>
        {periodReady && (
          <>
            <PeriodPicker value={period} onChange={setPicked} />
            <DefaultPeriodButton
              period={period}
              savedDefault={savedDefault}
              teamId={teamID}
              prefKey={PREF_KEY_REJECTED_PERIOD}
              title={t("rejected.set_default_period_hint")}
            />
          </>
        )}
        <a href={csvHref} download>
          <Button sm>
            <Download className="h-3.5 w-3.5" /> {t("rejected.export_csv")}
          </Button>
        </a>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Select className="w-56" value={reason} onChange={(e) => setReason(e.target.value)}>
          <option value="">{t("rejected.filter.all_reasons")}</option>
          {REJECT_REASONS.map((r) => (
            <option key={r} value={r}>
              {t(reasonLabelKey(r))}
            </option>
          ))}
        </Select>
        <SearchInput
          className="min-w-[240px] flex-1"
          type="text"
          placeholder={t("rejected.filter.search_placeholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <label className="flex cursor-pointer items-center gap-2 text-[13px] text-fg-muted">
          <input
            type="checkbox"
            checked={includeResolved}
            onChange={(e) => setIncludeResolved(e.target.checked)}
            className="h-4 w-4 accent-accent"
          />
          {t("rejected.filter.include_resolved")}
        </label>
      </div>

      {listQ.isError && <ErrorAlert />}

      <Card className="overflow-hidden p-0">
        <div className="max-h-[calc(100vh-320px)] min-h-[280px] overflow-auto">
          <table className="w-full min-w-[760px] text-[12.5px]">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
                <th className="px-3 py-2 font-medium">{t("rejected.columns.reason")}</th>
                <th className="px-3 py-2 font-medium">{t("rejected.columns.address")}</th>
                {isAdmin && <th className="px-3 py-2 font-medium">{t("rejected.columns.team")}</th>}
                <th className="px-3 py-2 text-right font-medium">{t("rejected.columns.clients")}</th>
                <th className="px-3 py-2 text-right font-medium">{t("rejected.columns.requests")}</th>
                <th className="px-3 py-2 font-medium">{t("rejected.columns.last_seen")}</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <RejectedRow
                  key={g.id}
                  group={g}
                  showTeam={isAdmin}
                  onOpen={() => setOpenID(g.id)}
                />
              ))}
            </tbody>
          </table>

          {!listQ.isLoading && groups.length === 0 && (
            <div className="p-6 text-center text-[13px] text-fg-muted">
              {collecting ? t("rejected.empty") : t("rejected.empty_disabled")}
            </div>
          )}
          {listQ.isLoading && (
            <div className="p-6 text-center text-[13px] text-fg-muted">{t("common.loading")}</div>
          )}
        </div>
      </Card>

      {(listQ.data?.total ?? 0) > groups.length && (
        <div className="text-xs text-fg-muted">
          {t("rejected.shown_of_total", { shown: groups.length, total: listQ.data?.total ?? 0 })}
        </div>
      )}

      {collecting && retentionDays > 0 && (
        <div className="text-xs text-fg-subtle">
          {t("rejected.retention_hint", { days: retentionDays })}
        </div>
      )}

      {openID && (
        <RejectedDrawer id={openID} onClose={() => setOpenID(null)} onChanged={invalidate} />
      )}
    </div>
  );
}

function RejectedRow({
  group,
  showTeam,
  onOpen,
}: {
  group: RejectedGroup;
  showTeam: boolean;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  const label = reasonLabelKey(group.reason);
  return (
    <tr
      onClick={onOpen}
      className={cn(
        "cursor-pointer border-b border-line/60 last:border-0 hover:bg-bg-muted",
        group.resolved_at && "opacity-60",
      )}
    >
      <td className="px-3 py-2 whitespace-nowrap">
        <span className="font-mono text-[11.5px] text-fg-muted">{group.status}</span>{" "}
        {label ? t(label) : group.reason}
      </td>
      <td className="px-3 py-2">
        <span className="font-mono text-[11.5px] text-fg-muted">{group.http_method}</span>{" "}
        <span className="break-all">{group.node_path}</span>
      </td>
      {showTeam && (
        <td className="px-3 py-2 whitespace-nowrap">
          {group.team_name || (
            // Слог, которому не нашлось команды: обращение по адресу
            // несуществующей команды — это тоже находка, а не пустая ячейка.
            <span className="text-warn" title={t("rejected.unknown_team_hint")}>
              {group.team_slug} ?
            </span>
          )}
        </td>
      )}
      <td className="px-3 py-2 text-right tabular-nums">{group.clients}</td>
      <td className="px-3 py-2 text-right tabular-nums">{fmtNum(group.count)}</td>
      <td className="px-3 py-2 whitespace-nowrap text-fg-muted">
        {new Date(group.last_seen).toLocaleString()}
      </td>
    </tr>
  );
}
