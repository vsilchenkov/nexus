import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ExternalLink, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";

import { api } from "../../api/client";
import { useConfirm } from "../../lib/confirm";
import { Button, ErrorAlert, Input, Modal, Pill } from "../../components/ui";

// §73: реестр соседних инстансов Nexus. «Инстанс» — самостоятельное
// развёртывание шины со своими PostgreSQL/Redis/Kafka (§70), не путать с
// «узлом» (маршрут шины). Раздел даёт только список и быстрый переход:
// данными инстансы не обмениваются.

export const INSTANCES_KEY = ["instances"] as const;

type InstanceStatus = "unknown" | "active" | "degraded" | "unreachable" | "error";

type PeerInstance = {
  id: string;
  title: string;
  base_url: string;
  comment: string;
  last_status: InstanceStatus;
  last_version: string;
  last_instance_id: string;
  last_latency_ms: number | null;
  last_error: string;
  last_checked_at: string | null;
};

type ListResp = { items: PeerInstance[] };

type ProbeResp = {
  status: InstanceStatus;
  version: string;
  instance_id: string;
  latency_ms: number | null;
  error: string;
};

const statusTone: Record<InstanceStatus, "ok" | "err" | "warn" | "muted"> = {
  active: "ok",
  degraded: "warn",
  unreachable: "err",
  error: "err",
  unknown: "muted",
};

