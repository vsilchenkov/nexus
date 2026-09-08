import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { api, type Node } from "../../api/client";
import {
  Card,
  Hint,
  LatencyChart,
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
import {
  defaultPeriod,
  defaultStepFor,
  isStepTooFine,
  normalizeStepForPeriod,
  stepsForPeriod,
  type ChartStep,
} from "../../lib/period";
import { parseMetricsView, withMetricsView } from "../../lib/nodeTabUrl";
import { nodeLookbackMs } from "../../lib/nodeLookback";
import { prefKeyNodeMetricsView, useNodeMetricsViewPref, useSetPref } from "../../lib/prefs";
import {
  advFormEqual,
  emptyAdvForm,
  logsFilterParams,
  type LogsAdvForm,
  type LogsStatusFilter,
} from "../../lib/logsQuery";
import { useNodeMetrics } from "./useNodeMetrics";
import { LogsAdvancedFilters } from "./LogsAdvancedFilters";
import { NodePulse } from "./NodePulse";
import { CapacityCard } from "./CapacityCard";

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
  // §84.8: переход на вкладку «Очередь» — ёмкость отвечает «помещается ли
  // узел», а «сколько ждёт прямо сейчас» живёт там.
}) {
  const { t } = useTranslation();

  // §84.2: период и шаг живут в АДРЕСЕ, а не в useState — иначе ссылку на
  // конкретный масштаб не передать, а «Назад» после смены периода уводит не
  // туда. Недостающее в адресе добирается дефолтом (§84.3: сначала преф узла).
  const [searchParams, setSearchParams] = useSearchParams();
  const urlView = useMemo(() => parseMetricsView(searchParams), [searchParams]);

  // §84.3, цепочка приоритетов: АДРЕС → преф ЭТОГО узла → системный дефолт.
  // Адрес выигрывает всегда, в том числе при первой загрузке: прямая ссылка
  // обязана открывать ровно то, что в ней написано (правило §71).
  const pref = useNodeMetricsViewPref(node.id);
  // Гейт готовности: пока преф не приехал, вид неизвестен, и запрос метрик не
  // уходит. Показать 24 ч и через мгновение переключиться на сохранённые 7 д —
  // это и мигание, и лишний запрос (урок §71).
  const viewReady = pref.settled;

  const period = urlView.period ?? pref.value.period ?? defaultPeriod;

  // explicitStep — шаг, ВЫБРАННЫЙ пользователем (адрес или преф). undefined
  // означает «шаг неявный, берётся дефолтом периода», и различать эти два
  // состояния обязательно: иначе дефолт одного периода при переключении на
  // другой переезжает туда уже как выбор и закрепляется в адресе.
  const explicitStep = urlView.step ?? pref.value.step ?? undefined;
  // Шаг нормализуется: ссылка «range=30d&step=1m» приходит извне, а
  // сохранённый шаг мог остаться от другого периода.
  const step = normalizeStepForPeriod(explicitStep ?? defaultStepFor(period), period);
  const stepOptions = useMemo(() => stepsForPeriod(period), [period]);

  const setPref = useSetPref();

  const applyView = useCallback(
    (p: Period, s: ChartStep | undefined) => {
      // Шаг согласуется с НОВЫМ периодом здесь, а не в рендере: в адрес обязано
      // попасть то же значение, которое подсвечено сегментом.
      const norm = s === undefined ? undefined : normalizeStepForPeriod(s, p);
      // Совпал с дефолтом периода — не пишем: ссылка на дефолтный вид обязана
      // быть короткой, а поведение от этого не меняется (шаг снова становится
      // неявным и следует за периодом).
      const write = norm === undefined || norm === defaultStepFor(p) ? undefined : norm;
      setSearchParams((prev) => withMetricsView(prev, p, write, { period: defaultPeriod }), {
        replace: true,
      });

      // Преф пишется ТОЛЬКО отсюда — из действия пользователя, и никогда из
      // эффекта синхронизации «адрес → состояние». Иначе кнопка «Назад»
      // переписывала бы личный дефолт узла.
      //
      // Произвольный период в преф не сохраняется (календарный диапазон в роли
      // дефолта бессмыслен), но и не стирает ранее сохранённый пресет: человек
      // посмотрел конкретные сутки и вернулся — его дефолт должен уцелеть.
      const keepRange = p.kind === "preset" ? p.range : pref.value.period?.range;
      setPref.mutate({
        teamId: node.team_id,
        key: prefKeyNodeMetricsView(node.id),
        value: {
          ...(keepRange ? { range: keepRange } : {}),
          ...(write ? { step: write } : {}),
        },
      });
    },
    [setSearchParams, setPref, node.id, node.team_id, pref.value.period],
  );

  const setPeriod = useCallback(
    (p: Period) => applyView(p, explicitStep),
    [applyView, explicitStep],
  );
  const setStep = useCallback((s: ChartStep) => applyView(period, s), [applyView, period]);

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

  const m = useNodeMetrics(node.id, period, step, filterParams, viewReady);

  // §84.6: «за всё время» — СТРОГО по клику и без поллинга. Полный
  // max(date_request) без окна читает колонку на всей таблице (боевая внешняя
  // §64 — 10,2 млн записей), а вкладка обновляется каждые ~12 с.
  const [allTimeMs, setAllTimeMs] = useState<number | null>(null);
  const loadAllTime = useCallback(async () => {
    try {
      const r = await api.get<{ max_ms: number }>(`/api/nodes/${node.id}/logs/date-range`);
      setAllTimeMs(r.max_ms || 0);
    } catch {
      // Второстепенное действие: молча остаёмся с оконным значением, вкладка
      // из-за него краснеть не должна.
      setAllTimeMs(0);
    }
  }, [node.id]);
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
          <PeriodPicker value={period} onChange={setPeriod} maxLookbackMs={nodeLookbackMs(node)} />
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
            {/* §84.1: набор кнопок ПОСТОЯНЕН, неприменимые гасятся. Пока
                список менялся вместе с периодом, менялась ширина строки, и вся
                шапка прыгала при каждом переключении. Гасится только то, что
                солгало бы: шаг мельче окна/400 сервер поднял бы до потолка. */}
            <Seg<ChartStep>
              value={step}
              onChange={setStep}
              options={stepOptions.map((s) => ({
                value: s,
                label: s === "auto" ? t("metrics.step.auto") : t(`metrics.step.opt.${s}`),
                disabled: isStepTooFine(s, period),
                title: isStepTooFine(s, period) ? t("metrics.step.too_fine") : undefined,
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

      {/* §84.6: пульс стоит НАД счётчиками — «узел молчит третий час» важнее
          любой цифры под ним, и заметить это надо раньше, чем начать читать. */}
      <NodePulse
        lastSeenMs={kpi?.last_seen_ms ?? 0}
        total={kpi?.total ?? 0}
        stepSeconds={m.data?.step_seconds ?? 0}
        available={!!m.data?.chart_available}
        allTimeMs={allTimeMs}
        onShowAllTime={loadAllTime}
      />
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

      {/* §84.5: латентность во времени — под графиком трафика и в тех же
          столбцах. Два числа в KPI (p95/p99) не отвечают на вопрос «когда было
          плохо»: на боевом узле они описывали получасовой пик, а выглядели как
          характеристика суток. */}
      {/* §84.8: узловой срез §80.2 — ни одного нового запроса, всё считается из
          уже полученных метрик. */}
      <CapacityCard
        node={node}
        total={kpi?.total ?? 0}
        p95Ms={kpi?.p95_ms ?? 0}
        rangeMs={m.data?.range_ms ?? 0}
      />

      {m.data?.latency_available && (
        <Card>
          <div className="mb-3 flex items-center gap-1.5 text-sm font-semibold">
            {t("metrics.latency.title")}
            <LabelHint
              content={
                <div className="max-w-xs space-y-1 text-left">
                  <div>{t("metrics.latency.hint")}</div>
                  <div>{t("metrics.latency.gap_hint")}</div>
                </div>
              }
            />
          </div>
          <LatencyChart data={m.data.latency ?? []} height={140} />
        </Card>
      )}
    </div>
  );
}
