import { useEffect, useState } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Beaker, Trash2 } from "lucide-react";

import { api, type Node, type CHTemplate } from "../api/client";
import { Topbar } from "../components/Topbar";
import { DryRunDialog } from "../components/DryRunDialog";

type Form = {
  path: string;
  root_method: "request" | "requestAsync";
  url_mode: "static" | "from_request";
  target_url: string;
  url_param_name: string;
  auth_type: string;
  auth_credentials: string;
  incoming_auth_type: string;
  incoming_auth_credentials: string;
  webhook_signature_header: string;
  webhook_signature_prefix: string;
  timeout_ms: number;
  retry_count: number;
  retry_backoff_ms: number;
  clickhouse_table: string;
  clickhouse_template_id: string;
  clickhouse_retention_days: number;
  status: "enabled" | "disabled" | "paused";
  log_request_body: boolean;
  log_response_body: boolean;
};

const emptyForm: Form = {
  path: "",
  root_method: "request",
  url_mode: "static",
  target_url: "",
  url_param_name: "url_base",
  auth_type: "none",
  auth_credentials: "",
  incoming_auth_type: "none",
  incoming_auth_credentials: "",
  webhook_signature_header: "X-Hub-Signature-256",
  webhook_signature_prefix: "sha256=",
  timeout_ms: 30000,
  retry_count: 0,
  retry_backoff_ms: 1000,
  clickhouse_table: "",
  clickhouse_template_id: "",
  clickhouse_retention_days: 90,
  status: "enabled",
  log_request_body: false,
  log_response_body: false,
};

