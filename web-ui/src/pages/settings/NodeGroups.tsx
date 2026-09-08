import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, Pencil, Plus, Trash2 } from "lucide-react";

import { api, type NodeGroup } from "../../api/client";
import { Button, Input, Modal, SearchInput } from "../../components/ui";
import { useConfirm } from "../../lib/confirm";

type ListResp = { items: NodeGroup[] };

// MAX_ORDER совпадает с CHECK миграции 0042 и с доменной проверкой (§99.3).
const MAX_ORDER = 100000;

function nameValid(name: string): boolean {
  const n = name.trim();
  return n.length >= 1 && n.length <= 100;
}

// Справочник групп узлов (§99.8): управление из «Настройки → Группы» (manager).
// Порядок задаётся числом в диалоге и стрелками в таблице; удаление
// используемой группы заблокировано (тот же запрет на сервере и в СУБД).
export function NodeGroupsPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<NodeGroup | "new" | null>(null);

  // Один ключ на весь справочник — его же читает комбобокс формы узла, поэтому
  // инвалидация по префиксу обновляет и открытые формы.
  const invalidate = () => qc.invalidateQueries({ queryKey: ["node-groups"] });

  const list = useQuery({
    queryKey: ["node-groups", "settings", q],
    queryFn: () => api.get<ListResp>("/api/node-groups", { q, limit: 200 }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/node-groups/${id}`),
    onSuccess: invalidate,
  });
  const move = useMutation({
    mutationFn: (v: { id: string; direction: "up" | "down" }) =>
      api.post(`/api/node-groups/${v.id}/move`, { direction: v.direction }),
    onSuccess: invalidate,
  });

  const items = list.data?.items ?? [];
  // Стрелки двигают по СПИСКУ, а он сужается поиском: сдвиг относительно
  // невидимого соседа выглядел бы как «кнопка сработала, но ничего не
  // изменилось». Поэтому при активном поиске стрелки выключены.
  const reorderable = q.trim() === "";

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.groups.title")}</h2>
          <p className="mt-1 max-w-xl text-xs text-fg-muted">{t("settings.groups.subtitle")}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing("new")}>
          <Plus className="h-4 w-4" /> {t("settings.groups.add")}
        </Button>
      </header>

      <SearchInput
        className="max-w-xs"
        placeholder={t("settings.groups.search")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />

      {list.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {list.error && <div className="text-sm text-err">{t("common.error")}</div>}
      {list.data && items.length === 0 && (
        <div className="text-sm text-fg-muted">{t("settings.groups.empty")}</div>
      )}

      {items.length > 0 && (
        <table className="w-full text-[12.5px]">
          <thead className="text-fg-muted">
            <tr className="border-b border-line">
              <th className="w-[110px] px-2.5 py-2 text-left font-medium">
                {t("settings.groups.col.order")}
              </th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.groups.col.name")}</th>
              <th className="px-2.5 py-2 text-left font-medium">
                {t("settings.groups.col.description")}
              </th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.groups.col.usage")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.map((g, i) => (
              <tr key={g.id} className="border-b border-line hover:bg-bg-muted">
                <td className="px-2.5 py-2">
                  <span className="flex items-center gap-1.5">
                    <span className="w-6 font-mono text-xs">{g.sort_order}</span>
                    <button
                      onClick={() => move.mutate({ id: g.id, direction: "up" })}
                      disabled={!reorderable || i === 0 || move.isPending}
                      title={t("settings.groups.move_up")}
                      aria-label={t("settings.groups.move_up")}
                      className="rounded p-0.5 text-fg-muted hover:bg-bg-3 hover:text-fg disabled:cursor-not-allowed disabled:opacity-25"
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </button>
                    <button
                      onClick={() => move.mutate({ id: g.id, direction: "down" })}
                      disabled={!reorderable || i === items.length - 1 || move.isPending}
                      title={t("settings.groups.move_down")}
                      aria-label={t("settings.groups.move_down")}
                      className="rounded p-0.5 text-fg-muted hover:bg-bg-3 hover:text-fg disabled:cursor-not-allowed disabled:opacity-25"
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </button>
                  </span>
                </td>
                <td className="px-2.5 py-2">{g.name}</td>
                <td className="px-2.5 py-2 text-fg-muted">{g.description || "—"}</td>
                <td className="px-2.5 py-2 text-fg-muted">
                  {g.usage_count > 0
                    ? t("settings.groups.used", { count: g.usage_count })
                    : t("settings.groups.not_used")}
                </td>
                <td className="whitespace-nowrap px-2.5 py-2 text-right">
                  <button
                    onClick={() => setEditing(g)}
                    className="rounded p-1 text-fg-muted hover:bg-bg-3 hover:text-fg"
                    title={t("common.edit")}
                    aria-label={t("common.edit")}
                  >
                    <Pencil className="h-4 w-4" />
                  </button>
                  <button
                    onClick={async () => {
                      if (
                        await confirm({
                          title: t("common.delete"),
                          message: t("settings.groups.confirm_delete", { name: g.name }),
                          confirmLabel: t("common.delete"),
                          danger: true,
                        })
                      ) {
                        del.mutate(g.id);
                      }
                    }}
                    disabled={g.usage_count > 0}
                    title={g.usage_count > 0 ? t("settings.groups.in_use_tip") : t("common.delete")}
                    aria-label={t("common.delete")}
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
        <GroupDialog
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

function GroupDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: NodeGroup | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mode = initial ? "edit" : "create";
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [order, setOrder] = useState(String(initial?.sort_order ?? 0));
  const [error, setError] = useState<string | null>(null);

  const orderNum = Number(order);
  const orderOk = Number.isInteger(orderNum) && orderNum >= 0 && orderNum <= MAX_ORDER;
  const nameOk = nameValid(name);

  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), description, sort_order: orderNum };
      return mode === "create"
        ? api.post("/api/node-groups", body)
        : api.patch(`/api/node-groups/${initial!.id}`, body);
    },
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) =>
      setError(err?.response?.data?.error ?? t("common.error")),
  });

  return (
    <Modal
      title={mode === "create" ? t("settings.groups.dialog.new") : t("settings.groups.dialog.edit")}
      subtitle={t("settings.groups.dialog.subtitle")}
      onClose={onClose}
      className="max-w-md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            disabled={save.isPending || !nameOk || !orderOk}
            onClick={() => save.mutate()}
          >
            {mode === "create" ? t("settings.groups.dialog.create") : t("common.save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <div className="rounded border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">
            {error}
          </div>
        )}

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.groups.field.name")}</span>
          {/* Имя используемой группы РЕДАКТИРУЕТСЯ (в отличие от заголовков
              §24): узлы ссылаются по id, переименование ссылку не осиротит. */}
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="1С Обмен" />
          {name.trim() !== "" && !nameOk && (
            <span className="text-[11px] text-warn">{t("settings.groups.name_length")}</span>
          )}
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.groups.field.description")}</span>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.groups.field.order")}</span>
          <Input
            mono
            className="max-w-[120px]"
            value={order}
            onChange={(e) => setOrder(e.target.value)}
            inputMode="numeric"
          />
          <span className="block text-[11px] text-fg-subtle">
            {t("settings.groups.order_hint")}
          </span>
          {!orderOk && (
            <span className="text-[11px] text-warn">{t("settings.groups.order_range")}</span>
          )}
        </label>
      </div>
    </Modal>
  );
}
