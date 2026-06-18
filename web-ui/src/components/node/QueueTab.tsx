import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Trash2, ChevronRight } from "lucide-react";

import { api, type Node } from "../../api/client";
import { Button, PeriodPicker, periodWindow, defaultPeriod, type Period } from "../ui";
import { cn } from "../../lib/cn";
import { useConfirm } from "../../lib/confirm";

type QueueMessage = {
  id: string;
  partition: number;
  offset: number;
  method: string;
  target_url: string;
  received_at: string;
  body_size: number;
};
type DepthResp = { count: number; capped: boolean; kafka_available: boolean };
type ListResp = { items: QueueMessage[]; capped: boolean; kafka_available: boolean };
type BodyResp = {
  id: string;
  method: string;
  target_url: string;
  headers?: Record<string, string>;
  body: string;
};

function prettyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// QueueTab — управление накопившейся async-очередью Kafka узла requestAsync (§34.4):
// глубина, первые 50 запросов (с ленивой подгрузкой тела), удаление одного /
// очистка за период / очистка всей очереди. Только для root_method=requestAsync.
export function QueueTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();
  const id = node.id;
  const [expanded, setExpanded] = useState<string | null>(null);
  const [period, setPeriod] = useState<Period>(defaultPeriod);

  const depthQ = useQuery({
    queryKey: ["aq-depth", id],
    queryFn: () => api.get<DepthResp>(`/api/nodes/${id}/async-queue/depth`),
    refetchInterval: 5000,
  });
  const listQ = useQuery({
    queryKey: ["aq-list", id],
    queryFn: () => api.get<ListResp>(`/api/nodes/${id}/async-queue/messages`),
    refetchInterval: 5000,
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["aq-depth", id] });
    qc.invalidateQueries({ queryKey: ["aq-list", id] });
  };

  const del = useMutation({
    mutationFn: (msgId: string) =>
      api.del(`/api/nodes/${id}/async-queue/messages/${encodeURIComponent(msgId)}`),
    onSuccess: invalidate,
  });
  const purge = useMutation({
    mutationFn: (body: { from?: string; to?: string }) =>
      api.post(`/api/nodes/${id}/async-queue/purge`, body),
    onSuccess: invalidate,
  });

  const kafkaAvailable =
    (depthQ.data?.kafka_available ?? true) && (listQ.data?.kafka_available ?? true);
  const capped = depthQ.data?.capped || listQ.data?.capped;
  const items = listQ.data?.items ?? [];

  if (!kafkaAvailable && !depthQ.isLoading) {
    return <div className="text-fg-muted">{t("queue.unavailable")}</div>;
  }

  return (
    <div className="space-y-4">
      {/* Глубина очереди + очистка всего */}
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-line bg-bg-soft px-4 py-3">
        <div>
          <div className="text-[11px] uppercase tracking-wide text-fg-subtle">
            {t("queue.depth")}
          </div>
          <div className="text-lg font-semibold text-fg">
            {depthQ.data ? depthQ.data.count : "—"}
            {capped && <span className="ml-1 text-[11px] text-warn">{t("queue.capped_mark")}</span>}
          </div>
        </div>
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

      {capped && <div className="text-xs text-warn">{t("queue.capped_hint")}</div>}

      {/* Очистка за период */}
      <div className="flex flex-wrap items-center gap-3 rounded-md border border-line px-4 py-3">
        <span className="text-xs text-fg-muted">{t("queue.purge_period")}</span>
        <PeriodPicker value={period} onChange={setPeriod} />
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
          {t("queue.purge")}
        </Button>
      </div>

      {/* Список запросов */}
      {listQ.isLoading ? (
        <div className="text-fg-muted">{t("common.loading")}</div>
      ) : items.length === 0 ? (
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
              {items.map((m) => {
                const open = expanded === m.id;
                return (
                  <FragmentRow
                    key={m.id}
                    m={m}
                    nodeId={id}
                    open={open}
                    onToggle={() => setExpanded(open ? null : m.id)}
                    onDelete={() => del.mutate(m.id)}
                    deleting={del.isPending}
                  />
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function FragmentRow({
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
            <QueueBody nodeId={nodeId} partition={m.partition} offset={m.offset} />
          </td>
        </tr>
      )}
    </>
  );
}

function QueueBody({
  nodeId,
  partition,
  offset,
}: {
  nodeId: string;
  partition: number;
  offset: number;
}) {
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
