import { useEffect, useRef } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ExternalLink, Trash2, X } from "lucide-react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import { fmtNum, fmtSize } from "../../lib/format";
import { reasonLabelKey, type RejectedDetails } from "../../lib/rejected";
import { Button, CopyButton, ErrorAlert } from "../ui";

type Props = {
  id: string;
  onClose: () => void;
  // onViewed — группа помечена просмотренной при открытии карточки (§94.7):
  // счётчики надо перечитать, а строку списка — приглушить на месте.
  onViewed: (id: string, at: string) => void;
  // onDeleted — группы больше нет, список перечитывается целиком.
  onDeleted: () => void;
};

// RejectedDrawer — карточка группы отказов (§94.7): клиенты, последние
// запросы, подсказка похожего узла и удаление группы.
//
// Панель НЕ модальная: подложки нет, таблица под ней остаётся кликабельной, и
// клик по другой строке переключает карточку без закрытия текущей. Закрывается
// Esc и крестиком.
//
// Открытие карточки = просмотр: непросмотренная группа помечается сама, ручной
// кнопки нет. Иначе счётчик непросмотренных приходилось бы гасить вторым
// действием уже после того, как человек всё посмотрел.
export function RejectedDrawer({ id, onClose, onViewed, onDeleted }: Props) {
  const { t } = useTranslation();
  // Единицы размера — локализованные (§42-доп), тот же приём, что в логах узла.
  const sizeUnits = t("logs.size_units").split("|");

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const q = useQuery({
    queryKey: ["rejected", "details", id],
    queryFn: () => api.get<RejectedDetails>(`/api/rejected/${id}`),
  });

  const remove = useMutation({
    mutationFn: () => api.del(`/api/rejected/${id}`),
    onSuccess: () => {
      onDeleted();
      onClose();
    },
  });

  const g = q.data?.group;
  const reasonKey = g ? reasonLabelKey(g.reason) : "";

  // Отметка «просмотрено» — ровно одна на открытие группы. markedRef держит id
  // уже помеченной: без него перерисовка панели слала бы запрос повторно, а
  // переключение между строками — на каждый рендер.
  const markedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!g || g.resolved_at || markedRef.current === g.id) return;
    markedRef.current = g.id;
    const at = new Date().toISOString();
    void api.post(`/api/rejected/${g.id}/resolve`, {}).then(() => onViewed(g.id, at));
  }, [g, onViewed]);

  // Ни подложки, ни перехвата кликов: панель занимает правый край, а список под
  // ней остаётся рабочим — клик по другой строке переключает карточку (§94.7).
  // z-40 ниже модалок приложения (z-50), чтобы диалог подтверждения ложился
  // поверх панели, а не под неё.
  return (
    <aside className="fixed bottom-0 right-0 top-0 z-40 flex w-full max-w-xl flex-col overflow-hidden border-l border-line-strong bg-app shadow-2xl">
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
                  <div className="flex flex-wrap items-center gap-x-1.5 text-fg-muted">
                    <span>{s.client_ip}</span>
                    <span aria-hidden>·</span>
                    {/* Тела в журнале нет НИКОГДА — хранится только его размер
                        (§94.9). При нуле пишем «без тела»: «тело —» читалось
                        как сбой, хотя означало «тела не было». Размер —
                        размерным форматтером, иначе fmtNum давал «2.00M Б». */}
                    <span>
                      {s.body_bytes > 0
                        ? t("rejected.details.body_bytes", { n: fmtSize(s.body_bytes, sizeUnits) })
                        : t("rejected.details.no_body")}
                    </span>
                    {s.request_id && (
                      <>
                        <span aria-hidden>·</span>
                        {/* request_id (§30) — сквозной идентификатор запроса: по
                            нему он ищется во вкладке «Сервисы» и в Sentry. Без
                            подписи голый UUID читался как «что-то про тело». */}
                        <span>{t("rejected.details.request_id")}</span>
                        <span className="select-all">{s.request_id}</span>
                        <CopyButton value={s.request_id} />
                      </>
                    )}
                  </div>
                </div>
              ))}
              {(q.data?.samples?.length ?? 0) === 0 && !q.isLoading && (
                <div className="font-sans text-fg-muted">{t("rejected.details.no_samples")}</div>
              )}
            </div>
          </section>
        </div>

      {/* Кнопки «пометить» здесь нет: отметка ставится самим открытием
          карточки. Осталось только удаление — оно необратимо и должно быть
          явным действием. */}
      <div className="flex shrink-0 items-center justify-end gap-2 border-t border-line px-5 py-3.5">
        <Button sm variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending || !g}>
          <Trash2 className="h-3.5 w-3.5" /> {t("rejected.details.delete")}
        </Button>
      </div>
    </aside>
  );
}
