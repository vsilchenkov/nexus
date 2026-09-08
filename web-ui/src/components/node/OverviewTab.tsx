import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { api, type Node } from "../../api/client";
import { Card, DefaultPeriodButton, Kpi, KpiRow, LabelHint, Pill, PeriodPicker, TrafficChart, type LogsRange, type Period } from "../ui";
import { nodeLookbackMs } from "../../lib/nodeLookback";
import { useNodeTabTo } from "../../lib/nodeTabLink";
import { fmtLogTs, fmtNum } from "../../lib/format";
import { isLogOK } from "../../lib/logsQuery";
import { useNodeTeamName } from "../../lib/nodeTeamName";
import { PREF_KEY_NODE_PERIOD, useTeamDefaultPeriod } from "../../lib/prefs";
import { useNodeMetrics, METRICS_REFETCH_MS } from "./useNodeMetrics";
import { LogUrlCell } from "./LogUrlCell";
import { type LogsResp } from "./types";

// OverviewTab — вкладка «Обзор» узла (§21): 4 KPI + график трафика + последние
// запросы. KPI/график — из ClickHouse через /api/metrics; таблица — из логов.
// onOpenLogs (§33.4) — клик по столбцу графика открывает логи за момент.
export function OverviewTab({
  node,
  onOpenLogs,
}: {
  node: Node;
  onOpenLogs?: (range: LogsRange) => void;
}) {
  // Адрес перехода строит сам компонент — ссылке он нужен на рендере.
  const to = useNodeTabTo();
  const { t } = useTranslation();
  // §92: стартовый период — дефолт команды (преф команды → глобальный → 24ч),
  // а не системные 24ч. Выбор пользователя живёт поверх префа отдельным
  // состоянием: писать выбор в преф на каждый клик нельзя — дефолт меняется
  // только кнопкой «По умолчанию».
  const { value: teamDefault, settled: periodReady } = useTeamDefaultPeriod(
    node.team_id,
    PREF_KEY_NODE_PERIOD,
  );
  // §92.3: имя команды нужно только подсказке кнопки «По умолчанию». Запросы
  // общие со страницей узла — react-query их дедуплицирует.
  const teamName = useNodeTeamName(node);
  const [picked, setPicked] = useState<Period | null>(null);
  const period = picked ?? teamDefault;
  // enabled=periodReady: до прихода префа период неизвестен, и запрос ушёл бы
  // за чужим окном — лишний поход в ClickHouse и мигание графика.
  const m = useNodeMetrics(node.id, period, "auto", {}, periodReady);
  const hasLogsTable = !!node.clickhouse_table;

  const recentQ = useQuery({
    queryKey: ["logs", node.id, { limit: 8 }],
    queryFn: () => api.get<LogsResp>(`/api/nodes/${node.id}/logs`, { limit: 8 }),
    enabled: hasLogsTable,
    refetchInterval: METRICS_REFETCH_MS,
  });

  const kpi = m.data?.kpi;
  const deliveredPct =
    kpi && kpi.total > 0 ? ((kpi.delivered / kpi.total) * 100).toFixed(1) + "%" : undefined;

  return (
    <div className="space-y-4">
      {node.comment && (
        <Card>
          <div className="mb-1 text-[11px] uppercase tracking-wide text-fg-subtle">
            {t("node.form.comment")}
          </div>
          <p className="whitespace-pre-wrap text-[13px] text-fg-muted">{node.comment}</p>
        </Card>
      )}
      <KpiRow>
        <Kpi label={t("metrics.kpi.in")} value={kpi ? fmtNum(kpi.total) : "—"} hint={t("metrics.hints.in")} />
        <Kpi
          label={t("metrics.kpi.delivered")}
          value={kpi ? fmtNum(kpi.delivered) : "—"}
          delta={deliveredPct}
          deltaTone="up"
          hint={t("metrics.hints.delivered")}
        />
        <Kpi
          label={t("metrics.kpi.p95")}
          value={kpi ? <>{Math.round(kpi.p95_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
          delta={kpi ? `p99 ${Math.round(kpi.p99_ms)} ms` : undefined}
          hint={t("metrics.hints.p95")}
        />
        <Kpi
          label={t("metrics.kpi.errors")}
          value={kpi ? fmtNum(kpi.errors) : "—"}
          deltaTone={kpi && kpi.errors > 0 ? "down" : "muted"}
          hint={t("metrics.hints.errors")}
        />
      </KpiRow>

      <Card>
        <div className="mb-3 flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-sm font-semibold">
            {t("metrics.traffic")}
            <LabelHint content={t("metrics.hints.traffic")} />
          </span>
          {periodReady ? (
            <div className="flex flex-wrap items-center gap-2">
              <PeriodPicker value={period} onChange={setPicked} maxLookbackMs={nodeLookbackMs(node)} />
              <DefaultPeriodButton
                period={period}
                savedDefault={teamDefault}
                teamId={node.team_id}
                prefKey={PREF_KEY_NODE_PERIOD}
                // §92.3: подсказка обязана называть скоуп — преф общий для
                // «Обзора» и «Очереди» и действует на все узлы команды, а не
                // на этот один. Пока имя команды не подгрузилось, показываем
                // подпись кнопки (дефолт компонента), а не «команда undefined».
                title={
                  teamName
                    ? t("node.set_default_period_hint", { team: teamName })
                    : undefined
                }
              />
            </div>
          ) : (
            /* §71/§92: плейсхолдер той же высоты вместо переключателя — показать
               24ч и переключить на настоящий дефолт значило бы мигание. */
            <div className="h-[30px] w-[320px] animate-pulse rounded-md bg-line/40" aria-hidden />
          )}
        </div>
        <TrafficChart data={m.data?.series ?? []} onOpenLogs={hasLogsTable ? onOpenLogs : undefined} />
      </Card>

      <Card className="p-0">
        <div className="flex items-center justify-between px-4 py-3">
          <span className="text-sm font-semibold">{t("metrics.recent")}</span>
          {/* §7.4 называет это переходом, и адрес у него есть (?tab=logs).
              Значит ссылка, а не кнопка: иначе Ctrl+клик и «Открыть в новой
              вкладке» не работают (урок §79.3). */}
          <Link to={to.tab("logs")} className="text-xs text-fg-muted hover:text-accent">
            {t("metrics.all_logs")}
          </Link>
        </div>
        {/* table-fixed + colgroup обязательны: при авто-раскладке браузер
            ИГНОРИРУЕТ max-width у ячейки и растягивает колонку под содержимое —
            длинный подпуть (§39) в «Методе» распирал таблицу за край карточки,
            а truncate не срабатывал вовсе. Ширины подобраны как в журнале
            логов: фиксированные служебные колонки, а «Метод» и URL делят
            остаток. */}
        <table className="w-full table-fixed text-[12.5px]">
          <colgroup>
            <col className="w-[150px]" />
            <col className="w-[76px]" />
            <col className="w-[80px]" />
            <col />
            <col />
          </colgroup>
          <thead>
            <tr className="border-y border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
              <th className="px-4 py-2 font-medium">{t("logs.col.time")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.status")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.ms")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.method")}</th>
              <th className="px-4 py-2 font-medium">{t("logs.col.url")}</th>
            </tr>
          </thead>
          <tbody>
            {(recentQ.data?.items ?? []).map((r) => {
              // §72.1: тот же предикат, что у фильтра «Ошибки» и подсветки в
              // журнале логов — «красная» строка везде означает одно и то же.
              const isErr = !isLogOK(r);
              return (
                <tr key={r.id} className={`border-b border-line last:border-0 ${isErr ? "bg-err/5" : ""}`}>
                  <td className="whitespace-nowrap px-4 py-2 font-mono text-xs">
                    {fmtLogTs(r.date_request)}
                  </td>
                  <td className="px-4 py-2">
                    <Pill tone={isErr ? "err" : "ok"}>{r.status}</Pill>
                  </td>
                  <td className="px-4 py-2 font-mono">{r.duration_ms} ms</td>
                  {/* Метод — это подпуть §39, он бывает длиннее URL; обрезаем
                      с полным значением в подсказке, как в журнале логов. */}
                  <td className="truncate px-4 py-2 font-mono" title={r.method}>
                    {r.method}
                  </td>
                  {/* Тот же компонент, что в журнале логов: обрезка + всплывающий
                      блок с полным адресом и кнопкой «Скопировать». */}
                  <td className="truncate px-4 py-2 font-mono text-fg-muted">
                    <LogUrlCell url={r.url} />
                  </td>
                </tr>
              );
            })}
            {hasLogsTable && (recentQ.data?.items?.length ?? 0) === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-6 text-center text-fg-muted">
                  {recentQ.isLoading
                    ? t("common.loading")
                    : recentQ.data?.logs_available === false
                      ? t("logs.unavailable")
                      : t("logs.empty")}
                </td>
              </tr>
            )}
            {!hasLogsTable && (
              <tr>
                <td colSpan={5} className="px-4 py-6 text-center text-fg-muted">
                  {t("logs.not_configured")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
