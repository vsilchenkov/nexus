import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plus, Pencil, Trash2 } from "lucide-react";

import { api, type LogMaskEntry } from "../../api/client";
import { Button, Input, Modal, SearchInput } from "../../components/ui";
import { useConfirm } from "../../lib/confirm";

type ListResp = { items: LogMaskEntry[] };
// Превью всегда отвечает 200: valid=false = недописанный/кривой regex (нормальный
// ответ «что будет», не сетевая ошибка) — см. log_mask_handler.go.
type PreviewResp = { valid: boolean; result: string; matched: boolean };

// Справочник маскирования логов узлов (§95): admin-only. Список regex-шаблонов,
// по которым Sender скрывает секреты в колонках url/method/parameters лога ПЕРЕД
// записью в ClickHouse. Порядок применения — sort_order сверху вниз. Диалог
// добавления/правки — с живым превью замены.
export function LogMasksPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<LogMaskEntry | "new" | null>(null);

  const invalidate = () => qc.invalidateQueries({ queryKey: ["log-masks"] });

  const list = useQuery({
    queryKey: ["log-masks", q],
    queryFn: () => api.get<ListResp>("/api/log-masks", { q }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/log-masks/${id}`),
    onSuccess: invalidate,
  });

  // Тумблер «Вкл» в строке: PATCH с текущими значениями и флипнутым enabled —
  // без открытия модалки.
  const toggle = useMutation({
    mutationFn: (e: LogMaskEntry) =>
      api.patch(`/api/log-masks/${e.id}`, {
        pattern: e.pattern,
        replacement: e.replacement,
        description: e.description,
        enabled: !e.enabled,
        sort_order: e.sort_order,
      }),
    onSuccess: invalidate,
  });

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.log_masks.title")}</h2>
          <p className="mt-1 max-w-xl text-xs text-fg-muted">{t("settings.log_masks.subtitle")}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing("new")}>
          <Plus className="h-4 w-4" /> {t("settings.log_masks.add")}
        </Button>
      </header>

      <SearchInput
        className="max-w-xs"
        placeholder={t("settings.log_masks.search")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />

      {list.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {list.error && <div className="text-sm text-err">{t("common.error")}</div>}
      {list.data && list.data.items.length === 0 && (
        <div className="text-sm text-fg-muted">{t("settings.log_masks.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && (
        <table className="w-full text-[12.5px]">
          <thead className="text-fg-muted">
            <tr className="border-b border-line">
              <th className="w-8 px-2.5 py-2 text-left font-medium">#</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.log_masks.col.pattern")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.log_masks.col.replacement")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.log_masks.col.description")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.log_masks.col.enabled")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.items.map((m, i) => (
              <tr key={m.id} className="border-b border-line hover:bg-bg-muted">
                <td className="px-2.5 py-2 text-fg-subtle">{i + 1}</td>
                <td className="px-2.5 py-2 font-mono text-xs">{m.pattern}</td>
                <td className="px-2.5 py-2 font-mono text-xs text-fg-muted">{m.replacement}</td>
                <td className="px-2.5 py-2 text-fg-muted">{m.description || "—"}</td>
                <td className="px-2.5 py-2">
                  <input
                    type="checkbox"
                    checked={m.enabled}
                    disabled={toggle.isPending}
                    onChange={() => toggle.mutate(m)}
                    title={m.enabled ? t("settings.log_masks.enabled_on") : t("settings.log_masks.enabled_off")}
                    className="h-4 w-4 cursor-pointer accent-accent"
                  />
                </td>
                <td className="whitespace-nowrap px-2.5 py-2 text-right">
                  <button
                    onClick={() => setEditing(m)}
                    className="rounded p-1 text-fg-muted hover:bg-bg-3 hover:text-fg"
                    title={t("common.edit")}
                  >
                    <Pencil className="h-4 w-4" />
                  </button>
                  <button
                    onClick={async () => {
                      if (
                        await confirm({
                          title: t("common.delete"),
                          message: t("settings.log_masks.confirm_delete", { description: m.description || m.pattern }),
                          confirmLabel: t("common.delete"),
                          danger: true,
                        })
                      ) {
                        del.mutate(m.id);
                      }
                    }}
                    title={t("common.delete")}
                    className="rounded p-1 text-err hover:bg-bg-3"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <p className="text-[11px] text-fg-subtle">{t("settings.log_masks.order_hint")}</p>

      {editing && (
        <LogMaskDialog
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            invalidate();
          }}
        />
      )}
    </div>
  );
}

const DEFAULT_SAMPLE = "https://api.telegram.org/bot123456789:AAExYourBotTokenHere/getUpdates";

function LogMaskDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: LogMaskEntry | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mode = initial ? "edit" : "create";
  const [pattern, setPattern] = useState(initial?.pattern ?? "");
  const [replacement, setReplacement] = useState(initial?.replacement ?? "***");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [sortOrder, setSortOrder] = useState(String(initial?.sort_order ?? 0));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [sample, setSample] = useState(DEFAULT_SAMPLE);
  const [error, setError] = useState<string | null>(null);

  // Живое превью «как шаблон изменит образец» — backend применяет тот же regexp.
  const preview = useQuery({
    queryKey: ["log-mask-preview", pattern, replacement, sample],
    queryFn: () =>
      api.post<PreviewResp>("/api/log-masks/preview", { pattern, replacement, sample }),
    enabled: pattern.trim().length > 0,
    retry: false,
  });
  const previewInvalid = preview.isError || preview.data?.valid === false;

  const patternOk = pattern.trim().length > 0 && !previewInvalid;

  const save = useMutation({
    mutationFn: () => {
      const body = {
        pattern: pattern.trim(),
        replacement,
        description,
        enabled,
        sort_order: Number(sortOrder) || 0,
      };
      return mode === "create"
        ? api.post("/api/log-masks", body)
        : api.patch(`/api/log-masks/${initial!.id}`, body);
    },
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) =>
      setError(err?.response?.data?.error ?? t("common.error")),
  });

  return (
    <Modal
      title={mode === "create" ? t("settings.log_masks.dialog.new") : t("settings.log_masks.dialog.edit")}
      subtitle={t("settings.log_masks.dialog.subtitle")}
      onClose={onClose}
      className="max-w-xl"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={save.isPending || !patternOk} onClick={() => save.mutate()}>
            {mode === "create" ? t("settings.log_masks.dialog.create") : t("common.save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <div className="rounded border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
        )}

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.log_masks.field.pattern")}</span>
          <Input mono value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="(bot[0-9]+):[A-Za-z0-9_-]+" />
          {pattern.trim() !== "" && previewInvalid && (
            <span className="text-[11px] text-warn">{t("settings.log_masks.pattern_invalid")}</span>
          )}
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.log_masks.field.replacement")}</span>
          <Input mono value={replacement} onChange={(e) => setReplacement(e.target.value)} placeholder="$1:***" />
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.log_masks.field.description")}</span>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>

        <div className="flex items-center gap-6">
          <label className="flex items-center gap-2">
            <span className="text-xs text-fg-muted">{t("settings.log_masks.field.sort_order")}</span>
            <Input
              className="w-20"
              value={sortOrder}
              onChange={(e) => setSortOrder(e.target.value.replace(/[^0-9]/g, ""))}
              inputMode="numeric"
            />
          </label>
          <label className="flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
              className="h-4 w-4 accent-accent"
            />
            <span className="text-xs text-fg-muted">{t("settings.log_masks.field.enabled")}</span>
          </label>
        </div>

        <div className="space-y-1.5 border-t border-line pt-3">
          <span className="text-xs text-fg-muted">{t("settings.log_masks.field.sample")}</span>
          <Input mono value={sample} onChange={(e) => setSample(e.target.value)} />
          <div className="mt-1 text-xs">
            <span className="text-fg-subtle">{t("settings.log_masks.preview.result")}: </span>
            {previewInvalid ? (
              <span className="text-warn">{t("settings.log_masks.preview.invalid")}</span>
            ) : (
              <span className="break-all font-mono text-ok">{preview.data?.result ?? sample}</span>
            )}
          </div>
        </div>

        <p className="text-[11px] text-fg-subtle">{t("settings.log_masks.re2_hint")}</p>
      </div>
    </Modal>
  );
}
