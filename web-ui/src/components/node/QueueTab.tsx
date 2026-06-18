import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Trash2, ChevronRight, Pause, Power, RotateCcw } from "lucide-react";

import { api, type Node } from "../../api/client";
import { Button, Kpi, KpiRow, Hint, Pill, PeriodPicker, periodWindow, defaultPeriod, type Period } from "../ui";
import { ReplayDialog } from "../ReplayDialog";
import { type LogsInitialFilter } from "./LogsTab";
import { type LogRow, type LogsResp, type LogDetail } from "./types";
import { cn } from "../../lib/cn";
import { msToDatetimeLocal } from "../../lib/format";
import { useConfirm } from "../../lib/confirm";
import { useRoleAtLeast } from "../../lib/useCurrentRole";

type QueueMessage = {
  id: string;
  partition: number;
  offset: number;
  method: string;
  target_url: string;
  received_at: string;
  body_size: number;
};
type ListResp = { items: QueueMessage[]; capped: boolean; kafka_available: boolean };
type BodyResp = { id: string; method: string; target_url: string; headers?: Record<string, string>; body: string };
type FailedCountResp = { count: number; logs_configured: boolean };

function prettyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// QueueTab — вкладка «Очередь» узла requestAsync (§35). Две части:
//  • «Ожидают отправки» — живая очередь nexus.async (peek, только admin; непуста
//    лишь для paused-узлов/лежащего Sender) с удалением/очисткой через tombstones;
//  • «Неудачные доставки» — из ClickHouse-логов (done=0, быстро), с reason/телом/
//    replay. Очистка неудач не через Kafka (нельзя), а пауза/отключение узла +
//    replay; фильтр по периоду показывает свежие.
export function QueueTab({
  node,
  onOpenFailedLogs,
}: {
  node: Node;
  onOpenFailedLogs?: (f: LogsInitialFilter) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();
  const id = node.id;
  const isAdmin = useRoleAtLeast("admin");
  const isManager = useRoleAtLeast("manager");
  const hasLogsTable = !!node.clickhouse_table;

  const [period, setPeriod] = useState<Period>(defaultPeriod);
  const periodIso = () => {
    const { since, until } = periodWindow(period);
    return { from: new Date(since).toISOString(), to: new Date(until).toISOString() };
  };

  const [pendingExpanded, setPendingExpanded] = useState<string | null>(null);
  const [failedExpanded, setFailedExpanded] = useState<string | null>(null);
  const [replayId, setReplayId] = useState<string | null>(null);

  // Живая очередь (pending) — только admin. Поллим лишь когда узел paused или
  // есть pending — на enabled-узле очередь пуста, нет смысла молотить Kafka.
  const pendingQ = useQuery({
    queryKey: ["aq-list", id],
    queryFn: () => api.get<ListResp>(`/api/nodes/${id}/async-queue/messages`),
    enabled: isAdmin,
    refetchInterval: (q) => {
      const data = q.state.data as ListResp | undefined;
      return node.status === "paused" || (data?.items.length ?? 0) > 0 ? 30_000 : false;
    },
  });
  const pending = pendingQ.data?.items ?? [];
  const pendingCount = pending.length;
  const pendingCapped = pendingQ.data?.capped ?? false;
  const showPending = isAdmin && (node.status === "paused" || pendingCount > 0);

  // Неудачные доставки — ClickHouse (done=0) за период.
  const failedCountQ = useQuery({
    queryKey: ["aq-failed-count", id, periodWindow(period)],
    queryFn: () => api.get<FailedCountResp>(`/api/nodes/${id}/logs/failed-count`, periodIso()),
    enabled: hasLogsTable,
    refetchInterval: 15_000,
  });
  const failedListQ = useQuery({
    queryKey: ["aq-failed-list", id, periodWindow(period)],
    queryFn: () => api.get<LogsResp>(`/api/nodes/${id}/logs`, { done: "no", ...periodIso(), limit: 50 }),
    enabled: hasLogsTable,
    refetchInterval: 15_000,
  });
  const failed = failedListQ.data?.items ?? [];

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
  const setStatus = useMutation({
    mutationFn: (status: "paused" | "disabled") =>
      api.patch(`/api/nodes/${id}/status`, { status }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["node", id] }),
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-end gap-3">
        <PeriodPicker value={period} onChange={setPeriod} />
      </div>

      <KpiRow cols={2}>
        <Kpi
          label={t("queue.kpi.pending")}
          value={isAdmin ? `${pendingCount}${pendingCapped ? "+" : ""}` : "—"}
          hint={t("queue.kpi.pending_hint")}
        />
        <Kpi
          label={t("queue.kpi.failed")}
          value={hasLogsTable ? String(failedCountQ.data?.count ?? "—") : "—"}
          hint={t("queue.kpi.failed_hint")}
        />
      </KpiRow>

      <Hint tone="warn">
        <div className="space-y-2">
          <p>{t("queue.banner.explain")}</p>
          {isManager && node.status === "enabled" && (
            <div className="flex flex-wrap gap-2">
              <Button
                sm
                variant="ghost"
                disabled={setStatus.isPending}
                onClick={() => setStatus.mutate("paused")}
              >
                <Pause className="h-3.5 w-3.5" /> {t("queue.banner.pause")}
              </Button>
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
            </div>
          )}
        </div>
      </Hint>

      {/* Секция «Ожидают отправки» — живая очередь (admin) */}
      {showPending && (
        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold text-fg">{t("queue.section.pending")}</h3>
            {pendingCount > 0 && (
              <div className="flex gap-2">
                <Button
                  sm
                  variant="ghost"
                  disabled={purge.isPending}
                  onClick={async () => {
                    const { since, until } = periodWindow(period);
                    if (
                      await confirm({
                        title: t("queue.purge_period"),
                        message: t("queue.purge_period_confirm"),
                        confirmLabel: t("queue.purge_period"),
                        danger: true,
                      })
                    )
                      purge.mutate({
                        from: new Date(since).toISOString(),
                        to: new Date(until).toISOString(),
                      });
                  }}
                >
                  {t("queue.purge_period")}
                </Button>
                <Button
                  sm
                  variant="danger"
                  disabled={purge.isPending}
                  onClick={async () => {
                    if (
                      await confirm({
                        title: t("queue.purge_all"),
                        message: t("queue.purge_all_confirm"),
                        confirmLabel: t("queue.purge_all"),
                        danger: true,
                      })
                    )
                      purge.mutate({});
                  }}
                >
                  {t("queue.purge_all")}
                </Button>
              </div>
            )}
          </div>
          {pending.length === 0 ? (
            <div className="text-fg-muted">{t("queue.empty")}</div>
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
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-fg">{t("queue.section.failed")}</h3>
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
        {!hasLogsTable ? (
          <div className="text-fg-muted">{t("queue.failed.no_logging")}</div>
        ) : failed.length === 0 ? (
          <div className="text-fg-muted">{t("queue.failed.empty")}</div>
        ) : (
          <div className="overflow-hidden rounded-md border border-line">
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
                    onReplay={() => setReplayId(r.id)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {replayId && <ReplayDialog logId={replayId} nodeId={id} onClose={() => setReplayId(null)} />}
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
            <PendingBody nodeId={nodeId} partition={m.partition} offset={m.offset} />
          </td>
        </tr>
      )}
    </>
  );
}

function PendingBody({ nodeId, partition, offset }: { nodeId: string; partition: number; offset: number }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["aq-body", nodeId, partition, offset],
    queryFn: () =>
      api.get<BodyResp>(`/api/nodes/${nodeId}/async-queue/messages/body`, { partition, offset }),
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
}: {
  r: LogRow;
  nodeId: string;
  open: boolean;
  onToggle: () => void;
  onReplay: () => void;
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
          <button
            type="button"
            onClick={onReplay}
            className="text-fg-muted hover:text-accent"
            title={t("queue.failed.replay")}
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
