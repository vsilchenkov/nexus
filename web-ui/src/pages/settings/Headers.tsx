import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plus, Pencil, Trash2 } from "lucide-react";

import { api, type HeaderCatalogEntry } from "../../api/client";
import { Button, Input, Modal, SearchInput } from "../../components/ui";
import { useConfirm } from "../../lib/confirm";

type ListResp = { items: HeaderCatalogEntry[] };

// HEADER_NAME_RE — RFC 7230 token (§24.2): та же проверка, что на сервере, чтобы
// не показывать «Сохранить» для заведомо невалидного имени.
const HEADER_NAME_RE = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;

function nameValid(name: string): boolean {
  return name.length >= 1 && name.length <= 100 && HEADER_NAME_RE.test(name);
}

// Справочник HTTP-заголовков (§24): страница управления в Настройках — список
// всех записей, добавление, переименование и удаление. Узлы ссылаются на
// заголовки по имени (не по id), поэтому переименование/удаление используемого
// заголовка блокируется сервером (usage_count>0 → 409); кнопки заблокированы в
// UI, поле имени недоступно при редактировании используемого заголовка.
export function HeadersPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<HeaderCatalogEntry | "new" | null>(null);

  // Мутации трогают тот же справочник, что combobox формы узла (queryKey
  // ["headers"]) — инвалидируем оба ключа, чтобы автодополнение не устаревало.
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["headers-catalog"] });
    qc.invalidateQueries({ queryKey: ["headers"] });
  };

  const list = useQuery({
    queryKey: ["headers-catalog", q],
    queryFn: () => api.get<ListResp>("/api/headers", { q, limit: 200 }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/headers/${id}`),
    onSuccess: invalidate,
  });

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.headers.title")}</h2>
          <p className="mt-1 max-w-xl text-xs text-fg-muted">{t("settings.headers.subtitle")}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing("new")}>
          <Plus className="h-4 w-4" /> {t("settings.headers.add")}
        </Button>
      </header>

      <SearchInput
        className="max-w-xs"
        placeholder={t("settings.headers.search")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />

      {list.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {list.error && <div className="text-sm text-err">{t("common.error")}</div>}
      {list.data && list.data.items.length === 0 && (
        <div className="text-sm text-fg-muted">{t("settings.headers.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && (
        <table className="w-full text-[12.5px]">
          <thead className="text-fg-muted">
            <tr className="border-b border-line">
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.headers.col.name")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.headers.col.description")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.headers.col.usage")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.items.map((h) => (
              <tr key={h.id} className="border-b border-line hover:bg-bg-muted">
                <td className="px-2.5 py-2 font-mono text-xs">{h.name}</td>
                <td className="px-2.5 py-2 text-fg-muted">{h.description || "—"}</td>
                <td className="px-2.5 py-2 text-fg-muted">
                  {h.usage_count > 0
                    ? t("settings.headers.used", { count: h.usage_count })
                    : t("settings.headers.not_used")}
                </td>
                <td className="whitespace-nowrap px-2.5 py-2 text-right">
                  <button
                    onClick={() => setEditing(h)}
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
                          message: t("settings.headers.confirm_delete", { name: h.name }),
                          confirmLabel: t("common.delete"),
                          danger: true,
                        })
                      ) {
                        del.mutate(h.id);
                      }
                    }}
                    disabled={h.usage_count > 0}
                    title={h.usage_count > 0 ? t("settings.headers.in_use_tip") : t("common.delete")}
                    className="rounded p-1 text-err hover:bg-bg-3 disabled:cursor-not-allowed disabled:opacity-30"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {editing && (
        <HeaderDialog
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

function HeaderDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: HeaderCatalogEntry | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mode = initial ? "edit" : "create";
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [error, setError] = useState<string | null>(null);

  // Имя используемого заголовка переименовать нельзя (узлы ссылаются по имени) —
  // поле недоступно, серверный guard (409) — второй уровень защиты.
  const nameLocked = mode === "edit" && (initial?.usage_count ?? 0) > 0;
  const nameOk = nameValid(name.trim());

  const save = useMutation({
    mutationFn: () =>
      mode === "create"
        ? api.post("/api/headers", { name: name.trim(), description })
        : api.patch(`/api/headers/${initial!.id}`, { name: name.trim(), description }),
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) =>
      setError(err?.response?.data?.error ?? t("common.error")),
  });

  return (
    <Modal
      title={mode === "create" ? t("settings.headers.dialog.new") : t("settings.headers.dialog.edit")}
      subtitle={t("settings.headers.dialog.subtitle")}
      onClose={onClose}
      className="max-w-md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            disabled={save.isPending || !nameOk}
            onClick={() => save.mutate()}
          >
            {mode === "create" ? t("settings.headers.dialog.create") : t("common.save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <div className="rounded border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
        )}

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.headers.field.name")}</span>
          <Input
            mono
            value={name}
            disabled={nameLocked}
            onChange={(e) => setName(e.target.value)}
            placeholder="X-Request-Id"
          />
          {nameLocked && <span className="text-[11px] text-warn">{t("settings.headers.in_use_tip")}</span>}
          {!nameLocked && name.trim() !== "" && !nameOk && (
            <span className="text-[11px] text-warn">{t("settings.headers.name_format")}</span>
          )}
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.headers.field.description")}</span>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>
      </div>
    </Modal>
  );
}
