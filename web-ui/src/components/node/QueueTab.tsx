import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Trash2, ChevronRight, Pause, Power, Play, RotateCcw, History } from "lucide-react";

import { api, type Node } from "../../api/client";
import { Button, Kpi, KpiRow, Hint, Pill, PeriodPicker, periodWindow, periodKey, defaultPeriod, type Period } from "../ui";
import { nodeLookbackMs } from "../../lib/nodeLookback";
import { ReplayDialog } from "../ReplayDialog";
import { type LogsInitialFilter } from "./LogsTab";
import { type LogRow, type LogDetail } from "./types";
import { cn } from "../../lib/cn";
import { msToDatetimeLocal } from "../../lib/format";
import { MAX_INFINITE_ROWS, useInfiniteLogs } from "../../lib/useInfiniteLogs";
import { useConfirm } from "../../lib/confirm";
import { useRoleAtLeast } from "../../lib/useCurrentRole";
import { BreakerCard } from "./BreakerCard";
import { ReplayPeriodDialog } from "./ReplayPeriodDialog";

type QueueMessage = {
  id: string;
  partition: number;
  offset: number;
  // §3.6: сообщения узла на паузе лежат в отдельном delay-топике, поэтому
  // координата (partition, offset) осмысленна только вместе с топиком.
  topic?: string;
  method: string;
  target_url: string;
  received_at: string;
  body_size: number;
};
type ListResp = { items: QueueMessage[]; capped: boolean; kafka_available: boolean };
type BodyResp = { id: string; method: string; target_url: string; headers?: Record<string, string>; body: string };
type FailedCountResp = { count: number; logs_configured: boolean; logs_available?: boolean };

// FAILED_PAGE_SIZE — размер страницы списка неудачных доставок (§72.3).
// Переключателя размера, как в журнале логов, здесь нет: секция вспомогательная,
// а общее число неудач и так показывает KPI над ней.
const FAILED_PAGE_SIZE = 50;

function prettyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// QueueTab — вкладка «Очередь» узла (§35, §69.1). Две части:
//  • «Ожидают отправки» — живая очередь nexus.async (peek, только admin; непуста
//    лишь для paused-узлов/лежащего Sender) с удалением/очисткой через tombstones;
//  • «Неудачные доставки» — из ClickHouse-логов (done=0, быстро), с reason/телом/
//    replay. Очистка неудач не через Kafka (нельзя), а пауза/отключение узла +
//    replay; фильтр по периоду показывает свежие.
//
// §69.1: вкладка открыта для ВСЕХ типов узлов. У sync-узла (root_method=request)
// очереди в Kafka нет, поэтому первая секция скрыта (и peek не запрашивается —
// иначе на каждое открытие уходил бы бесполезный скан топиков), а вторая
// работает как и раньше: записи done=0 появляются и у sync (circuit breaker,
// сетевые ошибки, превышение лимита тела). До §69.1 очистить их из UI было
// нельзя вообще — вкладку приходилось «открывать» временной сменой типа узла.
export function QueueTab({
  node,
  onOpenFailedLogs,
  onOpenMetrics,
}: {
  node: Node;
  onOpenFailedLogs?: (f: LogsInitialFilter) => void;
  // §84.8: обратная ссылка на «Метрики». Ёмкость партиции считается там (из
  // метрик), «сколько ждёт прямо сейчас» — здесь; без взаимных ссылок разбор
  // упирается в тупик на любой из двух вкладок.
  onOpenMetrics?: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();
  const id = node.id;
  const isManager = useRoleAtLeast("manager");
  const hasLogsTable = !!node.clickhouse_table;
  // §69.1: очередь в Kafka есть у requestAsync и pull-узлов (RabbitMQAsync) —
  // они идут через один топик nexus.async. У sync-узла её нет.
  const isAsync = node.root_method !== "request";
  // Pull-узел на паузе исключается из поллинга RabbitMQ (NodeLister), поэтому
  // новые сообщения ждут в БРОКЕРЕ, а не в очереди Nexus: тексты про «копятся
  // в очереди ниже» и «очистите очередь» для него ввели бы в заблуждение —
  // очистка тронет только уже принятое шиной.
  const isPull = node.root_method === "RabbitMQAsync";

  const [period, setPeriod] = useState<Period>(defaultPeriod);
  const periodIso = () => {
    const { since, until } = periodWindow(period);
    return { from: new Date(since).toISOString(), to: new Date(until).toISOString() };
  };

  const [pendingExpanded, setPendingExpanded] = useState<string | null>(null);
  const [failedExpanded, setFailedExpanded] = useState<string | null>(null);
  // id + HTTP-глагол строки — ReplayDialog по нему решает, требуется ли тело
  // (GET — без тела) и какой метод реинъекции покажет поведение бэкенда.
  const [replay, setReplay] = useState<{ id: string; httpMethod?: string } | null>(null);
  const [replayPeriodOpen, setReplayPeriodOpen] = useState(false);

  // §85: кнопка повтора из логов гаснет по конкретной причине, а не «просто
  // недоступна» — иначе оператор идёт искать права, хотя дело в конфиге узла.
  // Сообщение о sync-узле здесь не нужно: у него кнопки нет вовсе (isAsync).
  const replayPeriodReason = useMemo(() => {
    if (!node.logging_enabled) return { enabled: false, title: t("queue.replay_period_off_logs") };
    if (!hasLogsTable) return { enabled: false, title: t("queue.replay_period_off_table") };
    if (!node.log_request_body) return { enabled: false, title: t("queue.replay_period_off_body") };
    return { enabled: true, title: undefined as string | undefined };
  }, [node.logging_enabled, node.log_request_body, hasLogsTable, t]);

  // Живая очередь (pending) — manager+ («Управление узлами»). На паузе опрашиваем
  // часто (очередь наполняется, нужна живая обратная связь); если есть pending —
  // реже; на enabled с пустой очередью — не молотим Kafka впустую.
  const pendingQ = useQuery({
    queryKey: ["aq-list", id],
    queryFn: () => api.get<ListResp>(`/api/nodes/${id}/async-queue/messages`),
    enabled: isManager && isAsync,
    refetchInterval: (q) => {
      if (node.status === "paused") return 4_000;
      const data = q.state.data as ListResp | undefined;
      return (data?.items.length ?? 0) > 0 ? 8_000 : false;
    },
  });
  const pending = pendingQ.data?.items ?? [];
  const pendingCount = pending.length;
  const pendingCapped = pendingQ.data?.capped ?? false;
  // Секцию показываем всегда (для manager+) — пустое состояние объясняет, почему
  // на активном узле в очереди пусто (см. §35: неудачи уходят в логи/DLQ).
  // У sync-узла очереди не существует — секции нет вовсе (§69.1).
  const showPending = isManager && isAsync;

  // Неудачные доставки — ClickHouse (done=0) за период.
  // ВАЖНО: в queryKey — стабильный periodKey(period), НЕ periodWindow(period).
  // periodWindow содержит until=Date.now() (меняется каждый рендер) → ключ
  // нестабилен → react-query вечно перезапрашивает и данные не «устаканиваются»
  // (KPI «—», список пуст). Окно from/to считается свежим в queryFn при каждом
  // запросе (в т.ч. по refetchInterval), так что значения остаются актуальными.
  const failedCountQ = useQuery({
    queryKey: ["aq-failed-count", id, periodKey(period)],
    queryFn: () => api.get<FailedCountResp>(`/api/nodes/${id}/logs/failed-count`, periodIso()),
    enabled: hasLogsTable,
    refetchInterval: 15_000,
  });
  // §72.3: список неудач читается тем же keyset-механизмом, что журнал логов, —
  // со скроллом и подгрузкой. До §72.3 здесь стоял жёсткий limit=50 без
  // пагинации: KPI показывал 340 неудач, а посмотреть можно было 50.
  //
  // Окно периода (from/to) считается в queryFn заново на каждый запрос, но в
  // ключ идёт стабильный periodKey — periodWindow содержит until=Date.now(),
  // и с ним ключ менялся бы каждый рендер (вечный перезапрос, пустой список).
  //
  // §79.1: список спрашивает НЕДОСТАВЛЕННЫЕ ЗАПИСИ (unresolved), а не строки
  // done=0. Иначе на одном экране KPI (он уже считает записи) показывает 0, а
  // список — запись, доставленную повтором: ровно это нашёл стенд 2026-08-05.
  const failedParams = useMemo(
    () => ({ unresolved: "1", ...periodIso() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [periodKey(period)],
  );
  const failedWrapRef = useRef<HTMLDivElement | null>(null);
  const failedLogs = useInfiniteLogs({
    nodeId: id,
    queryKey: ["aq-failed-list", id, periodKey(period)],
    params: failedParams,
    pageSize: FAILED_PAGE_SIZE,
    enabled: hasLogsTable,
    refetchInterval: 15_000,
    containerRef: failedWrapRef,
  });
  const failed = failedLogs.items;
  // CH временно недоступен: бэкенд отдаёт logs_available=false (а не 500).
  // KPI «неудачные доставки» показываем «—» (не «0», чтобы не вводить в
  // заблуждение), а список — индикатор недоступности.
  const failedUnavailable = failedCountQ.data?.logs_available === false || !failedLogs.logsAvailable;

  const invalidatePending = () => qc.invalidateQueries({ queryKey: ["aq-list", id] });
  const del = useMutation({
    mutationFn: (msgId: string) =>
      api.del(`/api/nodes/${id}/async-queue/messages/${encodeURIComponent(msgId)}`),
    onSuccess: invalidatePending,
  });
  const purge = useMutation({
    mutationFn: (body: { from?: string; to?: string }) =>
      api.post(`/api/nodes/${id}/async-queue/purge`, body),
    onSuccess: invalidatePending,
  });
  const invalidateFailed = () => {
    qc.invalidateQueries({ queryKey: ["aq-failed-count", id] });
    qc.invalidateQueries({ queryKey: ["aq-failed-list", id] });
  };
  // §35/§36: очистка «Неудачных доставок» — отменяет повтор (DLQ-репроцессор
  // перестаёт повторять) и удаляет записи done=0 из CH-логов узла.
  const purgeFailed = useMutation({
    mutationFn: (body: { from?: string; to?: string }) =>
      api.post(`/api/nodes/${id}/async-queue/purge-failed`, body),
    onSuccess: invalidateFailed,
  });
  // §36.11: «Повторить все сейчас» — пере-инжектит все неудачные через Receiver
  // и отменяет их оригиналы в DLQ (без двойной доставки).
  const replayFailed = useMutation({
    mutationFn: (body: { from?: string; to?: string }) =>
      api.post(`/api/nodes/${id}/async-queue/replay-failed`, body),
    onSuccess: () => {
      // Ре-инжекция асинхронна (Receiver→Kafka→Sender→CH ≈ пара секунд): сразу
      // обновляем + ещё раз с задержкой, чтобы список/счётчик актуализировались
      // под новые результаты, не дожидаясь 15с-поллинга.
      invalidateFailed();
      window.setTimeout(invalidateFailed, 2500);
      window.setTimeout(invalidateFailed, 6000);
    },
  });
  const setStatus = useMutation({
    mutationFn: (status: "enabled" | "paused" | "disabled") =>
      api.patch(`/api/nodes/${id}/status`, { status }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["node", id] });
      qc.invalidateQueries({ queryKey: ["aq-list", id] });
    },
  });

  return (
    <div className="space-y-4">
      {/* Пикер прижат ВПРАВО — как на остальных вкладках и как было в релизе.
          Пробовал сдвинуть влево (казалось, что слева пустое место): под ним
          идут плитки во всю ширину, и пикер, прижатый к левому краю, ломает
          общую линию правого края страницы. Пустота слева тут не дефект. */}
      <div className="flex flex-wrap items-center justify-end gap-3">
        {/* §85: повтор запросов из логов за период. Не в секции «Неудачные
            доставки»: операция идёт по ВСЕМ записям окна, а не по её
            подмножеству, и место рядом с фильтром периода это отражает. */}
        {isManager && isAsync && (
          <Button
            sm
            variant="ghost"
            disabled={!replayPeriodReason.enabled}
            title={replayPeriodReason.title}
            onClick={() => setReplayPeriodOpen(true)}
          >
            <History className="h-3.5 w-3.5" /> {t("queue.replay_period")}
          </Button>
        )}
        <PeriodPicker value={period} onChange={setPeriod} maxLookbackMs={nodeLookbackMs(node)} />
      </div>

      {/* У sync-узла плитки «Ожидают отправки» нет: очереди не существует.
          Оставшаяся одна плитка выкладывается НЕ сеткой (см. KpiRow): половина
          строки под неё и пустая половина рядом читались как поломка вёрстки. */}
      <KpiRow cols={2}>
        {isAsync && (
          <Kpi
            label={t("queue.kpi.pending")}
            value={isManager ? `${pendingCount}${pendingCapped ? "+" : ""}` : "—"}
            hint={t("queue.kpi.pending_hint")}
          />
        )}
        <Kpi
          label={t("queue.kpi.failed")}
          value={hasLogsTable && !failedUnavailable ? String(failedCountQ.data?.count ?? "—") : "—"}
          hint={failedUnavailable ? t("logs.unavailable") : t("queue.kpi.failed_hint")}
        />
      </KpiRow>

      <Hint tone={node.status === "enabled" ? "muted" : "warn"}>
        <div className="space-y-2">
          {/* §69.1: у sync-узла на паузе запросы не копятся в очереди, а
              отклоняются на входе — тексты про «накопится в очереди» врали бы. */}
          <p>
            {node.status === "paused"
              ? t(
                  isPull
                    ? "queue.banner.state_paused_pull"
                    : isAsync
                      ? "queue.banner.state_paused"
                      : "queue.banner.state_paused_sync",
                )
              : node.status === "disabled"
                ? t("queue.banner.state_disabled")
                : t(isAsync ? "queue.banner.explain" : "queue.banner.explain_sync")}
          </p>
          {isManager && (
            <div className="flex flex-wrap gap-2">
              {node.status !== "enabled" && (
                <Button
                  sm
                  variant="ghost"
                  disabled={setStatus.isPending}
                  onClick={() => setStatus.mutate("enabled")}
                >
                  <Play className="h-3.5 w-3.5" />{" "}
                  {node.status === "paused" ? t("queue.banner.resume") : t("queue.banner.enable")}
                </Button>
              )}
              {node.status === "enabled" && (
                <Button
                  sm
                  variant="ghost"
                  disabled={setStatus.isPending}
                  onClick={() => setStatus.mutate("paused")}
                >
                  <Pause className="h-3.5 w-3.5" /> {t("queue.banner.pause")}
                </Button>
              )}
              {node.status !== "disabled" && (
                <Button
                  sm
                  variant="ghost"
                  disabled={setStatus.isPending}
                  onClick={async () => {
                    if (
                      await confirm({
                        title: t("queue.banner.disable"),
                        message: t("queue.banner.disable_confirm"),
                        confirmLabel: t("queue.banner.disable"),
                        danger: true,
                      })
                    )
                      setStatus.mutate("disabled");
                  }}
                >
                  <Power className="h-3.5 w-3.5" /> {t("queue.banner.disable")}
                </Button>
              )}
            </div>
          )}
        </div>
      </Hint>

      {/* §81.4: защита узла (circuit breaker). Не зависит от типа узла: у sync
          секции «Ожидают отправки» нет, а карточка есть — именно там и болит
          (боевой acs_sigur). Прячется сама, если Redis не сконфигурирован. */}
      <BreakerCard nodeId={id} />

      {/* Секция «Ожидают отправки» — живая очередь (admin) */}
      {showPending && (
        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold text-fg">{t("queue.section.pending")}</h3>
            {/* Очистка ожидающих доступна всегда (admin): на активном узле очередь
                обычно пуста (доставка сразу) — кнопки безвредны; на паузе/при
                лежащем Sender чистят накопленный backlog (tombstones). */}
            <PurgeButtons
              period={period}
              busy={purge.isPending}
              onPurge={(b) => purge.mutate(b)}
              labels={{
                period: t("queue.purge_period"),
                periodConfirm: t("queue.purge_period_confirm"),
                all: t("queue.purge_all"),
                allConfirm: t("queue.purge_all_confirm"),
              }}
            />
          </div>
          {/* §84.8: голова очереди из УЖЕ загруженного списка — он отсортирован
              по времени приёма, значит items[0] и есть самое старое сообщение.
              Ни нового эндпоинта, ни лишнего давления на KafkaRateLimit. */}
          {pendingCount > 0 && (
            <QueueHeadSummary
              count={pendingCount}
              capped={pendingCapped}
              partition={pending[0].partition}
              headReceivedAt={pending[0].received_at}
              onOpenMetrics={onOpenMetrics}
            />
          )}
          {pending.length === 0 ? (
            <div className="text-fg-muted">
              {node.status === "paused"
                ? t(isPull ? "queue.empty_paused_pull" : "queue.empty_paused")
                : t("queue.empty_enabled")}
            </div>
          ) : (
            <div className="overflow-hidden rounded-md border border-line">
              <table className="w-full text-[13px]">
                <thead className="bg-bg-soft text-left text-[11px] uppercase tracking-wide text-fg-subtle">
                  <tr>
                    <th className="w-8 px-2 py-2"></th>
                    <th className="px-2 py-2">{t("queue.col.received")}</th>
                    <th className="px-2 py-2">{t("queue.col.method")}</th>
                    <th className="px-2 py-2">{t("queue.col.target")}</th>
                    <th className="px-2 py-2 text-right">{t("queue.col.size")}</th>
                    <th className="w-10 px-2 py-2"></th>
                  </tr>
                </thead>
                <tbody>
                  {pending.map((m) => (
                    <PendingRow
                      key={m.id}
                      m={m}
                      nodeId={id}
                      open={pendingExpanded === m.id}
                      onToggle={() => setPendingExpanded(pendingExpanded === m.id ? null : m.id)}
                      onDelete={() => del.mutate(m.id)}
                      deleting={del.isPending}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      {/* Секция «Неудачные доставки» — ClickHouse done=0 */}
      <section className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-sm font-semibold text-fg">{t("queue.section.failed")}</h3>
          <div className="flex flex-wrap items-center justify-end gap-2">
            {/* §36.11/§36.10: повтор всех сейчас + очистка неудачных. Manager+
                («Управление узлами»; маршруты async-queue под authedManager). */}
            {isManager && hasLogsTable && (
              <>
                <Button
                  sm
                  variant="ghost"
                  disabled={replayFailed.isPending}
                  onClick={async () => {
                    const { since, until } = periodWindow(period);
                    if (
                      await confirm({
                        title: t("queue.replay_failed_all"),
                        message: t("queue.replay_failed_all_confirm"),
                        confirmLabel: t("queue.replay_failed_all"),
                      })
                    )
                      replayFailed.mutate({
                        from: new Date(since).toISOString(),
                        to: new Date(until).toISOString(),
                      });
                  }}
                >
                  <RotateCcw className="h-3.5 w-3.5" /> {t("queue.replay_failed_all")}
                </Button>
                <PurgeButtons
                  period={period}
                  busy={purgeFailed.isPending}
                  onPurge={(b) => purgeFailed.mutate(b)}
                  labels={{
                    period: t("queue.purge_failed_period"),
                    // §69.1: у sync-узла нет авто-повтора — обещать его отмену
                    // в подтверждении нельзя, чистится только вид логов.
                    periodConfirm: t(
                      isAsync
                        ? "queue.purge_failed_period_confirm"
                        : "queue.purge_failed_period_confirm_sync",
                    ),
                    all: t("queue.purge_failed_all"),
                    allConfirm: t(
                      isAsync
                        ? "queue.purge_failed_all_confirm"
                        : "queue.purge_failed_all_confirm_sync",
                    ),
                  }}
                />
              </>
            )}
            {hasLogsTable && onOpenFailedLogs && (
              <button
                type="button"
                className="text-xs text-accent transition-colors hover:text-fg"
                onClick={() => {
                  const { since, until } = periodWindow(period);
                  onOpenFailedLogs({
                    from: msToDatetimeLocal(since),
                    to: msToDatetimeLocal(until),
                    done: "no",
                  });
                }}
              >
                {t("queue.open_in_logs")}
              </button>
            )}
          </div>
        </div>
        {/* §36: подсказка про авто-репроцессор DLQ — повтор до TTL узла.
            §69.1: у sync-узла DLQ нет, повторять некому — своя подсказка. */}
        <p className="text-xs text-fg-muted">
          {isAsync
            ? t("queue.failed.reprocess_hint", {
                hours: Math.round(((node.dlq_ttl_seconds ?? 86400) / 3600) * 10) / 10,
              })
            : t("queue.failed.no_reprocess_hint")}
        </p>
        {!hasLogsTable ? (
          <div className="text-fg-muted">{t("queue.failed.no_logging")}</div>
        ) : failedUnavailable ? (
          <div className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
            {t("logs.unavailable")}
          </div>
        ) : failed.length === 0 ? (
          <div className="text-fg-muted">{t("queue.failed.empty")}</div>
        ) : (
          // §72.3: свой скролл-контейнер — по нему хук считает близость к низу и
          // подгружает следующую страницу неудач.
          <div
            ref={failedWrapRef}
            onScroll={failedLogs.onScroll}
            className="max-h-[60vh] overflow-y-auto rounded-md border border-line"
          >
            <table className="w-full text-[13px]">
              <thead className="bg-bg-soft text-left text-[11px] uppercase tracking-wide text-fg-subtle">
                <tr>
                  <th className="w-8 px-2 py-2"></th>
                  <th className="px-2 py-2">{t("queue.col.received")}</th>
                  <th className="px-2 py-2">{t("queue.col.method")}</th>
                  <th className="px-2 py-2">{t("queue.failed.col.status")}</th>
                  <th className="px-2 py-2">{t("queue.failed.col.reason")}</th>
                  <th className="w-10 px-2 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {failed.map((r) => (
                  <FailedRow
                    key={r.id}
                    r={r}
                    nodeId={id}
                    open={failedExpanded === r.id}
                    onToggle={() => setFailedExpanded(failedExpanded === r.id ? null : r.id)}
                    onReplay={() => setReplay({ id: r.id, httpMethod: r.http_method })}
                    canReplay={isManager}
                  />
                ))}
                {failedLogs.query.isFetchingNextPage && (
                  <tr>
                    <td colSpan={6} className="px-2 py-3 text-center text-[11px] text-fg-muted">
                      {t("common.loading")}
                    </td>
                  </tr>
                )}
                {/* Потолок накопленных строк (§44.K) — дальше не подгружаем,
                    просим сузить период. */}
                {failedLogs.atCap && failedLogs.query.hasNextPage && (
                  <tr>
                    <td colSpan={6} className="px-2 py-3 text-center text-[11px] text-warn">
                      {t("logs.cap_reached", { n: MAX_INFINITE_ROWS })}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {replay && (
        <ReplayDialog
          logId={replay.id}
          nodeId={id}
          httpMethod={replay.httpMethod}
          incomingMethod={node.incoming_method}
          onClose={() => setReplay(null)}
        />
      )}

      {replayPeriodOpen && (
        <ReplayPeriodDialog
          node={node}
          onClose={() => setReplayPeriodOpen(false)}
          onFinished={invalidateFailed}
        />
      )}
    </div>
  );
}

// PurgeButtons — пара кнопок «Очистить за период» / «Очистить все» с
// подтверждением. Переиспользуется для pending-очереди и неудачных доставок —
// меняются лишь подписи (labels) и обработчик onPurge (разные эндпоинты).
function PurgeButtons({
  period,
  busy,
  labels,
  onPurge,
}: {
  period: Period;
  busy: boolean;
  labels: { period: string; periodConfirm: string; all: string; allConfirm: string };
  onPurge: (body: { from?: string; to?: string }) => void;
}) {
  const confirm = useConfirm();
  return (
    <div className="flex gap-2">
      <Button
        sm
        variant="ghost"
        disabled={busy}
        onClick={async () => {
          const { since, until } = periodWindow(period);
          if (await confirm({ title: labels.period, message: labels.periodConfirm, confirmLabel: labels.period, danger: true }))
            onPurge({ from: new Date(since).toISOString(), to: new Date(until).toISOString() });
        }}
      >
        {labels.period}
      </Button>
      <Button
        sm
        variant="danger"
        disabled={busy}
        onClick={async () => {
          if (await confirm({ title: labels.all, message: labels.allConfirm, confirmLabel: labels.all, danger: true }))
            onPurge({});
        }}
      >
        {labels.all}
      </Button>
    </div>
  );
}

function PendingRow({
  m,
  nodeId,
  open,
  onToggle,
  onDelete,
  deleting,
}: {
  m: QueueMessage;
  nodeId: string;
  open: boolean;
  onToggle: () => void;
  onDelete: () => void;
  deleting: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <tr className="border-t border-line hover:bg-bg-muted/40">
        <td className="px-2 py-2">
          <button type="button" onClick={onToggle} className="text-fg-muted hover:text-fg">
            <ChevronRight className={cn("h-4 w-4 transition-transform", open && "rotate-90")} />
          </button>
        </td>
        <td className="px-2 py-2 font-mono text-[11px] text-fg-muted">
          {new Date(m.received_at).toLocaleString()}
        </td>
        <td className="px-2 py-2 font-mono">{m.method}</td>
        <td className="max-w-md truncate px-2 py-2 font-mono text-[11px]" title={m.target_url}>
          {m.target_url}
        </td>
        <td className="px-2 py-2 text-right font-mono text-[11px] text-fg-muted">{m.body_size}</td>
        <td className="px-2 py-2">
          <button
            type="button"
            disabled={deleting}
            onClick={onDelete}
            className="text-err hover:opacity-80 disabled:opacity-40"
            title={t("queue.delete")}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </td>
      </tr>
      {open && (
        <tr className="border-t border-line bg-bg-muted/30">
          <td colSpan={6} className="px-4 py-3">
            <PendingBody nodeId={nodeId} topic={m.topic} partition={m.partition} offset={m.offset} />
          </td>
        </tr>
      )}
    </>
  );
}

function PendingBody({
  nodeId,
  topic,
  partition,
  offset,
}: {
  nodeId: string;
  topic?: string;
  partition: number;
  offset: number;
}) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["aq-body", nodeId, topic ?? "", partition, offset],
    queryFn: () =>
      api.get<BodyResp>(`/api/nodes/${nodeId}/async-queue/messages/body`, {
        partition,
        offset,
        ...(topic ? { topic } : {}),
      }),
    staleTime: 60_000,
  });
  if (q.isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (q.isError || !q.data) return <div className="text-err">{t("common.error")}</div>;
  return (
    <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted/50 p-2 font-mono text-[11px]">
      {q.data.body ? prettyJson(q.data.body) : t("queue.body_empty")}
    </pre>
  );
}

function FailedRow({
  r,
  nodeId,
  open,
  onToggle,
  onReplay,
  canReplay,
}: {
  r: LogRow;
  nodeId: string;
  open: boolean;
  onToggle: () => void;
  onReplay: () => void;
  canReplay: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <tr className="border-t border-line hover:bg-bg-muted/40">
        <td className="px-2 py-2">
          <button type="button" onClick={onToggle} className="text-fg-muted hover:text-fg">
            <ChevronRight className={cn("h-4 w-4 transition-transform", open && "rotate-90")} />
          </button>
        </td>
        <td className="px-2 py-2 font-mono text-[11px] text-fg-muted">
          {new Date(r.date_request).toLocaleString()}
        </td>
        <td className="px-2 py-2 font-mono">{r.method}</td>
        <td className="px-2 py-2">
          <Pill tone="err">{r.status || "—"}</Pill>
        </td>
        <td className="max-w-xs truncate px-2 py-2 font-mono text-[11px] text-err" title={r.reason}>
          {r.reason}
        </td>
        <td className="px-2 py-2">
          {/* §7.4.1/§58: replay — manager+. viewer видит кнопку disabled. */}
          <button
            type="button"
            disabled={!canReplay}
            onClick={onReplay}
            className={cn(
              "text-fg-muted",
              canReplay ? "hover:text-accent" : "cursor-not-allowed opacity-40",
            )}
            title={canReplay ? t("queue.failed.replay") : t("common.no_permission")}
          >
            <RotateCcw className="h-3.5 w-3.5" />
          </button>
        </td>
      </tr>
      {open && (
        <tr className="border-t border-line bg-bg-muted/30">
          <td colSpan={6} className="px-4 py-3">
            <FailedBody nodeId={nodeId} logId={r.id} />
          </td>
        </tr>
      )}
    </>
  );
}

function FailedBody({ nodeId, logId }: { nodeId: string; logId: string }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["log", nodeId, logId],
    queryFn: () => api.get<LogDetail>(`/api/nodes/${nodeId}/log/${logId}`),
    staleTime: 60_000,
  });
  if (q.isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (q.isError || !q.data) return <div className="text-err">{t("common.error")}</div>;
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
      <div className="min-w-0">
        <div className="mb-1 text-[10px] uppercase tracking-wider text-fg-muted">
          {t("logs.detail.request")}
        </div>
        <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted/50 p-2 font-mono text-[11px]">
          {q.data.request ? prettyJson(q.data.request) : t("logs.detail.empty")}
        </pre>
      </div>
      <div className="min-w-0">
        <div className="mb-1 text-[10px] uppercase tracking-wider text-fg-muted">
          {t("logs.detail.response")}
        </div>
        <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-muted/50 p-2 font-mono text-[11px]">
          {q.data.response ? prettyJson(q.data.response) : t("logs.detail.empty")}
        </pre>
      </div>
    </div>
  );
}

/**
 * QueueHeadSummary — голова очереди узла (§84.8, узловой срез §80.2).
 *
 * Возраст головы отвечает на вопрос, который число «ждут N» не закрывает:
 * «доставка встала или просто много трафика». Считается из УЖЕ загруженного
 * списка — он отсортирован по времени приёма, поэтому items[0] и есть самое
 * старое сообщение; нового запроса к Kafka не появляется.
 *
 * capped означает, что выборка упёрлась в предел: показываем «50+», а не «50»,
 * иначе число читается как точное.
 */
function QueueHeadSummary({
  count,
  capped,
  partition,
  headReceivedAt,
  onOpenMetrics,
}: {
  count: number;
  capped: boolean;
  partition: number;
  headReceivedAt: string;
  onOpenMetrics?: () => void;
}) {
  const { t } = useTranslation();
  const ageMs = Math.max(Date.now() - Date.parse(headReceivedAt), 0);
  const ageMin = Math.round(ageMs / 60_000);
  // Пять минут — уже не «просто много трафика»: при штатной доставке голова
  // очереди живёт секунды.
  const stale = ageMin >= 5;
  return (
    <p className={cn("text-xs", stale ? "text-warn" : "text-fg-muted")}>
      {t("queue.head.summary", {
        waiting: `${count}${capped ? "+" : ""}`,
        partition,
        age: ageMin < 1 ? t("queue.head.age_lt_min") : t("queue.head.age_min", { m: ageMin }),
      })}
      {stale && " ⚠"}
      {onOpenMetrics && (
        <>
          {" · "}
          <button
            type="button"
            onClick={onOpenMetrics}
            className="text-accent underline-offset-2 hover:underline"
          >
            {t("queue.head.capacity_link")}
          </button>
        </>
      )}
    </p>
  );
}
