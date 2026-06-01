import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plus, Pencil, Trash2, Check, X } from "lucide-react";

import { api, type HostAllowlistEntry, type HostKind } from "../../api/client";
import { Button, Chip, Input, Modal, Seg, type SegOption } from "../../components/ui";

type ListResp = { items: HostAllowlistEntry[] };
// Превью всегда отвечает 200: valid=false означает недописанный/кривой паттерн
// (нормальный ответ «что будет», а не сетевая ошибка) — см. host_allowlist_handler.go.
type PreviewResp = { valid: boolean; reason?: string; allowed: string[]; blocked: string[] };

const kindTone: Record<HostKind, "success" | "info" | "warning"> = {
  exact: "success",
  wildcard: "info",
  regex: "warning",
};

// Каталог разрешённых хостов (§23). Таблица с фильтром по типу и поиском,
// диалог создания/редактирования с живым превью «Разрешит / Заблокирует».
export function AllowedHostsPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [kind, setKind] = useState<"" | HostKind>("");
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<HostAllowlistEntry | "new" | null>(null);

  const list = useQuery({
    queryKey: ["allowed-hosts", kind, q],
    queryFn: () => api.get<ListResp>("/api/allowed-hosts", { kind, q, limit: 200 }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/allowed-hosts/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["allowed-hosts"] }),
  });

  const segOptions: SegOption<"" | HostKind>[] = [
    { value: "", label: t("settings.allowed_hosts.filter_all") },
    { value: "exact", label: "exact" },
    { value: "wildcard", label: "wildcard" },
    { value: "regex", label: "regex" },
  ];

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.allowed_hosts.title")}</h2>
          <p className="mt-1 max-w-xl text-xs text-fg-muted">
            {t("settings.allowed_hosts.subtitle")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setEditing("new")}>
          <Plus className="h-4 w-4" /> {t("settings.allowed_hosts.add")}
        </Button>
      </header>

      <div className="flex flex-wrap items-center gap-2">
        <Seg value={kind} options={segOptions} onChange={setKind} />
        <Input
          className="max-w-xs flex-1"
          placeholder={t("settings.allowed_hosts.search")}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
      </div>

      {list.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {list.error && <div className="text-sm text-err">{t("common.error")}</div>}
      {list.data && list.data.items.length === 0 && (
        <div className="text-sm text-fg-muted">{t("settings.allowed_hosts.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && (
        <table className="w-full text-[12.5px]">
          <thead className="text-fg-muted">
            <tr className="border-b border-line">
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.allowed_hosts.col.pattern")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.allowed_hosts.col.kind")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.allowed_hosts.col.description")}</th>
              <th className="px-2.5 py-2 text-left font-medium">{t("settings.allowed_hosts.col.usage")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.items.map((h) => (
              <tr key={h.id} className="border-b border-line hover:bg-bg-muted">
                <td className="px-2.5 py-2 font-mono text-xs">{h.pattern}</td>
                <td className="px-2.5 py-2">
                  <Chip tone={kindTone[h.kind]}>{h.kind}</Chip>
                </td>
                <td className="px-2.5 py-2 text-fg-muted">{h.description || "—"}</td>
                <td className="px-2.5 py-2 text-fg-muted">
                  {h.usage_count > 0
                    ? t("settings.allowed_hosts.used", { count: h.usage_count })
                    : t("settings.allowed_hosts.not_used")}
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
                    onClick={() => {
                      if (confirm(t("settings.allowed_hosts.confirm_delete", { pattern: h.pattern }))) {
                        del.mutate(h.id);
                      }
                    }}
                    disabled={h.usage_count > 0}
                    title={h.usage_count > 0 ? t("settings.allowed_hosts.in_use_tip") : t("common.delete")}
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
        <HostDialog
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["allowed-hosts"] });
          }}
        />
      )}
    </div>
  );
}

const DEFAULT_TEST_URLS = [
  "https://api.partner.com/v1/hook",
  "https://x.api.partner.com/cb",
  "https://partner.com/hook",
  "http://169.254.169.254/",
  "http://localhost:6379/",
].join("\n");

function HostDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: HostAllowlistEntry | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mode = initial ? "edit" : "create";
  const [kind, setKind] = useState<HostKind>(initial?.kind ?? "wildcard");
  const [pattern, setPattern] = useState(initial?.pattern ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [testUrls, setTestUrls] = useState(DEFAULT_TEST_URLS);
  const [error, setError] = useState<string | null>(null);

  const urls = useMemo(
    () => testUrls.split("\n").map((s) => s.trim()).filter(Boolean),
    [testUrls],
  );

  // Живое превью «Разрешит/Заблокирует» — backend применяет тот же матчер.
  const preview = useQuery({
    queryKey: ["host-preview", kind, pattern, urls],
    queryFn: () =>
      api.post<PreviewResp>("/api/allowed-hosts/preview", { kind, pattern, test_urls: urls }),
    enabled: pattern.trim().length > 0,
    retry: false,
  });
  // Невалидный паттерн — это valid:false в успешном ответе (200), либо реальная
  // сетевая ошибка превью. В обоих случаях колонки показываем как «invalid».
  const previewInvalid = preview.isError || preview.data?.valid === false;

  const save = useMutation({
    mutationFn: () => {
      if (mode === "create") {
        return api.post("/api/allowed-hosts", { pattern, kind, description });
      }
      // Маршрут редактирования — PATCH /allowed-hosts/:id (см. routes.go).
      return api.patch(`/api/allowed-hosts/${initial!.id}`, { pattern, kind, description });
    },
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) =>
      setError(err?.response?.data?.error ?? t("common.error")),
  });

  const kindOptions: { value: HostKind; title: string; example: string }[] = [
    { value: "exact", title: t("settings.allowed_hosts.kind.exact"), example: "api.partner.com" },
    { value: "wildcard", title: t("settings.allowed_hosts.kind.wildcard"), example: "*.partner.com" },
    { value: "regex", title: t("settings.allowed_hosts.kind.regex"), example: "^api-\\d+\\.io$" },
  ];

  return (
    <Modal
      title={mode === "create" ? t("settings.allowed_hosts.dialog.new") : t("settings.allowed_hosts.dialog.edit")}
      subtitle={t("settings.allowed_hosts.dialog.subtitle")}
      onClose={onClose}
      className="max-w-xl"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="primary" disabled={save.isPending || pattern.trim() === ""} onClick={() => save.mutate()}>
            {mode === "create" ? t("settings.allowed_hosts.dialog.create") : t("common.save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <div className="rounded border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
        )}

        <div>
          <div className="mb-1.5 text-xs text-fg-muted">{t("settings.allowed_hosts.field.kind")}</div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
            {kindOptions.map((o) => (
              <button
                key={o.value}
                type="button"
                onClick={() => setKind(o.value)}
                className={
                  "rounded-md border px-3 py-2.5 text-center " +
                  (kind === o.value ? "border-accent bg-accent/10" : "border-line hover:bg-bg-muted")
                }
              >
                <div className={"text-xs font-medium " + (kind === o.value ? "text-accent" : "")}>{o.title}</div>
                <div className="mt-0.5 font-mono text-[10px] text-fg-subtle">{o.example}</div>
              </button>
            ))}
          </div>
        </div>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.allowed_hosts.field.pattern")}</span>
          <Input mono value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="*.partner.com" />
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.allowed_hosts.field.description")}</span>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>

        <label className="block space-y-1.5">
          <span className="text-xs text-fg-muted">{t("settings.allowed_hosts.field.test_urls")}</span>
          <textarea
            value={testUrls}
            onChange={(e) => setTestUrls(e.target.value)}
            rows={4}
            className="w-full rounded-md border border-line bg-app px-3 py-2 font-mono text-[11px] text-fg outline-none focus:border-accent"
          />
        </label>

        <div className="grid grid-cols-1 gap-3 border-t border-line pt-3 sm:grid-cols-2">
          <PreviewCol
            tone="ok"
            title={t("settings.allowed_hosts.preview.allow")}
            icon={<Check className="h-3.5 w-3.5" />}
            urls={preview.data?.allowed ?? []}
            invalid={previewInvalid}
          />
          <PreviewCol
            tone="err"
            title={t("settings.allowed_hosts.preview.block")}
            icon={<X className="h-3.5 w-3.5" />}
            urls={preview.data?.blocked ?? []}
            invalid={previewInvalid}
          />
        </div>
        {previewInvalid && (
          <div className="text-[11px] text-warn">{t("settings.allowed_hosts.preview.invalid")}</div>
        )}
      </div>
    </Modal>
  );
}

function PreviewCol({
  tone,
  title,
  icon,
  urls,
  invalid,
}: {
  tone: "ok" | "err";
  title: string;
  icon: React.ReactNode;
  urls: string[];
  invalid: boolean;
}) {
  return (
    <div>
      <div className={"mb-2 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide " + (tone === "ok" ? "text-ok" : "text-err")}>
        {icon} {title}
      </div>
      <ul className="space-y-0.5 font-mono text-[11px] text-fg-muted">
        {!invalid && urls.map((u) => (
          <li key={u} className="break-all">
            <span className={tone === "ok" ? "text-ok" : "text-err"}>{tone === "ok" ? "✓" : "✗"}</span> {u}
          </li>
        ))}
        {!invalid && urls.length === 0 && <li className="text-fg-subtle">—</li>}
      </ul>
    </div>
  );
}
