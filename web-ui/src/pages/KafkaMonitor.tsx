import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plug, RefreshCw } from "lucide-react";

import {
  api,
  type KafkaByNode,
  type KafkaOverview,
  type KafkaTestResp,
  type KafkaTimeseries,
  type KafkaTopicsResp,
} from "../api/client";
import { Button, Card, DefaultPeriodButton, PeriodPicker, Seg, periodKey, periodParams, periodWindow } from "../components/ui";
import { PREF_KEY_KAFKA_PERIOD, useTeamDefaultPeriod } from "../lib/prefs";
import type { Period } from "../components/ui";
import { HealthBanner } from "../components/kafka/HealthBanner";
import { LagChart, MiniSpark, ThroughputChart } from "../components/kafka/charts";
import { TopicsTable } from "../components/kafka/TopicsTable";
import { TopNodes } from "../components/kafka/TopNodes";
import { Brokers } from "../components/kafka/Brokers";
import { fmtNum } from "../lib/format";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { useStableData } from "../lib/useStableData";

const REFRESH_MS = 10_000; // §7 spec: автообновление раз в 10с.
type Resolution = "auto" | "10s" | "1m" | "5m";

export default function KafkaMonitor() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const isAdmin = useRoleAtLeast("admin");
  // §92: дефолтный период монитора — ГЛОБАЛЬНЫЙ преф (пустой teamId): брокер
  // один на инстанс и ни одной команде не принадлежит, поэтому командного
  // значения здесь быть не может. Выбор пользователя живёт поверх префа.
  const { value: savedDefault, settled: periodReady } = useTeamDefaultPeriod(
    "",
    PREF_KEY_KAFKA_PERIOD,
  );
  const [picked, setPicked] = useState<Period | null>(null);
  const period = picked ?? savedDefault;
  const [resolution, setResolution] = useState<Resolution>("auto");
  const [now, setNow] = useState(() => Date.now());

  const pk = periodKey(period);
  const pp = periodParams(period);
  const win = periodWindow(period);
  const longRange = win.until - win.since > 2 * 86_400_000;

  const overviewQ = useQuery({
    queryKey: ["kafka", "overview", pk],
    queryFn: () => api.get<KafkaOverview>("/api/kafka/overview", pp),
    refetchInterval: REFRESH_MS,
    enabled: isAdmin,
  });
  const seriesQ = useQuery({
    queryKey: ["kafka", "timeseries", pk, resolution],
    queryFn: () =>
      api.get<KafkaTimeseries>("/api/kafka/timeseries", {
        ...pp,
        step: resolution,
        metrics: "produced,consumed,errors,lag",
      }),
    refetchInterval: REFRESH_MS,
    enabled: isAdmin,
  });
  const topicsQ = useQuery({
    queryKey: ["kafka", "topics"],
    queryFn: () => api.get<KafkaTopicsResp>("/api/kafka/topics"),
    refetchInterval: REFRESH_MS,
    enabled: isAdmin,
  });
  const byNodeQ = useQuery({
    queryKey: ["kafka", "by-node", pk],
    queryFn: () => api.get<KafkaByNode>("/api/kafka/by-node", pp),
    refetchInterval: REFRESH_MS,
    enabled: isAdmin,
  });
  const testQ = useQuery({
    queryKey: ["kafka", "test"],
    queryFn: () => api.post<KafkaTestResp>("/api/kafka/test"),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    enabled: isAdmin,
  });

  // Анти-мерцание: держим последний ответ с prometheus_available=true (§ useStableData).
  // ВАЖНО: хуки — до раннего return ниже (rules-of-hooks).
  const ov = useStableData(overviewQ.data, pk, (d) => d.prometheus_available);
  const series = useStableData(seriesQ.data, `${pk}:${resolution}`, (d) => d.prometheus_available);
  const byNode = useStableData(byNodeQ.data, pk, (d) => d.prometheus_available);

  // Тикаем раз в секунду для индикатора «обновлено N секунд назад».
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  if (!isAdmin) {
    return <div className="mx-auto max-w-6xl py-10 text-center text-fg-muted">{t("kafka.forbidden")}</div>;
  }

  const secsAgo = overviewQ.dataUpdatedAt ? Math.max(0, Math.floor((now - overviewQ.dataUpdatedAt) / 1000)) : 0;

  function refreshAll() {
    qc.invalidateQueries({ queryKey: ["kafka"] });
  }

  return (
    <div className="mx-auto max-w-7xl space-y-3.5">
      {/* Шапка (§5.1) */}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">{t("kafka.title")}</h1>
          <p className="text-[13px] text-fg-muted">{t("kafka.subtitle")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {periodReady ? (
            <>
              <PeriodPicker value={period} onChange={setPicked} />
              <DefaultPeriodButton
                period={period}
                savedDefault={savedDefault}
                teamId=""
                prefKey={PREF_KEY_KAFKA_PERIOD}
                title={t("kafka.set_default_period_hint")}
              />
            </>
          ) : (
            <div className="h-[30px] w-[320px] animate-pulse rounded-md bg-line/40" aria-hidden />
          )}
          <span className="text-[11px] text-fg-subtle">{t("kafka.updated_ago", { secs: secsAgo })}</span>
          <Button sm onClick={refreshAll}>
            <RefreshCw className="h-3.5 w-3.5" /> {t("kafka.refresh")}
          </Button>
          <Button sm onClick={() => testQ.refetch()} disabled={testQ.isFetching}>
            <Plug className="h-3.5 w-3.5" /> {t("kafka.check_cluster")}
          </Button>
        </div>
      </div>

      {ov && <HealthBanner ov={ov} />}

      {/* KPI (§5.3) */}
      {ov && <KpiCards ov={ov} lagSpark={(series?.series.lag ?? []).map((p) => p.v)} t={t} />}

      {/* Главный график throughput (§5.4) */}
      <Card className="p-4">
        <div className="mb-3 flex items-center justify-between">
          <div className="text-[13.5px] font-semibold">
            {t("kafka.throughput.title")}
            <span className="ml-2 text-[11.5px] font-normal text-fg-subtle">{t("kafka.throughput.unit")}</span>
          </div>
          <Seg<Resolution>
            value={resolution}
            onChange={setResolution}
            options={[
              { value: "auto", label: t("kafka.resolution.auto") },
              { value: "10s", label: "10s" },
              { value: "1m", label: "1m" },
              { value: "5m", label: "5m" },
            ]}
          />
        </div>
        <ThroughputChart
          series={series?.series ?? {}}
          longRange={longRange}
          stepSeconds={series?.step_seconds}
          labels={{
            produced: t("kafka.legend.produced"),
            consumed: t("kafka.legend.consumed"),
            errors: t("kafka.legend.errors"),
          }}
        />
      </Card>

      {/* Lag-график (§5.5) */}
      <Card className="p-4">
        <div className="mb-3 text-[13.5px] font-semibold">
          {t("kafka.lag.title")}
          <span className="ml-2 text-[11.5px] font-normal text-fg-subtle">{t("kafka.lag.subtitle")}</span>
        </div>
        <LagChart points={series?.series.lag ?? []} threshold={1000} longRange={longRange} />
      </Card>

      {/* Топики (§5.6) */}
      <TopicsTable
        topics={topicsQ.data?.topics ?? []}
        sizesAvailable={topicsQ.data?.sizes_available ?? false}
      />

      {/* Топ-узлы (§5.7) */}
      {byNode && <TopNodes data={byNode} from={win.since} to={win.until} />}

      {/* Брокеры (§5.8) */}
      <Brokers data={testQ.data} onRecheck={() => testQ.refetch()} rechecking={testQ.isFetching} />
    </div>
  );
}