export function InstancesPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();
  const [editing, setEditing] = useState<PeerInstance | "new" | null>(null);

  const list = useQuery({
    queryKey: INSTANCES_KEY,
    queryFn: () => api.get<ListResp>("/api/instances"),
  });

  // Проверка всех инстансов. Ответ кладётся в кеш напрямую (setQueryData), а не
  // через инвалидацию: ручка возвращает тот же список уже со свежими статусами,
  // и повторный GET показал бы ровно эти же данные лишним запросом.
  const checkAll = useMutation({
    mutationFn: () => api.post<ListResp>("/api/instances/check"),
    onSuccess: (data) => qc.setQueryData(INSTANCES_KEY, data),
  });

  const checkOne = useMutation({
    mutationFn: (id: string) => api.post<PeerInstance>(`/api/instances/${id}/check`),
    onSuccess: (updated) =>
      qc.setQueryData<ListResp>(INSTANCES_KEY, (prev) =>
        prev ? { items: prev.items.map((p) => (p.id === updated.id ? updated : p)) } : prev,
      ),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/instances/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: INSTANCES_KEY }),
  });

  // Опрос при открытии вкладки — ровно один раз на монтирование. Без guard'а
  // любой рефетч списка (возврат фокуса на вкладку браузера) тянул бы за собой
  // новые сетевые пробы всех соседей.
  const probedRef = useRef(false);
  const items = list.data?.items;
  const checkAllMutate = checkAll.mutate;
  useEffect(() => {
    if (probedRef.current || !items || items.length === 0) return;
    probedRef.current = true;
    checkAllMutate();
  }, [items, checkAllMutate]);

  const busy = checkAll.isPending;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.instances.title")}</h2>
          <p className="mt-1 max-w-xl text-xs text-fg-muted">{t("settings.instances.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" disabled={busy || !items?.length} onClick={() => checkAll.mutate()}>
            <RefreshCw className={busy ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
            {t("settings.instances.check_all")}
          </Button>
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus className="h-4 w-4" /> {t("settings.instances.add")}
          </Button>
        </div>
      </header>

      {list.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {list.error && <ErrorAlert />}
      {items && items.length === 0 && (
        <div className="rounded-md border border-dashed border-line px-4 py-8 text-center text-sm text-fg-muted">
          {t("settings.instances.empty")}
        </div>
      )}

      {items && items.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-[12.5px]">
            <thead className="text-fg-muted">
              <tr className="border-b border-line">
                <th className="px-2.5 py-2 text-left font-medium">{t("settings.instances.col.title")}</th>
                <th className="px-2.5 py-2 text-left font-medium">{t("settings.instances.col.address")}</th>
                <th className="px-2.5 py-2 text-left font-medium">{t("settings.instances.col.code")}</th>
                <th className="px-2.5 py-2 text-left font-medium">{t("settings.instances.col.version")}</th>
                <th className="px-2.5 py-2 text-left font-medium">{t("settings.instances.col.status")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {items.map((p) => (
                <InstanceRow
                  key={p.id}
                  item={p}
                  checking={checkOne.isPending && checkOne.variables === p.id}
                  onCheck={() => checkOne.mutate(p.id)}
                  onEdit={() => setEditing(p)}
                  onDelete={async () => {
                    if (
                      await confirm({
                        title: t("settings.instances.delete"),
                        message: t("settings.instances.confirm_delete", { title: p.title }),
                        confirmLabel: t("common.delete"),
                        danger: true,
                      })
                    ) {
                      del.mutate(p.id);
                    }
                  }}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <InstanceDialog
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: INSTANCES_KEY });
          }}
        />
      )}
    </div>
  );
}

function InstanceRow({
  item,
  checking,
  onCheck,
  onEdit,
  onDelete,
}: {
  item: PeerInstance;
  checking: boolean;
  onCheck: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();

  // Вторая строка ячейки статуса: время ответа и момент проверки либо причина
  // отказа. Без неё «Активен» невозможно отличить от «был активен вчера».
  const detail =
    item.last_status === "unknown"
      ? null
      : item.last_error
        ? item.last_error
        : [
            item.last_latency_ms != null ? `${item.last_latency_ms} ${t("settings.instances.ms")}` : null,
            item.last_checked_at ? new Date(item.last_checked_at).toLocaleTimeString() : null,
          ]
            .filter(Boolean)
            .join(" · ");

  return (
    <tr className="border-b border-line last:border-0 hover:bg-bg-muted">
      <td className="px-2.5 py-2.5">
        <div className="font-medium">{item.title}</div>
        {item.comment && <div className="text-[11px] text-fg-muted">{item.comment}</div>}
      </td>
      <td className="px-2.5 py-2.5">
        {/* rel="noopener noreferrer" обязателен: без noopener открытая вкладка
            получает window.opener и может подменить эту страницу. */}
        <a
          href={item.base_url}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 font-mono text-xs text-accent hover:underline"
        >
          {item.base_url}
          <ExternalLink className="h-3 w-3 shrink-0" />
        </a>
      </td>
      <td className="px-2.5 py-2.5 font-mono text-xs uppercase text-fg-muted">
        {item.last_instance_id || "—"}
      </td>
      <td className="px-2.5 py-2.5 font-mono text-xs">{item.last_version || "—"}</td>
      <td className="px-2.5 py-2.5">
        <Pill tone={statusTone[item.last_status]}>{t(`settings.instances.status.${item.last_status}`)}</Pill>
        {detail && <div className="mt-0.5 text-[11px] text-fg-muted">{detail}</div>}
      </td>
      <td className="whitespace-nowrap px-2.5 py-2 text-right">
        <button
          onClick={onCheck}
          disabled={checking}
          title={t("settings.instances.check_one")}
          className="rounded p-1 text-fg-muted hover:bg-bg-3 hover:text-fg disabled:opacity-30"
        >
          <RefreshCw className={checking ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
        </button>
        <button
          onClick={onEdit}
          title={t("common.edit")}
          className="rounded p-1 text-fg-muted hover:bg-bg-3 hover:text-fg"
        >
          <Pencil className="h-4 w-4" />
        </button>
        <button
          onClick={onDelete}
          title={t("common.delete")}
          className="rounded p-1 text-err hover:bg-bg-3"
        >
          <Trash2 className="h-4 w-4" />
        </button>
      </td>
    </tr>
  );
}

function InstanceDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: PeerInstance | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mode = initial ? "edit" : "create";
  const [title, setTitle] = useState(initial?.title ?? "");
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? "");
  const [comment, setComment] = useState(initial?.comment ?? "");
  const [error, setError] = useState<string | null>(null);
  const [probe, setProbe] = useState<ProbeResp | null>(null);

  const errorText = (err: unknown) =>
    (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? t("common.error");

  // Проверка адреса ДО сохранения: завести заведомо нерабочий инстанс вслепую
  // не должно быть единственным способом узнать, отвечает ли он.
  const check = useMutation({
    mutationFn: () => api.post<ProbeResp>("/api/instances/probe", { base_url: baseURL.trim() }),
    onSuccess: (res) => {
      setProbe(res);
      setError(null);
      // Название необязательное: если его не ввели, подставляем код инстанса
      // соседа — он и есть его самоназвание (§70.1).
      if (!title.trim() && res.instance_id) setTitle(res.instance_id.toUpperCase());
    },
    onError: (err) => {
      setProbe(null);
      setError(errorText(err));
    },
  });

  const save = useMutation({
    mutationFn: () => {
      const body = { title: title.trim(), base_url: baseURL.trim(), comment: comment.trim() };
      return mode === "create"
        ? api.post("/api/instances", body)
        : api.patch(`/api/instances/${initial!.id}`, body);
    },
    onSuccess: () => onSaved(),
    onError: (err) => setError(errorText(err)),
  });

  const canSubmit = title.trim() !== "" && baseURL.trim() !== "";

  return (
    <Modal
      title={mode === "create" ? t("settings.instances.dialog.new") : t("settings.instances.dialog.edit")}
      subtitle={t("settings.instances.dialog.subtitle")}
      onClose={onClose}
      className="max-w-md"
      footer={
        <>
          <Button
            variant="ghost"
            disabled={check.isPending || baseURL.trim() === ""}
            onClick={() => check.mutate()}
          >
            {t("settings.instances.dialog.check")}
          </Button>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={save.isPending || !canSubmit} onClick={() => save.mutate()}>
            {mode === "create" ? t("settings.instances.dialog.create") : t("common.save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && <ErrorAlert>{error}</ErrorAlert>}

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.instances.field.address")}</span>
          <Input
            mono
            value={baseURL}
            onChange={(e) => {
              setBaseURL(e.target.value);
              setProbe(null);
            }}
            placeholder="https://nexus-kz.example.ru"
          />
          <span className="text-[11px] text-fg-muted">{t("settings.instances.field.address_hint")}</span>
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.instances.field.title")}</span>
          <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Казахстан" />
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.instances.field.comment")}</span>
          <Input value={comment} onChange={(e) => setComment(e.target.value)} />
        </label>

        {probe && (
          <div className="rounded-md border border-line bg-bg-muted px-3 py-2 text-xs">
            <Pill tone={statusTone[probe.status]}>{t(`settings.instances.status.${probe.status}`)}</Pill>
            <div className="mt-1.5 space-y-0.5 text-fg-muted">
              {probe.version && (
                <div>
                  {t("settings.instances.col.version")}: <span className="font-mono">{probe.version}</span>
                </div>
              )}
              {probe.instance_id && (
                <div>
                  {t("settings.instances.col.code")}:{" "}
                  <span className="font-mono uppercase">{probe.instance_id}</span>
                </div>
              )}
              {probe.latency_ms != null && (
                <div>
                  {probe.latency_ms} {t("settings.instances.ms")}
                </div>
              )}
              {probe.error && <div className="text-err">{probe.error}</div>}
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
