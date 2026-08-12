import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api, type Node } from "../../api/client";
import { CopyButton } from "../ui";
import { shouldShowAck } from "../../lib/logAckGate";

export type LogAckResult = {
  applicable: boolean;
  not_applicable_reason?: string;
  ok: boolean;
  status?: number;
  content_type?: string;
  body?: string;
  reason?: string;
  placeholder?: string;
  message?: string;
  on_error?: string;
  spec_changed_after_request: boolean;
  spec_updated_at_ms?: number;
  request_at_ms?: number;
  body_source?: string;
  warnings?: string[];
};

function fmtStamp(ms?: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * LogAckBlock — чем шина ответила БЫ на этот запрос по текущему шаблону (§84.9).
 *
 * Это ПЕРЕСЧЁТ, а не факт: отрендеренный ответ приёма не сохраняется нигде —
 * Receiver отдаёт его в сокет и с ClickHouse не соединён вовсе. Всё, что мешает
 * принять расчёт за факт, показывается прямо здесь: пересчёт, ошибочно принятый
 * за факт, хуже отсутствия пересчёта.
 */
export function LogAckBlock({
  node,
  logId,
  recordType,
}: {
  node: Node;
  logId: string;
  recordType?: string;
}) {
  const { t } = useTranslation();
  const enabled = shouldShowAck(node, recordType);

  const q = useQuery({
    queryKey: ["log-ack", node.id, logId],
    queryFn: () => api.get<LogAckResult>(`/api/nodes/${node.id}/log/${logId}/ack`),
    // Оба условия проверены на клиенте по уже загруженному узлу и записи —
    // в отсекаемых случаях запрос на сервер не уходит ни разу.
    enabled,
    staleTime: 60_000,
  });

  if (!enabled) return null;
  const d = q.data;
  if (!d) return null;
  // Серверная проверка — второй рубеж: спеку могли снять уже после открытия
  // страницы.
  if (!d.applicable) return null;

  return (
    <div className="mt-3 rounded-md border border-line p-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-xs font-semibold uppercase tracking-wide text-fg-muted">
          {t("logs.detail.ack.title")}
        </span>
        {d.ok && d.body && <CopyButton value={d.body} />}
      </div>

      {d.ok ? (
        <>
          <div className="mb-1 text-xs text-fg-muted">
            {d.status} · {d.content_type}
          </div>
          <pre className="overflow-x-auto rounded bg-bg-muted p-2 text-xs">{d.body}</pre>
        </>
      ) : (
        <div className="text-xs text-err">
          {t("logs.detail.ack.failed", {
            reason: d.reason ? t(`node.ack.reason.${d.reason}`, { defaultValue: d.reason }) : d.message,
          })}
          {d.placeholder && <div className="mt-1 font-mono">{d.placeholder}</div>}
          {/* Политика узла решает, что клиент получил бы на самом деле, и это
              разные исходы — молчать о ней нельзя. */}
          <div className="mt-1 text-fg-muted">
            {d.on_error === "error"
              ? t("logs.detail.ack.on_error_error")
              : t("logs.detail.ack.on_error_default")}
          </div>
        </div>
      )}

      <div className="mt-2 space-y-1 border-t border-line pt-2 text-xs text-fg-muted">
        {/* Главное предупреждение — всегда, а не только при подозрениях. */}
        <div className="text-warn">⚠ {t("logs.detail.ack.warn_now_not_then")}</div>
        {d.spec_changed_after_request && (
          <div className="text-warn">
            ⚠{" "}
            {t("logs.detail.ack.warn_spec_changed", {
              changed: fmtStamp(d.spec_updated_at_ms),
              request: fmtStamp(d.request_at_ms),
            })}
          </div>
        )}
        {d.body_source === "truncated" && (
          <div className="text-warn">⚠ {t("logs.detail.ack.warn_truncated")}</div>
        )}
        {d.warnings?.includes("query_not_stored") && (
          <div>ⓘ {t("logs.detail.ack.warn_query_not_stored")}</div>
        )}
        {d.on_error === "error" && <div>ⓘ {t("logs.detail.ack.note_on_error_error")}</div>}
      </div>
    </div>
  );
}
