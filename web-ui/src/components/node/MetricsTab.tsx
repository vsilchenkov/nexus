import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { type Node } from "../../api/client";
import {
  Card,
  Hint,
  Kpi,
  KpiRow,
  LabelHint,
  PeriodPicker,
  Seg,
  TrafficChart,
  type LogsRange,
  type Period,
} from "../ui";
import { fmtNum } from "../../lib/format";
import { CHART_STEPS, type ChartStep } from "../../lib/period";
import {
  advFormEqual,
  emptyAdvForm,
  logsFilterParams,
  type LogsAdvForm,
  type LogsStatusFilter,
} from "../../lib/logsQuery";
import { useNodeMetrics } from "./useNodeMetrics";
import { LogsAdvancedFilters } from "./LogsAdvancedFilters";

// MetricsTab — вкладка «Метрики» узла (§21): перцентили + счётчики + график
// за выбранный период. Источник — ClickHouse (точные quantile). По умолчанию 24h.
// onOpenLogs (§33.4) — клик по столбцу графика открывает логи за момент.
//
// §79.4: те же расширенные фильтры, что в журнале логов — KPI и график считаются
// под ними. Полей дат в панели нет: окно задаёт выбор периода, два контрола,
// пишущих в одни границы, затирали бы друг друга. Сегмента «Завершено/В работе»
// тоже нет — §77.4 убрал его из журнала как доказанный дубль «ОК/Ошибок».
// §79.5: «Шаг графика» — ширина одного столбца (период 14д + шаг 24ч = 14
// столбцов по суткам).
export function MetricsTab({
  node,
  onOpenLogs,
}: {
  node: Node;
  onOpenLogs?: (range: LogsRange) => void;
}) {
  const { t } = useTranslation();
  const [period, setPeriod] = useState<Period>({ kind: "preset", range: "24h" });
  const [step, setStep] = useState<ChartStep>("auto");
  const [showFilters, setShowFilters] = useState(false);
  const [advForm, setAdvForm] = useState<LogsAdvForm>(emptyAdvForm);
  const [applied, setApplied] = useState<LogsAdvForm>(emptyAdvForm);
  const [status, setStatus] = useState<LogsStatusFilter>("all");

  // Коммит идемпотентен — как в журнале (§77.3): Enter + последующий blur не
  // должны порождать два одинаковых запроса метрик.
  const commit = (next: LogsAdvForm) => {
    setAdvForm(next);
    setApplied((prev) => (advFormEqual(prev, next) ? prev : next));
  };

  // Даты в параметры не идут: окно метрик — это период (§79.4).
  // done тоже не выводится в UI: §77.4 доказал, что «Завершено/В работе» —
  // дубль «ОК/Ошибок» (на боевых узлах err ≡ done=no), и сегмент убрали из
  // журнала; повторять его здесь незачем. Параметр остаётся выключенным.
  const filterParams = useMemo(
    () => logsFilterParams({ ...applied, from: "", to: "", status, done: "all" }),
    [applied, status],
  );

  const m = useNodeMetrics(node.id, period, step, filterParams);
  const kpi = m.data?.kpi;
  const chartUnavailable = m.data && !m.data.chart_available;
  const byAttempts = m.data?.chart_unit === "attempts";
  const filtersActive =
    !advFormEqual(applied, emptyAdvForm) || status !== "all";

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-semibold">{t("node.tabs.metrics")}</span>
        <div className="flex flex-wrap items-center gap-2">
          <PeriodPicker value={period} onChange={setPeriod} />
          {/* Группа с aria-label: подписи шага совпадают с подписями периода
              («24ч» и там, и там), и без имени группы их не различить ни
              программе чтения с экрана, ни тесту. */}
          <div
            role="group"
            aria-label={t("metrics.step.label")}
            className="flex items-center gap-1.5"
          >
            {/* Без uppercase: рядом стоит выбор периода с обычными подписями,
                и капс тут читался как отдельный «заголовок секции». */}
            <span className="text-xs text-fg-muted">{t("metrics.step.label")}</span>
            <Seg<ChartStep>
              value={step}
              onChange={setStep}
              options={CHART_STEPS.map((s) => ({
                value: s,
                label: s === "auto" ? t("metrics.step.auto") : t(`metrics.range.${s}`),
              }))}
            />
          </div>
          <button
            type="button"
            onClick={() => setShowFilters((v) => !v)}
            className={`rounded-md px-2.5 py-1.5 text-xs transition-colors ${
              showFilters || filtersActive
                ? "bg-accent/15 text-accent"
                : "bg-bg-muted text-fg-muted hover:text-fg"
            }`}
          >
            {showFilters ? t("logs.advanced.toggle_off") : t("logs.advanced.toggle_on")}
          </button>
        </div>
      </div>

      {showFilters && (
        <div className="overflow-hidden rounded-md border border-line">
          <LogsAdvancedFilters
            nodeId={node.id}
            draft={advForm}
            applied={applied}
            onDraft={setAdvForm}
            onCommit={commit}
            showDates={false}
          />
          <div className="flex flex-wrap items-center gap-4 px-4 py-2">
            <Seg<LogsStatusFilter>
              value={status}
              onChange={setStatus}
              options={[
                { value: "all", label: t("logs.filter.all") },
                { value: "ok", label: t("logs.filter.ok") },
                { value: "err", label: t("logs.filter.err") },
              ]}
            />
          </div>
        </div>
      )}

      {chartUnavailable && <Hint tone="muted">{t("logs.not_configured")}</Hint>}
      {/* §79.4: авто-обновление выключено, пока набор фильтров отвечает дольше
          порога — иначе запросы накладываются друг на друга. */}
      {m.slow && <Hint tone="warn">{t("metrics.filters.autorefresh_paused")}</Hint>}

      <KpiRow>
        <Kpi label={t("metrics.kpi.total")} value={kpi ? fmtNum(kpi.total) : "—"} hint={t("metrics.hints.total")} />
        <Kpi label={t("metrics.kpi.delivered")} value={kpi ? fmtNum(kpi.delivered) : "—"} hint={t("metrics.hints.delivered")} />
        <Kpi
          label={t("metrics.kpi.p95")}
          value={kpi ? <>{Math.round(kpi.p95_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
          hint={t("metrics.hints.p95")}
        />
        <Kpi
          label={t("metrics.kpi.p99")}
          value={kpi ? <>{Math.round(kpi.p99_ms)}<span className="text-sm text-fg-muted"> ms</span></> : "—"}
          hint={t("metrics.hints.p99")}
        />
      </KpiRow>

      <Card>
        <div className="mb-3 flex items-center gap-1.5 text-sm font-semibold">
          {t("metrics.kpi.requests")}
          <LabelHint
            content={
              <div className="max-w-xs space-y-1 text-left">
                <div>{t("metrics.hints.requests")}</div>
                <div>{t("metrics.hints.bucket_unit")}</div>
              </div>
            }
          />
        </div>
        {/* §79.5: столбцы посчитаны по прогонам, а не по итогу записи — окно
            слишком велико для точной формы. Молчать об этом нельзя. */}
        {byAttempts && (
          <div className="mb-2 text-xs text-warn">{t("metrics.hints.attempts_mode")}</div>
        )}
        <TrafficChart
          data={m.data?.series ?? []}
          height={180}
          bucketMs={m.data?.step_seconds ? m.data.step_seconds * 1000 : undefined}
          onOpenLogs={node.clickhouse_table ? onOpenLogs : undefined}
        />
      </Card>
    </div>
  );
}