export default function NodeSettings() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const isNew = !id;

  const existing = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !isNew,
  });

  const [form, setForm] = useState<Form>(emptyForm);
  const [showDryRun, setShowDryRun] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // §19: список шаблонов CH-таблиц для селектора.
  const templates = useQuery({
    queryKey: ["ch-templates"],
    queryFn: () => api.get<{ items: CHTemplate[] }>("/api/ch-templates"),
  });

  useEffect(() => {
    if (existing.data) {
      setForm({ ...emptyForm, ...(existing.data as unknown as Form) });
    }
  }, [existing.data]);

  const save = useMutation({
    mutationFn: () => (isNew ? api.post("/api/nodes", form) : api.put(`/api/nodes/${id}`, form)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      navigate("/");
    },
    onError: (e: any) => setError(e?.response?.data?.error ?? String(e)),
  });

  const remove = useMutation({
    mutationFn: () => api.del(`/api/nodes/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      navigate("/");
    },
    onError: (e: any) => setError(e?.response?.data?.error ?? String(e)),
  });

  function set<K extends keyof Form>(k: K, v: Form[K]) {
    setForm((p) => ({ ...p, [k]: v }));
  }

  return (
    <div className="min-h-screen bg-bg text-fg">
      <Topbar />

      <main className="max-w-4xl mx-auto p-6 space-y-6">
        <Link to="/" className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-accent">
          <ArrowLeft className="w-4 h-4" />
          back
        </Link>

        <header className="flex items-center justify-between">
          <h1 className="text-2xl font-semibold">
            {isNew ? t("overview.new_node") : `${t("node.actions.edit")}: ${form.path}`}
          </h1>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setShowDryRun(true)}
              className="flex items-center gap-2 bg-bg-muted hover:bg-bg-elev px-3 py-2 rounded-md text-sm"
            >
              <Beaker className="w-4 h-4" />
              dry-run
            </button>
            {!isNew && (
              <button
                type="button"
                onClick={() => {
                  if (confirm("delete?")) remove.mutate();
                }}
                className="flex items-center gap-2 bg-err/20 hover:bg-err/30 text-err px-3 py-2 rounded-md text-sm"
              >
                <Trash2 className="w-4 h-4" />
                {t("node.actions.delete")}
              </button>
            )}
          </div>
        </header>

        {error && <div className="text-err">{error}</div>}

        <form
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
          className="space-y-6"
        >
          {/* Route */}
          <Section title="route">
            <Field label="path">
              <input
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent font-mono"
                value={form.path}
                onChange={(e) => set("path", e.target.value)}
                placeholder="webhook/send"
                required
              />
            </Field>
            <Field label="root_method">
              <Select
                value={form.root_method}
                onChange={(v) => set("root_method", v as Form["root_method"])}
                options={["request", "requestAsync"]}
              />
            </Field>
            <Field label="status">
              <Select
                value={form.status}
                onChange={(v) => set("status", v as Form["status"])}
                options={["enabled", "paused", "disabled"]}
              />
            </Field>
          </Section>

          {/* URL */}
          <Section title="target">
            <Field label="url_mode">
              <Select
                value={form.url_mode}
                onChange={(v) => set("url_mode", v as Form["url_mode"])}
                options={["static", "from_request"]}
              />
            </Field>
            {form.url_mode === "static" && (
              <Field label="target_url">
                <input
                  className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent font-mono"
                  value={form.target_url}
                  onChange={(e) => set("target_url", e.target.value)}
                  placeholder="https://api.partner.com/hook"
                />
              </Field>
            )}
            {form.url_mode === "from_request" && (
              <Field label="url_param_name">
                <input
                  className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                  value={form.url_param_name}
                  onChange={(e) => set("url_param_name", e.target.value)}
                />
              </Field>
            )}
            <Field label="timeout_ms">
              <input
                type="number"
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                value={form.timeout_ms}
                onChange={(e) => set("timeout_ms", Number(e.target.value))}
              />
            </Field>
            <Field label="retry_count">
              <input
                type="number"
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                value={form.retry_count}
                onChange={(e) => set("retry_count", Number(e.target.value))}
              />
            </Field>
          </Section>

          {/* Auth */}
          <Section title="auth">
            <Field label="incoming_auth_type">
              <Select
                value={form.incoming_auth_type}
                onChange={(v) => set("incoming_auth_type", v)}
                options={["none", "basic", "token", "webhook_signature"]}
              />
            </Field>
            {form.incoming_auth_type !== "none" && (
              <Field
                label={
                  form.incoming_auth_type === "webhook_signature"
                    ? "webhook_secret"
                    : "incoming_auth_credentials"
                }
              >
                <input
                  className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                  value={form.incoming_auth_credentials}
                  onChange={(e) => set("incoming_auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : "leave empty to keep current"}
                />
              </Field>
            )}
            {form.incoming_auth_type === "webhook_signature" && (
              <>
                <Field label="webhook_signature_header">
                  <input
                    className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent font-mono"
                    value={form.webhook_signature_header}
                    onChange={(e) => set("webhook_signature_header", e.target.value)}
                    placeholder="X-Hub-Signature-256"
                  />
                </Field>
                <Field label="webhook_signature_prefix">
                  <input
                    className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent font-mono"
                    value={form.webhook_signature_prefix}
                    onChange={(e) => set("webhook_signature_prefix", e.target.value)}
                    placeholder="sha256="
                  />
                </Field>
                <div className="col-span-2 text-xs text-fg-muted bg-bg-muted/40 rounded-md p-3">
                  {t("node.webhook.hint", {
                    defaultValue:
                      "Receive webhooks at POST /v1/callback/{path}. Signature is HMAC-SHA256 of raw body with the secret, hex-encoded, optionally prefixed (e.g. \"sha256=\") in the header above.",
                  })}
                </div>
              </>
            )}
            <Field label="auth_type (outgoing)">
              <Select
                value={form.auth_type}
                onChange={(v) => set("auth_type", v)}
                options={["none", "basic", "token", "token_from_request", "basic_from_request"]}
              />
            </Field>
            {(form.auth_type === "basic" || form.auth_type === "token") && (
              <Field label="auth_credentials">
                <input
                  className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                  value={form.auth_credentials}
                  onChange={(e) => set("auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : "leave empty to keep current"}
                />
              </Field>
            )}
          </Section>

          {/* ClickHouse */}
          <Section title="clickhouse">
            <Field label={t("node.fields.ch_template")}>
              <select
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                value={form.clickhouse_template_id}
                onChange={(e) => set("clickhouse_template_id", e.target.value)}
              >
                <option value="">{t("node.fields.ch_template_manual")}</option>
                {templates.data?.items.map((tpl) => (
                  <option key={tpl.id} value={tpl.id}>
                    {tpl.name}
                    {tpl.is_default ? " ★" : ""}
                  </option>
                ))}
              </select>
              <p className="text-xs text-fg-muted mt-1">{t("node.fields.ch_template_hint")}</p>
            </Field>
            <Field label="clickhouse_table">
              <input
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent font-mono"
                value={form.clickhouse_table}
                onChange={(e) => set("clickhouse_table", e.target.value)}
                placeholder="webhook_send"
              />
            </Field>
            <Field label="retention_days">
              <input
                type="number"
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
                value={form.clickhouse_retention_days}
                onChange={(e) => set("clickhouse_retention_days", Number(e.target.value))}
              />
            </Field>
            <Field label="log bodies">
              <label className="flex items-center gap-2 mr-4">
                <input
                  type="checkbox"
                  checked={form.log_request_body}
                  onChange={(e) => set("log_request_body", e.target.checked)}
                />
                request
              </label>
              <label className="inline-flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={form.log_response_body}
                  onChange={(e) => set("log_response_body", e.target.checked)}
                />
                response
              </label>
            </Field>
          </Section>

          <div className="flex items-center justify-end gap-3">
            <Link to="/" className="px-3 py-2 text-sm text-fg-muted hover:text-fg">
              {t("common.cancel")}
            </Link>
            <button
              type="submit"
              disabled={save.isPending}
              className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm font-medium disabled:opacity-50"
            >
              {t("common.save")}
            </button>
          </div>
        </form>

        {showDryRun && (
          <DryRunDialog
            node={form}
            onClose={() => setShowDryRun(false)}
          />
        )}
      </main>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="bg-bg-elev rounded-xl border border-bg-muted p-4">
      <h2 className="text-sm uppercase tracking-wider text-fg-muted mb-3">{title}</h2>
      <div className="grid grid-cols-2 gap-3">{children}</div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block text-sm">
      <div className="text-fg-muted mb-1 text-xs font-mono">{label}</div>
      {children}
    </label>
  );
}

function Select({
  value,
  onChange,
  options,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent"
    >
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}
