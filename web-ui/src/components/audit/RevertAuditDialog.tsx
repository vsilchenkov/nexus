import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Info } from "lucide-react";

import { api } from "../../api/client";
import { Button } from "../ui";
import { Modal } from "../ui/Modal";
import {
  defaultSelection,
  formatAuditValue,
  isRevertible,
  type RevertField,
  type RevertPreview,
} from "./revert";

type ApiErrorBody = { error?: string; code?: string; fields?: string[] };
type ApiError = { response?: { status?: number; data?: ApiErrorBody } };

// RevertAuditDialog — отмена изменения узла из строки журнала аудита (§108.8).
// Предпросмотр показывает по каждому полю «сейчас → станет»; отмечены поля, где
// сейчас «Стало» и нет предупреждения о кредах. Применение шлёт updated_at узла
// из предпросмотра строкой как есть: узел изменился — 409, и предпросмотр
// перечитывается.
export function RevertAuditDialog({ entryId, onClose }: { entryId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  // Выбор привязан к версии предпросмотра: после 409 предпросмотр
  // перечитывается, и выбор возвращается к умолчанию нового состояния узла.
  const [picked, setPicked] = useState<{ version: number; names: Set<string> } | null>(null);

  const preview = useQuery({
    queryKey: ["audit-revert", entryId],
    queryFn: () => api.get<RevertPreview>(`/api/audit/revert/${entryId}`),
    refetchOnMount: "always",
    // Фокус окна перечитал бы предпросмотр и молча сбросил выбор полей.
    refetchOnWindowFocus: false,
  });
  const p = preview.data;
  const version = preview.dataUpdatedAt;
  const selected =
    picked && picked.version === version ? picked.names : defaultSelection(p?.fields ?? []);

  const toggle = (name: string) => {
    const next = new Set(selected);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setPicked({ version, names: next });
  };

  const errorText = (data?: ApiErrorBody) => {
    const fallback = data?.error ?? t("common.error");
    const msg = data?.code ? t(data.code, { defaultValue: fallback }) : fallback;
    return data?.fields?.length ? `${msg}: ${data.fields.join(", ")}` : msg;
  };

  const apply = useMutation({
    mutationFn: () =>
      api.post(`/api/audit/revert/${entryId}`, {
        fields: [...selected],
        expected_updated_at: p?.node?.updated_at,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["audit"] });
      qc.invalidateQueries({ queryKey: ["audit-count"] });
      qc.invalidateQueries({ queryKey: ["nodes"] });
      if (p?.node) qc.invalidateQueries({ queryKey: ["node", p.node.id] });
      onClose();
    },
    onError: (err: ApiError) => {
      const r = err?.response;
      if (r?.status === 409 && r.data?.code === "audit.revert.conflict") {
        setError(t("audit.revert.conflict_refreshed"));
        void preview.refetch();
        return;
      }
      setError(errorText(r?.data));
    },
  });

  // Пока предпросмотр перечитывается (после 409), версия узла в нём старая —
  // отправка дала бы тот же 409.
  const canApply = !!p?.node && selected.size > 0 && !apply.isPending && !preview.isFetching;

  return (
    <Modal
      title={t("audit.revert.title")}
      subtitle={
        p
          ? `${p.entry.action} · ${p.entry.user_login} · ${new Date(p.entry.created_at).toLocaleString()}`
          : undefined
      }
      onClose={onClose}
      className="max-w-2xl"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="primary" disabled={!canApply} onClick={() => apply.mutate()}>
            {t("audit.revert.submit", { count: selected.size })}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {preview.isPending && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
        {preview.isError && (
          <Notice tone="err">{errorText((preview.error as ApiError)?.response?.data)}</Notice>
        )}
        {p && <PreviewBody preview={p} selected={selected} onToggle={toggle} />}
        {error && <Notice tone="err">{error}</Notice>}
      </div>
    </Modal>
  );
}

function PreviewBody({
  preview,
  selected,
  onToggle,
}: {
  preview: RevertPreview;
  selected: Set<string>;
  onToggle: (name: string) => void;
}) {
  const { t } = useTranslation();
  const { node, dry_run: dry } = preview;
  const dryText =
    dry.code === "audit.revert.not_held"
      ? t("audit.revert.dry_not_held", { fields: dry.not_held.join(", ") })
      : t(dry.code, { defaultValue: dry.error ?? dry.code });
  return (
    <div className="space-y-3 text-[12.5px]">
      {node ? (
        <div>
          {t("audit.revert.node")}: <span className="font-mono">{node.path}</span>
        </div>
      ) : (
        <Notice tone="warn">{t("audit.revert.gone")}</Notice>
      )}
      {preview.moved && <Notice tone="warn">{t("audit.revert.moved")}</Notice>}
      <FieldsTable fields={preview.fields} selected={selected} onToggle={onToggle} gone={!node} />
      {node && dry.code && <Notice tone="warn">{t("audit.revert.dry_run", { message: dryText })}</Notice>}
      {node && (
        <div className="flex items-center gap-1.5 text-xs text-fg-muted">
          <Info className="h-3.5 w-3.5" />
          {t("audit.revert.note")}
        </div>
      )}
    </div>
  );
}

function FieldsTable({
  fields,
  selected,
  onToggle,
  gone,
}: {
  fields: RevertField[];
  selected: Set<string>;
  onToggle: (name: string) => void;
  gone: boolean;
}) {
  const { t } = useTranslation();
  if (fields.length === 0) {
    return <div className="text-fg-muted">{t("audit.revert.nothing")}</div>;
  }
  return (
    <table className="w-full border-collapse">
      <thead>
        <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
          <th className="w-px pb-1.5 pr-2" />
          <th className="pb-1.5 pr-2 font-medium">{t("audit.revert.col_field")}</th>
          <th className="pb-1.5 pr-2 font-medium">{t("audit.revert.col_now")}</th>
          <th className="pb-1.5 font-medium">{t("audit.revert.col_becomes")}</th>
        </tr>
      </thead>
      <tbody>
        {fields.map((f) => (
          <FieldRow
            key={f.name}
            field={f}
            checked={selected.has(f.name)}
            onToggle={() => onToggle(f.name)}
            selectable={!gone && isRevertible(f)}
          />
        ))}
      </tbody>
    </table>
  );
}

function FieldRow({
  field: f,
  checked,
  onToggle,
  selectable,
}: {
  field: RevertField;
  checked: boolean;
  onToggle: () => void;
  selectable: boolean;
}) {
  const { t } = useTranslation();
  return (
    <tr className="border-b border-line align-top last:border-0">
      <td className="py-1.5 pr-2">
        {selectable ? (
          <input type="checkbox" checked={checked} onChange={onToggle} aria-label={f.name} />
        ) : (
          <span className="text-fg-muted">—</span>
        )}
      </td>
      <td className="whitespace-nowrap py-1.5 pr-2 font-mono">{f.name}</td>
      {f.status === "secret" ? (
        <td colSpan={2} className="py-1.5 italic text-fg-muted">
          {t("audit.revert.status.secret")}
        </td>
      ) : (
        <>
          <td className="break-all py-1.5 pr-2 font-mono">
            {formatAuditValue(f.current)}
            {f.status === "overwritten" && <Warn text={t("audit.revert.overwritten")} />}
            {f.warning === "credentials" && <Warn text={t("audit.revert.credentials")} />}
          </td>
          <td className="break-all py-1.5 font-mono">
            {isRevertible(f) ? (
              <>→ {formatAuditValue(f.before)}</>
            ) : (
              <span className="font-sans italic text-fg-muted">{t(`audit.revert.status.${f.status}`)}</span>
            )}
          </td>
        </>
      )}
    </tr>
  );
}

function Warn({ text }: { text: string }) {
  return (
    <span className="ml-1.5 inline-flex items-center gap-1 font-sans text-[11px] text-warn">
      <AlertTriangle className="h-3 w-3" />
      {text}
    </span>
  );
}

function Notice({ tone, children }: { tone: "warn" | "err"; children: ReactNode }) {
  const cls = tone === "err" ? "bg-err/10 text-err" : "border border-warn/30 bg-warn/10 text-warn";
  return <div className={`rounded-md px-3 py-2 text-sm ${cls}`}>{children}</div>;
}
