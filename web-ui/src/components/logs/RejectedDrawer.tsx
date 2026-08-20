import { useEffect } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, ExternalLink, Trash2, X } from "lucide-react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import { fmtNum } from "../../lib/format";
import { reasonLabelKey, type RejectedDetails } from "../../lib/rejected";
import { Button, ErrorAlert } from "../ui";

type Props = {
  id: string;
  onClose: () => void;
  // onChanged — список и сводку нужно перечитать: разобранная группа исчезает
  // из выдачи, удалённая — тем более.
  onChanged: () => void;
};

// RejectedDrawer — карточка группы отказов (§94.7): клиенты, последние
// запросы, подсказка похожего узла и действия «разобрано» / «удалить».
//
// Панель справа, а не диалог по центру: список остаётся на виду, и по нему
// видно, из какой строки открыта карточка. Закрывается Esc и крестиком —
// клик по подложке НЕ закрывает (общее правило диалогов §21).
export function RejectedDrawer({ id, onClose, onChanged }: Props) {
  const { t } = useTranslation();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const q = useQuery({
    queryKey: ["rejected", "details", id],
    queryFn: () => api.get<RejectedDetails>(`/api/rejected/${id}`),
  });

  const resolve = useMutation({
    mutationFn: () => api.post(`/api/rejected/${id}/resolve`, {}),
    onSuccess: () => {
      onChanged();
      onClose();
    },
  });
  const remove = useMutation({
    mutationFn: () => api.del(`/api/rejected/${id}`),
    onSuccess: () => {
      onChanged();
      onClose();
    },
  });

  const g = q.data?.group;
  const reasonKey = g ? reasonLabelKey(g.reason) : "";

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/40">
      <aside className="flex h-full w-full max-w-xl flex-col overflow-hidden border-l border-line-strong bg-app shadow-2xl">
        <div className="flex shrink-0 items-start justify-between gap-3 border-b border-line px-5 py-4">
          <div className="min-w-0">
            <h3 className="break-all text-[15px] font-semibold">{g?.node_path ?? "…"}</h3>
            {g && (
              <div className="mt-0.5 text-xs text-fg-muted">
                <span className="font-mono">{g.http_method}</span> · {g.status}{" "}
                {reasonKey ? t(reasonKey) : g.reason} · {g.team_name || g.team_slug}
              </div>
            )}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("common.close")}
            className="grid h-8 w-8 shrink-0 place-items-center rounded-md text-fg-muted hover:bg-bg-muted hover:text-fg"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4 text-[13px]">
          {q.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
          {q.isError && <ErrorAlert />}

          {g && (
            <div className="text-fg-muted">
              {t("rejected.details.seen", {
                first: new Date(g.first_seen).toLocaleString(),
                last: new Date(g.last_seen).toLocaleString(),
                total: fmtNum(g.count),
              })}
              {g.resolved_at && (
                <div className="mt-1 text-ok">
                  {t("rejected.details.resolved_by", {
                    who: g.resolved_by || "—",
                    when: new Date(g.resolved_at).toLocaleString(),
                  })}
                </div>
              )}
            </div>
          )}

          {q.data?.suggestion && (
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-accent/40 bg-accent/5 px-3 py-2">
              <span>
                {t("rejected.details.suggestion", { path: q.data.suggestion.path })}
              </span>
              <Link
                to={`/nodes/${q.data.suggestion.node_id}`}
                className="inline-flex items-center gap-1 text-accent hover:underline"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                {t("rejected.details.open_node")}
              </Link>
            </div>
          )}

          <section>
            <h4 className="mb-1.5 text-[11px] uppercase tracking-wide text-fg-muted">
              {t("rejected.details.clients")}
            </h4>
            <div className="overflow-x-auto">
              <table className="w-full text-[12.5px]">
                <tbody>
                  {(q.data?.clients ?? []).map((c) => (
                    <tr key={c.ip} className="border-b border-line/50 last:border-0">
                      <td className="py-1.5 pr-3 font-mono">{c.ip}</td>
                      <td className="py-1.5 pr-3 text-fg-muted">{c.host || "—"}</td>
                      <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(c.count)}</td>
                      <td className="py-1.5 text-fg-muted break-all">{c.user_agent || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {(q.data?.clients?.length ?? 0) === 0 && !q.isLoading && (
                <div className="text-fg-muted">{t("rejected.details.no_clients")}</div>
              )}
            </div>
          </section>

          <section>
            <h4 className="mb-1.5 text-[11px] uppercase tracking-wide text-fg-muted">
              {t("rejected.details.samples")}
            </h4>
            <div className="space-y-1.5 font-mono text-[11.5px]">
              {(q.data?.samples ?? []).map((s, i) => (
                <div key={`${s.at}-${i}`} className="border-b border-line/40 pb-1.5 last:border-0">
                  <div className="flex flex-wrap gap-x-2 text-fg">
                    <span className="text-fg-subtle">{new Date(s.at).toLocaleTimeString()}</span>
                    <span>{s.http_method}</span>
                    <span className="break-all">{s.raw_path}</span>
                  </div>
                  <div className="text-fg-muted">
                    {s.client_ip} · {t("rejected.details.body_bytes", { n: fmtNum(s.body_bytes) })}
                    {s.request_id ? ` · ${s.request_id}` : ""}
                  </div>
                </div>
              ))}
              {(q.data?.samples?.length ?? 0) === 0 && !q.isLoading && (
                <div className="font-sans text-fg-muted">{t("rejected.details.no_samples")}</div>
              )}
            </div>
          </section>
        </div>

        <div className="flex shrink-0 items-center justify-between gap-2 border-t border-line px-5 py-3.5">
          <Button
            sm
            onClick={() => resolve.mutate()}
            disabled={resolve.isPending || !g || !!g.resolved_at}
          >
            <Check className="h-3.5 w-3.5" /> {t("rejected.details.resolve")}
          </Button>
          <Button sm variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending || !g}>
            <Trash2 className="h-3.5 w-3.5" /> {t("rejected.details.delete")}
          </Button>
        </div>
      </aside>
    </div>
  );
}