// KpiCards — 4 KPI экрана (§5.3). Локальный компонент: KPI требуют нескольких
// строк деталей (больше, чем атом Kpi).
function KpiCards({
  ov,
  lagSpark,
  t,
}: {
  ov: KafkaOverview;
  lagSpark: number[];
  t: (k: string, o?: Record<string, unknown>) => string;
}) {
  const s = ov.summary;
  const lagTone = s.current_lag > 1000 ? "err" : s.current_lag > 100 ? "warn" : "ok";
  const errTone = ov.error_rate > 0.01 ? "err" : ov.error_rate > 0.001 ? "warn" : "ok";
  const deltaPct = (ov.delta_vs_previous_period.produced * 100).toFixed(1);
  const deltaUp = ov.delta_vs_previous_period.produced >= 0;

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <KpiCard label={t("kafka.kpi.messages")} value={fmtNum(s.produced_total + s.consumed_total)}>
        <div className="text-[11.5px] text-fg-muted">
          ↑ {fmtNum(s.produced_total)} · ↓ {fmtNum(s.consumed_total)}
        </div>
        {ov.delta_vs_previous_period.has_delta && (
          <div className={`mt-1 text-[11px] ${deltaUp ? "text-ok" : "text-err"}`}>
            {deltaUp ? "+" : ""}
            {deltaPct}% {t("kafka.kpi.vs_previous")}
          </div>
        )}
      </KpiCard>

      <KpiCard label={t("kafka.kpi.lag")} value={s.current_lag.toLocaleString()} tone={lagTone}>
        <div className="-mx-1 mt-1">
          <MiniSpark values={lagSpark} color={lagTone === "err" ? "#e85d5c" : lagTone === "warn" ? "#d9a441" : "#5b9bf0"} unit={t("kafka.tooltip.lag_unit")} />
        </div>
      </KpiCard>

      <KpiCard label={t("kafka.kpi.errors")} value={(s.failed_produced + s.failed_consumed).toLocaleString()} tone={errTone}>
        <div className="text-[11.5px] text-fg-muted">
          {s.failed_produced} produce · {s.failed_consumed} consume
        </div>
        <div className="mt-1 text-[11px] text-fg-subtle">
          {(ov.error_rate * 100).toFixed(3)}% {t("kafka.kpi.of_total")}
        </div>
      </KpiCard>

      <KpiCard label={t("kafka.kpi.in_flight")} value={s.in_flight_now.toLocaleString()}>
        <div className="text-[11.5px] text-fg-muted">{t("kafka.kpi.in_flight_hint")}</div>
      </KpiCard>
    </div>
  );
}

function KpiCard({
  label,
  value,
  tone = "ok",
  children,
}: {
  label: string;
  value: string;
  tone?: "ok" | "warn" | "err";
  children?: React.ReactNode;
}) {
  const border = tone === "err" ? "border-err/40" : tone === "warn" ? "border-warn/40" : "border-line";
  const valCls = tone === "err" ? "text-err" : tone === "warn" ? "text-warn" : "text-fg";
  return (
    <div className={`flex min-h-[120px] flex-col rounded-md border bg-bg px-4 py-3 ${border}`}>
      <div className="mb-1.5 text-[11px] uppercase tracking-wide text-fg-muted">{label}</div>
      <div className={`text-[27px] font-bold leading-none tracking-tight ${valCls}`}>{value}</div>
      <div className="mt-auto pt-2">{children}</div>
    </div>
  );
}
