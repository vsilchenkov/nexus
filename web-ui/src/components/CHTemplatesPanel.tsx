import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";

import { api, type CHTemplate, type CHTemplateSpec } from "../api/client";

// Обязательные колонки таблицы логов (§4.3 / domain.RequiredLogColumns).
const LOG_COLUMNS = [
  "ID", "type", "url", "method", "parameters", "request", "response",
  "status", "reason", "date_create", "date_request", "date_response",
  "duration", "done", "checksum_request", "checksum_response",
  "Host", "IP", "attempts", "attempts_details",
];
const PARTITIONS = ["toYYYYMM(date_create)", "toYYYYMMDD(date_create)", "toDate(date_create)", "tuple()"];

type Editor = {
  id: string;
  name: string;
  description: string;
  spec: CHTemplateSpec;
  is_default: boolean;
};

function emptyEditor(): Editor {
  return {
    id: "",
    name: "",
    description: "",
    is_default: false,
    spec: {
      engine: "MergeTree",
      partition_by: "toYYYYMM(date_create)",
      order_by: ["date_create", "date_request", "method"],
      column_overrides: [],
      indexes: [],
      ttl_mode: "none",
    },
  };
}

export function CHTemplatesPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["ch-templates"],
    queryFn: () => api.get<{ items: CHTemplate[] }>("/api/ch-templates"),
  });

  const [editor, setEditor] = useState<Editor | null>(null);
  const [verify, setVerify] = useState<{ ok: boolean; error?: string } | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: (e: Editor) => {
      const body = { name: e.name, description: e.description, spec: e.spec, is_default: e.is_default };
      return e.id ? api.put(`/api/ch-templates/${e.id}`, body) : api.post("/api/ch-templates", body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ch-templates"] });
      setEditor(null);
      setVerify(null);
      setErr(null);
    },
    onError: (e: any) => setErr(e?.response?.data?.error ?? String(e)),
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.del(`/api/ch-templates/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["ch-templates"] }),
    onError: (e: any) => setErr(e?.response?.data?.error ?? String(e)),
  });

  const doVerify = useMutation({
    mutationFn: (e: Editor) =>
      api.post<{ ok: boolean; error?: string }>("/api/ch-templates/verify", {
        name: e.name || "draft", description: e.description, spec: e.spec, is_default: e.is_default,
      }),
    onSuccess: (r) => setVerify(r),
    onError: (e: any) => setVerify({ ok: false, error: e?.response?.data?.error ?? String(e) }),
  });

  function openEdit(tpl: CHTemplate) {
    setEditor({
      id: tpl.id, name: tpl.name, description: tpl.description, is_default: tpl.is_default,
      spec: {
        ...tpl.spec,
        column_overrides: tpl.spec.column_overrides ?? [],
        indexes: tpl.spec.indexes ?? [],
      },
    });
    setVerify(null);
    setErr(null);
  }

  function patchSpec(p: Partial<CHTemplateSpec>) {
    setEditor((e) => (e ? { ...e, spec: { ...e.spec, ...p } } : e));
  }

  return (
    <section className="mt-8 border-t border-bg-muted pt-6 space-y-4">
      <header className="flex items-center justify-between">
        <h3 className="text-lg font-semibold">{t("settings.clickhouse.templates.title")}</h3>
        <button
          type="button"
          onClick={() => { setEditor(emptyEditor()); setVerify(null); setErr(null); }}
          className="flex items-center gap-1 bg-accent hover:bg-accent-hover px-3 py-1.5 rounded-md text-sm"
        >
          <Plus className="w-4 h-4" /> {t("settings.clickhouse.templates.new")}
        </button>
      </header>
      <p className="text-sm text-fg-muted">{t("settings.clickhouse.templates.hint")}</p>

      {list.data?.items.length === 0 && (
        <p className="text-sm text-fg-muted">{t("settings.clickhouse.templates.empty")}</p>
      )}

      <ul className="space-y-2">
        {list.data?.items.map((tpl) => (
          <li key={tpl.id} className="flex items-center justify-between bg-bg-muted/40 rounded-md px-3 py-2">
            <div>
              <span className="font-medium">{tpl.name}</span>
              {tpl.is_default && (
                <span className="ml-2 text-xs text-accent">{t("settings.clickhouse.templates.default_badge")}</span>
              )}
              <span className="ml-2 text-xs text-fg-muted">
                {tpl.spec.partition_by} · {tpl.spec.ttl_mode}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <button type="button" onClick={() => openEdit(tpl)} className="text-sm text-fg-muted hover:text-accent">
                {t("node.actions.edit")}
              </button>
              {!tpl.is_default && (
                <button
                  type="button"
                  onClick={() => { if (confirm(t("settings.clickhouse.templates.delete_confirm", { name: tpl.name }))) remove.mutate(tpl.id); }}
                  className="text-err hover:opacity-80"
                  title={t("node.actions.delete")}
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              )}
            </div>
          </li>
        ))}
      </ul>

      {editor && (
        <TemplateEditor
          editor={editor}
          setEditor={setEditor}
          patchSpec={patchSpec}
          onSave={() => save.mutate(editor)}
          onVerify={() => doVerify.mutate(editor)}
          onCancel={() => { setEditor(null); setVerify(null); setErr(null); }}
          verifying={doVerify.isPending}
          verify={verify}
          saving={save.isPending}
          err={err}
        />
      )}
    </section>
  );
}

function TemplateEditor(props: {
  editor: Editor;
  setEditor: (e: Editor) => void;
  patchSpec: (p: Partial<CHTemplateSpec>) => void;
  onSave: () => void;
  onVerify: () => void;
  onCancel: () => void;
  verifying: boolean;
  verify: { ok: boolean; error?: string } | null;
  saving: boolean;
  err: string | null;
}) {
  const { t } = useTranslation();
  const { editor: e, setEditor, patchSpec, verify } = props;
  const codecs = e.spec.column_overrides ?? [];
  const indexes = e.spec.indexes ?? [];
  const inp = "w-full px-2 py-1.5 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent text-sm";

  return (
    <div className="border border-bg-muted rounded-md p-4 space-y-3 bg-bg-elev">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <label className="space-y-1">
          <span className="text-xs text-fg-muted">{t("settings.clickhouse.templates.name")}</span>
          <input className={inp} value={e.name} onChange={(ev) => setEditor({ ...e, name: ev.target.value })} />
        </label>
        <label className="space-y-1">
          <span className="text-xs text-fg-muted">{t("settings.clickhouse.templates.description")}</span>
          <input className={inp} value={e.description} onChange={(ev) => setEditor({ ...e, description: ev.target.value })} />
        </label>
        <label className="space-y-1">
          <span className="text-xs text-fg-muted">{t("settings.clickhouse.templates.partition_by")}</span>
          <select className={inp} value={e.spec.partition_by} onChange={(ev) => patchSpec({ partition_by: ev.target.value })}>
            {PARTITIONS.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </label>
        <label className="space-y-1">
          <span className="text-xs text-fg-muted">{t("settings.clickhouse.templates.order_by")}</span>
          <input
            className={`${inp} font-mono`}
            value={e.spec.order_by.join(", ")}
            onChange={(ev) => patchSpec({ order_by: ev.target.value.split(",").map((s) => s.trim()).filter(Boolean) })}
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs text-fg-muted">{t("settings.clickhouse.templates.ttl_mode")}</span>
          <select className={inp} value={e.spec.ttl_mode} onChange={(ev) => patchSpec({ ttl_mode: ev.target.value as "none" | "ttl_days" })}>
            <option value="none">{t("settings.clickhouse.templates.ttl_none")}</option>
            <option value="ttl_days">{t("settings.clickhouse.templates.ttl_days")}</option>
          </select>
        </label>
        <label className="flex items-center gap-2 mt-5">
          <input type="checkbox" checked={e.is_default} onChange={(ev) => setEditor({ ...e, is_default: ev.target.checked })} />
          <span className="text-sm">{t("settings.clickhouse.templates.is_default")}</span>
        </label>
      </div>

      {/* CODEC overrides */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-sm font-medium">{t("settings.clickhouse.templates.codecs")}</span>
          <button type="button" className="text-xs text-accent" onClick={() => patchSpec({ column_overrides: [...codecs, { name: "request", codec: "ZSTD(3)" }] })}>
            + {t("settings.clickhouse.templates.codecs_add")}
          </button>
        </div>
        {codecs.map((c, i) => (
          <div key={i} className="flex items-center gap-2">
            <select className={inp} value={c.name} onChange={(ev) => {
              const next = [...codecs]; next[i] = { ...c, name: ev.target.value }; patchSpec({ column_overrides: next });
            }}>
              {LOG_COLUMNS.map((col) => <option key={col} value={col}>{col}</option>)}
            </select>
            <input className={`${inp} font-mono`} value={c.codec} placeholder="ZSTD(3)" onChange={(ev) => {
              const next = [...codecs]; next[i] = { ...c, codec: ev.target.value }; patchSpec({ column_overrides: next });
            }} />
            <button type="button" className="text-err" onClick={() => patchSpec({ column_overrides: codecs.filter((_, j) => j !== i) })}>
              <Trash2 className="w-4 h-4" />
            </button>
          </div>
        ))}
      </div>

      {/* Indexes */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-sm font-medium">{t("settings.clickhouse.templates.indexes")}</span>
          <button type="button" className="text-xs text-accent" onClick={() => patchSpec({ indexes: [...indexes, { name: "idx_host", expr: "Host", type: "bloom_filter(0.01)", granularity: 4 }] })}>
            + {t("settings.clickhouse.templates.indexes_add")}
          </button>
        </div>
        {indexes.map((idx, i) => (
          <div key={i} className="grid grid-cols-[1fr_1fr_1fr_80px_32px] gap-2">
            <input className={inp} value={idx.name} placeholder="idx_name" onChange={(ev) => {
              const next = [...indexes]; next[i] = { ...idx, name: ev.target.value }; patchSpec({ indexes: next });
            }} />
            <select className={inp} value={idx.expr} onChange={(ev) => {
              const next = [...indexes]; next[i] = { ...idx, expr: ev.target.value }; patchSpec({ indexes: next });
            }}>
              {LOG_COLUMNS.map((col) => <option key={col} value={col}>{col}</option>)}
            </select>
            <input className={`${inp} font-mono`} value={idx.type} placeholder="minmax" onChange={(ev) => {
              const next = [...indexes]; next[i] = { ...idx, type: ev.target.value }; patchSpec({ indexes: next });
            }} />
            <input type="number" className={inp} value={idx.granularity} onChange={(ev) => {
              const next = [...indexes]; next[i] = { ...idx, granularity: Number(ev.target.value) }; patchSpec({ indexes: next });
            }} />
            <button type="button" className="text-err" onClick={() => patchSpec({ indexes: indexes.filter((_, j) => j !== i) })}>
              <Trash2 className="w-4 h-4" />
            </button>
          </div>
        ))}
      </div>

      {props.err && <p className="text-sm text-err">{props.err}</p>}
      {verify && verify.ok && <p className="text-sm text-ok">{t("settings.clickhouse.templates.verify_ok")}</p>}
      {verify && !verify.ok && <p className="text-sm text-err">{t("settings.clickhouse.templates.verify_failed", { error: verify.error })}</p>}

      <div className="flex items-center gap-2">
        <button type="button" onClick={props.onSave} disabled={props.saving} className="bg-accent hover:bg-accent-hover px-3 py-1.5 rounded-md text-sm disabled:opacity-50">
          {t("common.save")}
        </button>
        <button type="button" onClick={props.onVerify} disabled={props.verifying} className="bg-bg-muted hover:bg-bg-elev px-3 py-1.5 rounded-md text-sm">
          {props.verifying ? t("settings.clickhouse.templates.verifying") : t("settings.clickhouse.templates.verify")}
        </button>
        <button type="button" onClick={props.onCancel} className="px-3 py-1.5 text-sm text-fg-muted hover:text-fg">
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}
