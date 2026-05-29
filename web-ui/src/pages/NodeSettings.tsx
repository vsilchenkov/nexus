import { useEffect, useState } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
  FlaskConical,
  Save,
  Trash2,
  Route as RouteIcon,
  Globe,
  Lock,
  ListChecks,
  List,
  Plus,
  X,
  ShieldAlert,
} from "lucide-react";

import { api, type Node, type CHTemplate, type HostAllowlistEntry } from "../api/client";
import { DryRunDialog } from "../components/DryRunDialog";
import { DeleteNodeDialog } from "../components/node/DeleteNodeDialog";
import { AllowedHostsField } from "../components/node/AllowedHostsField";
import {
  Button,
  Card,
  Chip,
  Field,
  Hint,
  Input,
  PickGroup,
  SectionHead,
  Select,
  Toggle,
  Toggle3,
} from "../components/ui";

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
  forward_headers: string[];
  log_request_body: boolean;
  log_response_body: boolean;
  log_headers: boolean;
  logging_enabled: boolean;
  max_body_size_enabled: boolean;
  max_body_size: number;
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
  forward_headers: [],
  log_request_body: false,
  log_response_body: false,
  log_headers: false,
  logging_enabled: true,
  max_body_size_enabled: false,
  max_body_size: 0,
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
  // §23: для нового узла выбранные хосты копятся локально и привязываются после
  // создания (allowlist — производный снимок каталога, управляется link/unlink).
  const [pendingHosts, setPendingHosts] = useState<HostAllowlistEntry[]>([]);
  const [headerInput, setHeaderInput] = useState("");
  const [showDryRun, setShowDryRun] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const templates = useQuery({
    queryKey: ["ch-templates"],
    queryFn: () => api.get<{ items: CHTemplate[] }>("/api/ch-templates"),
  });

  useEffect(() => {
    if (existing.data) setForm({ ...emptyForm, ...(existing.data as unknown as Form) });
  }, [existing.data]);

  const save = useMutation({
    mutationFn: async () => {
      if (!isNew) return api.put(`/api/nodes/${id}`, form);
      // §23: создаём узел, затем привязываем выбранные паттерны к нему.
      const node = await api.post<Node>("/api/nodes", form);
      for (const h of pendingHosts) {
        await api.post(`/api/nodes/${node.id}/allowed-hosts`, { host_id: h.id });
      }
      return node;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      navigate("/");
    },
    onError: (e: { response?: { data?: { error?: string } } }) =>
      setError(e?.response?.data?.error ?? t("common.error")),
  });

  function set<K extends keyof Form>(k: K, v: Form[K]) {
    setForm((p) => ({ ...p, [k]: v }));
  }

  function addHeader() {
    const h = headerInput.trim();
    if (!h || form.forward_headers.includes(h)) {
      setHeaderInput("");
      return;
    }
    set("forward_headers", [...form.forward_headers, h]);
    setHeaderInput("");
  }

  function removeHeader(h: string) {
    set(
      "forward_headers",
      form.forward_headers.filter((x) => x !== h),
    );
  }

  const verb = form.root_method === "request" ? "request" : "requestAsync";
  const routePath = `/v1/${verb}/${form.path || "…"}`;

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">
          {isNew ? t("overview.new_node") : `${t("node.actions.edit")}: ${form.path}`}
        </h1>
        <div className="flex items-center gap-2">
          <Link to="/">
            <Button variant="ghost">{t("common.cancel")}</Button>
          </Link>
          <Button onClick={() => setShowDryRun(true)}>
            <FlaskConical className="h-4 w-4" /> {t("node.actions.dry_run")}
          </Button>
          {!isNew && (
            <Button variant="danger" onClick={() => setShowDelete(true)}>
              <Trash2 className="h-4 w-4" /> {t("node.actions.delete")}
            </Button>
          )}
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>
            <Save className="h-4 w-4" /> {t("common.save")}
          </Button>
        </div>
      </div>

      {error && <div className="rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_320px]">
        <div className="space-y-4">
          <Card>
            <SectionHead icon={<RouteIcon className="h-4 w-4" />}>{t("node.form.route")}</SectionHead>
            <Field label={t("node.form.method_type")}>
              <PickGroup
                value={form.root_method}
                onChange={(v) => set("root_method", v)}
                options={[
                  { value: "request", title: "request", description: t("node.form.sync_desc") },
                  { value: "requestAsync", title: "requestAsync", description: t("node.form.async_desc") },
                ]}
              />
            </Field>
            <Field label={t("node.fields.path")} className="mt-3">
              <Input
                mono
                value={form.path}
                onChange={(e) => set("path", e.target.value)}
                placeholder="webhook/send"
                required
              />
              <span className="font-mono text-[11px] text-fg-subtle">{routePath}</span>
            </Field>
            <Field label={t("node.form.state")} className="mt-3">
              <Toggle3
                value={form.status}
                onChange={(v) => set("status", v)}
                options={[
                  { value: "enabled", label: `▶ ${t("node.status.enabled")}` },
                  { value: "paused", label: `⏸ ${t("node.status.paused")}` },
                  { value: "disabled", label: `⏹ ${t("node.status.disabled")}` },
                ]}
              />
            </Field>
          </Card>

          <Card>
            <SectionHead icon={<Globe className="h-4 w-4" />}>{t("node.form.target")}</SectionHead>
            <Field label={t("node.fields.url_mode")}>
              <PickGroup
                value={form.url_mode}
                onChange={(v) => set("url_mode", v)}
                options={[
                  { value: "static", title: t("node.form.url_static"), description: t("node.form.url_static_desc") },
                  { value: "from_request", title: t("node.form.url_from_request"), description: t("node.form.url_from_request_desc") },
                ]}
              />
            </Field>
            {form.url_mode === "static" && (
              <Field label={t("node.fields.target_url")} className="mt-3">
                <Input
                  mono
                  value={form.target_url}
                  onChange={(e) => set("target_url", e.target.value)}
                  placeholder="https://api.partner.com/hook"
                />
              </Field>
            )}
            {form.url_mode === "from_request" && (
              <Field label={t("node.form.param_name")} className="mt-3">
                <Input
                  mono
                  value={form.url_param_name}
                  onChange={(e) => set("url_param_name", e.target.value)}
                />
                <span className="font-mono text-[11px] text-fg-subtle">
                  ?{form.url_param_name}=https://api.partner.com/hook
                </span>
              </Field>
            )}
            {form.url_mode === "from_request" && (
              <Field
                label={t("node.allowed_hosts.label")}
                hint={t("node.allowed_hosts.hint_short")}
                className="mt-3"
              >
                <AllowedHostsField
                  nodeId={id}
                  urlMode={form.url_mode}
                  pending={pendingHosts}
                  onPendingChange={setPendingHosts}
                />
              </Field>
            )}
            <div className="mt-3 grid grid-cols-2 gap-3">
              <Field label={t("node.form.timeout_ms")}>
                <Input
                  type="number"
                  value={form.timeout_ms}
                  onChange={(e) => set("timeout_ms", Number(e.target.value))}
                />
              </Field>
              <Field label={t("node.form.retry_count")}>
                <Input
                  type="number"
                  value={form.retry_count}
                  onChange={(e) => set("retry_count", Number(e.target.value))}
                />
              </Field>
            </div>
          </Card>

          <Card>
            <SectionHead icon={<Lock className="h-4 w-4" />}>{t("node.form.auth")}</SectionHead>
            <Field label={t("node.form.incoming")}>
              <Select
                value={form.incoming_auth_type}
                onChange={(e) => set("incoming_auth_type", e.target.value)}
              >
                <option value="none">none</option>
                <option value="basic">basic</option>
                <option value="token">token</option>
                <option value="webhook_signature">webhook_signature</option>
              </Select>
            </Field>
            {form.incoming_auth_type !== "none" && (
              <Field
                label={
                  form.incoming_auth_type === "webhook_signature"
                    ? t("node.form.webhook_secret")
                    : t("node.form.credentials")
                }
                className="mt-3"
              >
                <Input
                  value={form.incoming_auth_credentials}
                  onChange={(e) => set("incoming_auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : t("node.form.keep_secret")}
                />
              </Field>
            )}
            {form.incoming_auth_type === "webhook_signature" && (
              <div className="mt-3 grid grid-cols-2 gap-3">
                <Field label={t("node.form.sig_header")}>
                  <Input
                    mono
                    value={form.webhook_signature_header}
                    onChange={(e) => set("webhook_signature_header", e.target.value)}
                  />
                </Field>
                <Field label={t("node.form.sig_prefix")}>
                  <Input
                    mono
                    value={form.webhook_signature_prefix}
                    onChange={(e) => set("webhook_signature_prefix", e.target.value)}
                  />
                </Field>
              </div>
            )}
            {form.incoming_auth_type === "webhook_signature" && (
              <Hint tone="muted" className="mt-3">
                {t("node.webhook.hint")}
              </Hint>
            )}
            <Field label={t("node.form.outgoing")} className="mt-3">
              <Select value={form.auth_type} onChange={(e) => set("auth_type", e.target.value)}>
                <option value="none">none</option>
                <option value="basic">basic</option>
                <option value="token">token</option>
                <option value="token_from_request">token_from_request</option>
                <option value="basic_from_request">basic_from_request</option>
              </Select>
            </Field>
            {(form.auth_type === "basic" || form.auth_type === "token") && (
              <Field label={t("node.form.credentials")} className="mt-3">
                <Input
                  value={form.auth_credentials}
                  onChange={(e) => set("auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : t("node.form.keep_secret")}
                />
              </Field>
            )}
          </Card>

          <Card>
            <SectionHead icon={<List className="h-4 w-4" />}>
              {t("node.form.headers")}
            </SectionHead>
            <Field label={t("node.form.forward_headers")} hint={t("node.form.forward_headers_hint")}>
              {form.forward_headers.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-1.5">
                  {form.forward_headers.map((h) => (
                    <Chip key={h}>
                      <span className="font-mono">{h}</span>
                      <button
                        type="button"
                        onClick={() => removeHeader(h)}
                        className="ml-1 text-fg-subtle hover:text-err"
                        aria-label={t("common.delete")}
                      >
                        <X className="h-3 w-3" />
                      </button>
                    </Chip>
                  ))}
                </div>
              )}
              <div className="flex gap-2">
                <Input
                  mono
                  value={headerInput}
                  onChange={(e) => setHeaderInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addHeader();
                    }
                  }}
                  placeholder="X-Request-Id"
                />
                <Button type="button" onClick={addHeader}>
                  <Plus className="h-4 w-4" /> {t("node.form.add_header")}
                </Button>
              </div>
            </Field>
          </Card>

          <Card>
            <div className="mb-3 flex items-center justify-between">
              <SectionHead icon={<ListChecks className="h-4 w-4" />} className="mb-0">
                {t("node.form.logging")}
              </SectionHead>
              <Toggle
                checked={form.logging_enabled}
                onChange={(v) => set("logging_enabled", v)}
                label={t("node.form.logging_enabled")}
              />
            </div>
            <fieldset
              disabled={!form.logging_enabled}
              className={form.logging_enabled ? "" : "pointer-events-none opacity-50"}
            >
              <Field label={t("node.fields.ch_template")} hint={t("node.fields.ch_template_hint")}>
                <Select
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
                </Select>
              </Field>
              <Field label={t("node.fields.ch_table")} className="mt-3">
                <Input
                  mono
                  value={form.clickhouse_table}
                  onChange={(e) => set("clickhouse_table", e.target.value)}
                  placeholder="webhook_send"
                />
              </Field>
              <Field label={t("node.form.retention_days")} className="mt-3">
                <Input
                  type="number"
                  value={form.clickhouse_retention_days}
                  onChange={(e) => set("clickhouse_retention_days", Number(e.target.value))}
                />
              </Field>
              <Field label={t("node.form.log_what")} className="mt-3">
                <div className="flex flex-col gap-1.5 text-xs">
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={form.log_request_body}
                      onChange={(e) => set("log_request_body", e.target.checked)}
                    />
                    {t("node.form.log_request")}
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={form.log_response_body}
                      onChange={(e) => set("log_response_body", e.target.checked)}
                    />
                    {t("node.form.log_response")}
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={form.log_headers}
                      onChange={(e) => set("log_headers", e.target.checked)}
                    />
                    {t("node.form.log_headers")}
                  </label>
                </div>
              </Field>
              <div className="mt-4 border-t border-line pt-3">
                <Toggle
                  checked={form.max_body_size_enabled}
                  onChange={(v) => set("max_body_size_enabled", v)}
                  label={t("node.form.max_body_enabled")}
                />
                <Field label={t("node.form.max_body_size")} hint={t("node.form.max_body_hint")} className="mt-2">
                  <Input
                    type="number"
                    min={0}
                    disabled={!form.max_body_size_enabled}
                    value={form.max_body_size}
                    onChange={(e) => set("max_body_size", Number(e.target.value))}
                    placeholder="10000"
                  />
                </Field>
              </div>
            </fieldset>
          </Card>
        </div>

        <div className="space-y-3">
          <Card>
            <div className="mb-2.5 text-sm font-semibold">{t("node.form.preview")}</div>
            <div className="space-y-1 text-[12px] leading-7 text-fg-muted">
              <div>
                <span className="rounded bg-accent/10 px-2 py-0.5 text-[11px] text-accent">POST</span>{" "}
                <span className="font-mono">{routePath}</span>
              </div>
              <div className="pl-2">↓ Receiver</div>
              <div className="pl-2">
                ↓ {form.root_method === "request" ? "gRPC (sync)" : "Kafka (async)"}
              </div>
              <div className="pl-2">↓ Sender</div>
              <div className="pl-2">
                →{" "}
                <span className="font-mono text-fg">
                  {form.url_mode === "static"
                    ? form.target_url || "{target_url}"
                    : `{${form.url_param_name}}`}
                </span>
              </div>
            </div>
          </Card>
          {form.url_mode === "from_request" && (
            <Hint tone="danger" icon={<ShieldAlert className="h-4 w-4" />}>
              {t("node.form.ssrf_warn")}
            </Hint>
          )}
        </div>
      </div>

      {showDryRun && <DryRunDialog node={form} onClose={() => setShowDryRun(false)} />}
      {showDelete && existing.data && (
        <DeleteNodeDialog node={existing.data} onClose={() => setShowDelete(false)} />
      )}
    </div>
  );
}
