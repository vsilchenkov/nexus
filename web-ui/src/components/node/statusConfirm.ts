import type { QueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";

import { api } from "../../api/client";
import type { ConfirmOptions } from "../../lib/confirm";

// §113.11: подтверждения смены статуса узла. Общие для формы настроек (на
// «Сохранить») и кнопок вкладки «Очередь» — правила и тексты в одном месте,
// иначе два входа в одну операцию разъедутся.

export type NodeStatus = "enabled" | "paused" | "disabled";

// NodeState — то, от чего зависит наличие очереди и смысл паузы.
export type NodeState = { root_method: string; status: NodeStatus };

// QueueSnapshot — сколько сообщений ждёт в очереди узла. null — неизвестно
// (Kafka недоступна, запрос упал): «очередь пуста» из этого не следует.
export type QueueSnapshot = { count: number; capped: boolean } | null;

// QueueListResp — ответ GET /api/nodes/{id}/async-queue/messages в той части,
// что нужна снимку.
type QueueListResp = { items: unknown[]; capped: boolean; kafka_available: boolean };

// hasAsyncQueue — есть ли у узла очередь Nexus. Зеркало серверного
// usesAsyncQueue (§113.9): async- и pull-узлы, а sync — только на паузе.
export function hasAsyncQueue(n: NodeState): boolean {
  return n.root_method !== "request" || n.status === "paused";
}

// pausesSyncNode — смена приводит узел в состояние «sync на паузе» (§113.11).
// Ловит и постановку на паузу, и смену типа у узла, уже стоящего на паузе.
// from=undefined — новый узел.
export function pausesSyncNode(from: NodeState | undefined, to: NodeState): boolean {
  const syncPaused = (n: NodeState) => n.root_method === "request" && n.status === "paused";
  return syncPaused(to) && !(from && syncPaused(from));
}

// disablesQueue — смена выключает узел, у которого сейчас есть очередь: её
// сообщения будут удалены без доставки. Нужно ли спрашивать — решает снимок.
export function disablesQueue(from: NodeState | undefined, to: NodeState): boolean {
  return !!from && to.status === "disabled" && from.status !== "disabled" && hasAsyncQueue(from);
}

// pauseSyncConfirm — инцидент 06.10.2026 начался с паузы, поставленной в
// ожидании отказа: sync-узел на паузе отвечает 202 и копит запросы.
export function pauseSyncConfirm(t: TFunction): ConfirmOptions {
  return {
    title: t("node.status_confirm.pause_sync_title"),
    message: t("node.status_confirm.pause_sync_message"),
    confirmLabel: t("node.status_confirm.pause_sync_action"),
  };
}

// disableConfirm — подтверждение выключения. Число сообщений — из списка
// очереди («50+» при усечении); неизвестное число не выдаётся за ноль.
export function disableConfirm(queue: QueueSnapshot, t: TFunction): ConfirmOptions {
  let message: string;
  if (queue === null) message = t("node.status_confirm.disable_unknown_message");
  else if (queue.count === 0) message = t("node.status_confirm.disable_empty_message");
  else
    message = t("node.status_confirm.disable_queue_message", {
      waiting: `${queue.count}${queue.capped ? "+" : ""}`,
    });
  return {
    title: t("node.status_confirm.disable_title"),
    message,
    confirmLabel: t("node.status_confirm.disable_action"),
    danger: true,
  };
}

// queueSnapshotOf — снимок из ответа списка очереди. Ответ без Kafka
// (kafka_available=false) пуст не потому, что очередь пуста.
export function queueSnapshotOf(resp: QueueListResp | undefined): QueueSnapshot {
  if (!resp || !resp.kafka_available) return null;
  return { count: resp.items.length, capped: resp.capped };
}

// fetchQueueSnapshot — свежий снимок очереди узла через общий кеш вкладки
// «Очередь» (тот же queryKey). Ошибка — null: выключение всё равно спросим.
export async function fetchQueueSnapshot(qc: QueryClient, nodeId: string): Promise<QueueSnapshot> {
  try {
    const resp = await qc.fetchQuery({
      queryKey: ["aq-list", nodeId],
      queryFn: () => api.get<QueueListResp>(`/api/nodes/${nodeId}/async-queue/messages`),
      staleTime: 0,
    });
    return queueSnapshotOf(resp);
  } catch {
    return null;
  }
}
