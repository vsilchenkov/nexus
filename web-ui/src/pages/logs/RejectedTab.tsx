import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { CheckCheck, Download } from "lucide-react";

import { api } from "../../api/client";
import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { useConfirm } from "../../lib/confirm";
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
import { REJECTED_PARAM } from "../../lib/rejectedShare";
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
  const confirm = useConfirm();

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
  // §98.3: открытая карточка живёт в АДРЕСЕ, а не в useState — иначе ею нельзя
  // поделиться (тот же урок, что §79.3 для вкладок узла). Карточка грузится по
  // id независимо от выдачи списка, поэтому ссылка работает и когда группы нет
  // в текущем периоде или под текущим фильтром.
  const [searchParams, setSearchParams] = useSearchParams();
  const openID = searchParams.get(REJECTED_PARAM);
  const setOpenID = useCallback(
    (id: string | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) next.set(REJECTED_PARAM, id);
          else next.delete(REJECTED_PARAM);
          return next;
        },
        // replace: переключение между карточками — это просмотр, а не переходы:
        // иначе «Назад» пришлось бы жать столько раз, сколько строк открыли.
        { replace: true },
      );
    },
    [setSearchParams],
  );

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

  // Сводка ВСЕЙ области видимости — без периода и прочих экранных фильтров.
  // Нужна ровно одному потребителю: доступности кнопки «пометить все». Кнопка
  // действует по всей области видимости (§94.6), поэтому гасить её по сводке
  // экрана нельзя — при узком периоде она гасла, хотя непросмотренные были и
  // бейдж горел, и обнулить счётчик становилось нечем.
  const scopeSummaryQ = useQuery({
    queryKey: ["rejected", "summary", "scope"],
    queryFn: () => api.get<RejectedSummary>("/api/rejected/summary"),
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

  // markViewedLocally — строка, помеченную открытием карточки, НЕ выдёргиваем
  // из выдачи: она гаснет на месте и уходит при следующем обновлении списка.
  // Инвалидация здесь заставила бы таблицу перестроиться прямо под курсором, и
  // соседние строки прыгали бы при каждом клике (§94.7).
  const markViewedLocally = useCallback(
    (id: string, at: string) => {
      qc.setQueriesData<RejectedListResp>({ queryKey: ["rejected", "list"] }, (prev) =>
        prev
          ? {
              ...prev,
              groups: prev.groups.map((g) => (g.id === id ? { ...g, resolved_at: at } : g)),
            }
          : prev,
      );
      // Счётчики и бейдж, наоборот, обязаны обновиться сразу — ради них отметка
      // и ставится.
      void qc.invalidateQueries({ queryKey: ["rejected", "summary"] });
    },
    [qc],
  );

  const resolveAll = useMutation({
    mutationFn: () => api.post<{ marked: number }>("/api/rejected/resolve-all", {}),
    onSuccess: invalidate,
  });

  const onResolveAll = async () => {
    // Подтверждение обязательно: одно нажатие гасит счётчик по ВСЕЙ области
    // видимости, включая группы, которых сейчас не видно из-за фильтров.
    const ok = await confirm({
      title: t("rejected.resolve_all.title"),
      message: t("rejected.resolve_all.confirm"),
      confirmLabel: t("rejected.resolve_all.title"),
    });
    if (ok) resolveAll.mutate();
  };

  return (
    // При открытой панели контент ужимается вправо, а не уезжает под неё.
    // Найдено на стенде: панель шириной 36rem накрывала правую половину
    // таблицы, и клик по строке в перекрытой области не доходил вовсе — то
    // есть немодальность была только на вид. Отступ только с xl: на узких
    // экранах панель всё равно занимает всю ширину.
    <div className={cn("space-y-3", openID && "xl:pr-[37rem]")}>
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
        {/* Кнопка активна, только пока есть что помечать: с нулевым счётчиком
            нажатие ничего не изменило бы, а выглядело бы как действие. Счёт —
            по области видимости (scopeSummaryQ), а не по экрану: именно её
            меняет операция. */}
        <Button
          sm
          onClick={() => void onResolveAll()}
          disabled={resolveAll.isPending || !(scopeSummaryQ.data?.unresolved ?? 0)}
        >
          <CheckCheck className="h-3.5 w-3.5" /> {t("rejected.resolve_all.title")}
        </Button>
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
                  active={g.id === openID}
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
        <RejectedDrawer
          id={openID}
          onClose={() => setOpenID(null)}
          onViewed={markViewedLocally}
          onDeleted={invalidate}
        />
      )}
    </div>
  );
}

function RejectedRow({
  group,
  showTeam,
  active,
  onOpen,
}: {
  group: RejectedGroup;
  showTeam: boolean;
  // active — карточка этой группы открыта в панели справа. Панель не модальная,
  // и без подсветки было бы не видно, к какой строке она относится.
  active: boolean;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  const label = reasonLabelKey(group.reason);
  return (
    <tr
      onClick={onOpen}
      className={cn(
        "cursor-pointer border-b border-line/60 last:border-0 hover:bg-bg-muted",
        // Просмотренная гаснет, но остаётся на месте до обновления списка.
        group.resolved_at && "opacity-60",
        active && "bg-bg-muted",
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
