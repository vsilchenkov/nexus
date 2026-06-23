import { useEffect, useState } from "react";
import { useNavigate, useParams, Link, Navigate } from "react-router-dom";
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
  ShieldAlert,
  MessageSquare,
  RefreshCw,
} from "lucide-react";

import { api, type Node, type CHTemplate, type HostAllowlistEntry } from "../api/client";
import { useNodeUrlBuilder } from "../lib/nodeUrl";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { parseNumInput } from "../lib/numField";
import { validateNodeForm } from "../lib/nodeValidation";
import { DryRunDialog } from "../components/DryRunDialog";
import { DeleteNodeDialog } from "../components/node/DeleteNodeDialog";
import { AllowedHostsField } from "../components/node/AllowedHostsField";
import { HeadersField } from "../components/node/HeadersField";
import { RequestFieldField } from "../components/node/RequestFieldField";
import { RabbitMQSection, type RMQSetter } from "../components/node/RabbitMQSection";
import {
  Button,
  Card,
  CopyButton,
  ErrorAlert,
  Field,
  Hint,
  Input,
  LabelHint,
  PickGroup,
  SecretInput,
  SectionHead,
  Select,
  Textarea,
  Toggle,
  Toggle3,
} from "../components/ui";

const HTTP_METHODS = ["GET", "POST", "PUT", "DELETE", "ANY"] as const;

type Form = {
  path: string;
  root_method: "request" | "requestAsync" | "RabbitMQAsync";
  incoming_method: "GET" | "POST" | "PUT" | "DELETE" | "ANY";
  outgoing_method: "GET" | "POST" | "PUT" | "DELETE" | "ANY";
  url_mode: "static" | "from_request";
  target_url: string;
  path_passthrough: boolean;
  url_param_name: string;
  auth_type: string;
  auth_credentials: string;
  incoming_auth_type: string;
  incoming_auth_credentials: string;
  // §41: динамическая авторизация — источник (header/query) и имя поля.
  auth_dynamic_source: string;
  auth_dynamic_field: string;
  auth_dynamic_strip_prefix: string; // скрыто в UI, round-trip для back-compat
  incoming_auth_dynamic_source: string;
  incoming_auth_dynamic_field: string;
  webhook_signature_header: string;
  webhook_signature_prefix: string;
  timeout_ms: number;
  retry_count: number;
  retry_backoff_ms: number;
  clickhouse_table: string;
  clickhouse_template_id: string;
  clickhouse_retention_days: number;
  dlq_ttl_seconds: number;
  dlq_retry_delay_seconds: number;
  status: "enabled" | "disabled" | "paused";
  forward_headers: string[];
  log_request_body: boolean;
  log_response_body: boolean;
  log_headers: boolean;
  logging_enabled: boolean;
  max_body_size_enabled: boolean;
  max_body_size: number;
  // §27: RabbitMQAsync.
  rmq_host: string;
  rmq_port: number;
  rmq_vhost: string;
  rmq_user: string;
  rmq_password: string;
  rmq_queue: string;
  rmq_use_tls: boolean;
  pull_interval_sec: number;
  pull_batch_size: number;
  pull_prefetch: number;
  // §29: произвольный комментарий-описание узла.
  comment: string;
};

const emptyForm: Form = {
  path: "",
  root_method: "request",
  incoming_method: "POST",
  outgoing_method: "POST",
  url_mode: "static",
  target_url: "",
  path_passthrough: false,
  url_param_name: "url_base",
  auth_type: "none",
  auth_credentials: "",
  incoming_auth_type: "none",
  incoming_auth_credentials: "",
  auth_dynamic_source: "query",
  auth_dynamic_field: "",
  auth_dynamic_strip_prefix: "",
  incoming_auth_dynamic_source: "header",
  incoming_auth_dynamic_field: "Authorization",
  webhook_signature_header: "X-Hub-Signature-256",
  webhook_signature_prefix: "sha256=",
  timeout_ms: 30000,
  retry_count: 0,
  retry_backoff_ms: 1000,
  clickhouse_table: "",
  clickhouse_template_id: "",
  clickhouse_retention_days: 90,
  dlq_ttl_seconds: 86400,
  dlq_retry_delay_seconds: 300,
  status: "enabled",
  forward_headers: [],
  log_request_body: false,
  log_response_body: false,
  log_headers: false,
  logging_enabled: true,
  max_body_size_enabled: false,
  max_body_size: 0,
  rmq_host: "",
  rmq_port: 5672,
  rmq_vhost: "/",
  rmq_user: "",
  rmq_password: "",
  rmq_queue: "",
  rmq_use_tls: false,
  pull_interval_sec: 5,
  pull_batch_size: 100,
  pull_prefetch: 100,
  comment: "",
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
  const [showDryRun, setShowDryRun] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // §28 Пункт 5: имя поля с ошибкой валидации (для inline-подсветки) из
  // ответа { error, code, field } бэкенда.
  const [errField, setErrField] = useState<string | null>(null);

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
    onSuccess: (data) => {
      const nodeId = isNew ? (data as { id?: string } | undefined)?.id : id;
      qc.invalidateQueries({ queryKey: ["nodes"] });
      // Инвалидируем и конкретный узел: иначе детальная/форма (staleTime 30с,
      // queryKey ["node", id]) после правки ~30с показывают устаревший кэш —
      // только что изменённое поле «отъезжает» к старому значению, пока не
      // истечёт staleTime. Точечная инвалидация даёт мгновенный refetch.
      if (nodeId) qc.invalidateQueries({ queryKey: ["node", nodeId] });
      // Сброс формы: иначе при следующем заходе на /nodes/new остаются
      // значения только что созданного узла (Phase AUD.7).
      setForm(emptyForm);
      setPendingHosts([]);
      // После сохранения возвращаемся в ОКНО УЗЛА (detail), а не на список:
      // правка существующего → его страница; создание → страница нового узла.
      navigate(nodeId ? `/nodes/${nodeId}` : "/");
    },
    onError: (e: { response?: { data?: { error?: string; code?: string; field?: string } } }) => {
      const d = e?.response?.data;
      setErrField(d?.field ?? null);
      // Переводим по code в языке UI (может отличаться от Accept-Language);
      // fallback — текст error с бэка, затем generic.
      setError(d?.code ? t(d.code) : (d?.error ?? t("common.error")));
    },
  });

  function set<K extends keyof Form>(k: K, v: Form[K]) {
    setForm((p) => ({ ...p, [k]: v }));
    // Сбрасываем подсветку поля, как только пользователь его правит.
    setErrField((f) => (f === k ? null : f));
  }

  // fieldErr — inline-сообщение об ошибке под полем name (§28 Пункт 5).
  function fieldErr(name: string) {
    if (errField !== name) return null;
    return <p className="mt-1 text-xs text-err">{error}</p>;
  }
  // errCls — класс красной рамки для поля с ошибкой.
  const errCls = (name: string) => (errField === name ? "border-err" : "");

  // submit — клиентская валидация лимитов (зеркало domain.Node.Validate) до
  // запроса: те же i18n-коды node.validation.*, что возвращает backend.
  function submit() {
    const v = validateNodeForm(form);
    if (v) {
      setErrField(v.field);
      setError(t(v.code));
      return;
    }
    setError(null);
    setErrField(null);
    save.mutate();
  }

  const isPull = form.root_method === "RabbitMQAsync";
  const verb = form.root_method === "request" ? "request" : "requestAsync";
  // §28 Пункт 1: полный адрес собирается из публичного адреса приложения
  // (если задан в настройках) или origin браузера + slug текущей команды.
  const buildUrl = useNodeUrlBuilder();
  const fullAddress = buildUrl(verb, form.path);

  // §26/§28 Пункт 3: форму узла (с полями авторизации) открывает только
  // manager+. viewer перенаправляется на просмотр/список.
  const canEdit = useRoleAtLeast("manager");
  if (!canEdit) {
    return <Navigate to={isNew ? "/" : `/nodes/${id}`} replace />;
  }

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">
          {isNew ? t("overview.new_node") : `${t("node.actions.edit")}: ${form.path}`}
        </h1>
        <div className="flex flex-wrap items-center gap-2">
          <Link to={isNew ? "/" : `/nodes/${id}`}>
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
          <Button
            variant="primary"
            disabled={save.isPending || form.path.trim() === ""}
            onClick={submit}
          >
            <Save className="h-4 w-4" /> {t("common.save")}
          </Button>
        </div>
      </div>

      {error && !errField && <ErrorAlert>{error}</ErrorAlert>}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_320px]">
        <div className="space-y-4">
          <Card>
            <SectionHead icon={<RouteIcon className="h-4 w-4" />}>{t("node.form.route")}</SectionHead>
            <Field label={t("node.form.method_type")} help={t("node.help.method_type")}>
              <PickGroup
                value={form.root_method}
                onChange={(v) => set("root_method", v)}
                options={[
                  { value: "request", title: "request", description: t("node.form.sync_desc") },
                  { value: "requestAsync", title: "requestAsync", description: t("node.form.async_desc") },
                  { value: "RabbitMQAsync", title: "RabbitMQAsync", description: t("node.form.rmq_desc") },
                ]}
              />
            </Field>
            <Field label={t("node.fields.path")} help={t("node.help.path")} className="mt-3">
              <Input
                mono
                className={errCls("path")}
                value={form.path}
                onChange={(e) => set("path", e.target.value)}
                placeholder="webhook/send"
                required
              />
              {fieldErr("path")}
              {!isPull && (
                <div className="mt-1 flex items-center gap-1.5">
                  <span className="min-w-0 break-all font-mono text-[11px] text-fg-subtle">
                    {fullAddress}
                  </span>
                  <CopyButton value={fullAddress} />
                </div>
              )}
            </Field>
            {!isPull && (
              <Field
                label={t("node.form.incoming_method")}
                hint={t("node.form.incoming_method_hint")}
                help={t("node.help.incoming_method")}
                className="mt-3"
              >
                <Select
                  value={form.incoming_method}
                  onChange={(e) =>
                    set("incoming_method", e.target.value as Form["incoming_method"])
                  }
                >
                  {HTTP_METHODS.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </Select>
              </Field>
            )}
            <Field label={t("node.form.state")} help={t("node.help.status")} className="mt-3">
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

          {isPull && (
            <RabbitMQSection
              form={form}
              // RMQFormFields — структурное подмножество Form с теми же типами
              // значений; дженерик-сеттер Form совместим, TS требует явный каст.
              set={set as RMQSetter}
              isNew={isNew}
              errField={errField}
              errMsg={error}
            />
          )}

          <Card>
            <SectionHead icon={<Globe className="h-4 w-4" />}>{t("node.form.target")}</SectionHead>
            {/* §27.11: для pull-узлов url_mode=from_request скрыт — нет входящего
                HTTP-запроса, из которого можно взять URL. Только статичный адрес. */}
            {!isPull && (
              <Field label={t("node.fields.url_mode")} help={t("node.help.url_mode")}>
                <PickGroup
                  value={form.url_mode}
                  onChange={(v) => set("url_mode", v)}
                  options={[
                    { value: "static", title: t("node.form.url_static"), description: t("node.form.url_static_desc") },
                    { value: "from_request", title: t("node.form.url_from_request"), description: t("node.form.url_from_request_desc") },
                  ]}
                />
              </Field>
            )}
            {(form.url_mode === "static" || isPull) && (
              <Field label={t("node.fields.target_url")} help={t("node.help.target_url")} className={isPull ? "" : "mt-3"}>
                <Input
                  mono
                  className={errCls("target_url")}
                  value={form.target_url}
                  onChange={(e) => set("target_url", e.target.value)}
                  placeholder="https://api.partner.com/hook"
                />
                {fieldErr("target_url")}
              </Field>
            )}
            {form.url_mode === "from_request" && !isPull && (
              <Field label={t("node.form.param_name")} help={t("node.help.url_param_name")} className="mt-3">
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
            {form.url_mode === "from_request" && !isPull && (
              <Field
                label={t("node.allowed_hosts.label")}
                hint={t("node.allowed_hosts.hint_short")}
                help={t("node.help.allowed_hosts")}
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
            {/* §39: path-passthrough — приклеивание хвоста входящего пути к target URL.
                Не применимо к pull-узлам (нет входящего HTTP-пути). */}
            {!isPull && (
              <div className="mt-3 border-t border-line pt-3">
                <div className="flex items-center gap-1">
                  <Toggle
                    checked={form.path_passthrough}
                    onChange={(v) => set("path_passthrough", v)}
                    label={t("node.form.path_passthrough")}
                  />
                  <LabelHint content={t("node.help.path_passthrough")} />
                </div>
                <p className="mt-1 text-xs text-fg-subtle">{t("node.form.path_passthrough_hint")}</p>
              </div>
            )}
            <Field
              label={t("node.form.outgoing_method")}
              hint={t("node.form.outgoing_method_hint")}
              help={t("node.help.outgoing_method")}
              className="mt-3"
            >
              <Select
                value={form.outgoing_method}
                onChange={(e) =>
                  set("outgoing_method", e.target.value as Form["outgoing_method"])
                }
              >
                {HTTP_METHODS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </Select>
            </Field>
            <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label={t("node.form.timeout_ms")} help={t("node.help.timeout_ms")}>
                <Input
                  type="number"
                  min={100}
                  max={300000}
                  className={errCls("timeout_ms")}
                  value={form.timeout_ms}
                  onChange={(e) => set("timeout_ms", parseNumInput(e.target.value, form.timeout_ms))}
                />
                {fieldErr("timeout_ms")}
              </Field>
              <Field label={t("node.form.retry_count")} help={t("node.help.retry_count")}>
                <Input
                  type="number"
                  min={0}
                  max={10}
                  className={errCls("retry_count")}
                  value={form.retry_count}
                  onChange={(e) => set("retry_count", parseNumInput(e.target.value, form.retry_count))}
                />
                {fieldErr("retry_count")}
              </Field>
            </div>
          </Card>

          <Card>
            <SectionHead icon={<Lock className="h-4 w-4" />}>{t("node.form.auth")}</SectionHead>
            {/* §27.11: «Входящая авторизация» скрыта для pull-узлов — входящих
                HTTP-запросов нет, некого авторизовывать. */}
            {!isPull && (
              <>
            <Field label={t("node.form.incoming")} help={t("node.help.incoming_auth")}>
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
                help={t("node.help.incoming_credentials")}
                className="mt-3"
              >
                <SecretInput
                  value={form.incoming_auth_credentials}
                  onChange={(e) => set("incoming_auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : t("node.form.keep_secret")}
                />
              </Field>
            )}
            {(form.incoming_auth_type === "basic" ||
              form.incoming_auth_type === "token") && (
              <Hint tone="muted" className="mt-2">
                {form.incoming_auth_type === "basic"
                  ? t("node.auth.basic_in_hint")
                  : t("node.auth.token_in_hint")}
              </Hint>
            )}
            {/* §41: источник креды клиента — заголовок (по умолчанию Authorization)
                или query-параметр — и имя поля (обязательно при query). */}
            {(form.incoming_auth_type === "basic" ||
              form.incoming_auth_type === "token") && (
              <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label={t("node.form.auth_source")} help={t("node.help.auth_source_in")}>
                  <Select
                    value={form.incoming_auth_dynamic_source}
                    onChange={(e) => {
                      const src = e.target.value;
                      set("incoming_auth_dynamic_source", src);
                      if (src === "query" && form.incoming_auth_dynamic_field === "Authorization") {
                        set("incoming_auth_dynamic_field", "");
                      } else if (src === "header" && form.incoming_auth_dynamic_field === "") {
                        set("incoming_auth_dynamic_field", "Authorization");
                      }
                    }}
                  >
                    <option value="header">{t("node.auth_source.header")}</option>
                    <option value="query">{t("node.auth_source.query")}</option>
                  </Select>
                </Field>
                <Field label={t("node.form.auth_field")} help={t("node.help.auth_field_in")}>
                  <RequestFieldField
                    value={form.incoming_auth_dynamic_field}
                    onChange={(v) => set("incoming_auth_dynamic_field", v)}
                    invalid={errField === "incoming_auth_dynamic_field"}
                  />
                  {fieldErr("incoming_auth_dynamic_field")}
                </Field>
              </div>
            )}
            {form.incoming_auth_type === "webhook_signature" && (
              <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label={t("node.form.sig_header")} help={t("node.help.sig_header")}>
                  <Input
                    mono
                    value={form.webhook_signature_header}
                    onChange={(e) => set("webhook_signature_header", e.target.value)}
                  />
                </Field>
                <Field label={t("node.form.sig_prefix")} help={t("node.help.sig_prefix")}>
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
              </>
            )}
            <Field label={t("node.form.outgoing")} help={t("node.help.outgoing_auth")} className={isPull ? "" : "mt-3"}>
              <Select value={form.auth_type} onChange={(e) => set("auth_type", e.target.value)}>
                <option value="none">none</option>
                <option value="basic">basic</option>
                <option value="token">token</option>
                {/* §27.11: динамическая авторизация (*_from_request) недоступна
                    для pull-узлов — её неоткуда брать. */}
                {!isPull && <option value="token_from_request">token_from_request</option>}
                {!isPull && <option value="basic_from_request">basic_from_request</option>}
              </Select>
            </Field>
            {(form.auth_type === "basic" || form.auth_type === "token") && (
              <Field label={t("node.form.credentials")} help={t("node.help.outgoing_credentials")} className="mt-3">
                <SecretInput
                  value={form.auth_credentials}
                  onChange={(e) => set("auth_credentials", e.target.value)}
                  placeholder={isNew ? "" : t("node.form.keep_secret")}
                />
              </Field>
            )}
            {(form.auth_type === "basic" || form.auth_type === "token") && (
              <Hint tone="muted" className="mt-2">
                {form.auth_type === "basic"
                  ? t("node.auth.basic_out_hint")
                  : t("node.auth.token_out_hint")}
              </Hint>
            )}
            {!isPull && form.auth_type === "token_from_request" && (
              <Hint tone="muted" className="mt-2">
                {t("node.auth.token_from_request_hint")}
              </Hint>
            )}
            {!isPull && form.auth_type === "basic_from_request" && (
              <Hint tone="muted" className="mt-2">
                {t("node.auth.basic_from_request_hint")}
              </Hint>
            )}
            {/* §41: источник креды (заголовок/параметр) + обязательное имя поля
                для динамических исходящих режимов. */}
            {!isPull &&
              (form.auth_type === "token_from_request" ||
                form.auth_type === "basic_from_request") && (
                <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label={t("node.form.auth_source")} help={t("node.help.auth_source_out")}>
                    <Select
                      value={form.auth_dynamic_source}
                      onChange={(e) => set("auth_dynamic_source", e.target.value)}
                    >
                      <option value="header">{t("node.auth_source.header")}</option>
                      <option value="query">{t("node.auth_source.query")}</option>
                      {/* body — legacy: показываем опцию только если узел уже на ней. */}
                      {form.auth_dynamic_source === "body" && <option value="body">body</option>}
                    </Select>
                  </Field>
                  <Field label={t("node.form.auth_field")} help={t("node.help.auth_field_out")}>
                    <RequestFieldField
                      value={form.auth_dynamic_field}
                      onChange={(v) => set("auth_dynamic_field", v)}
                      invalid={errField === "auth_dynamic_field"}
                    />
                    {fieldErr("auth_dynamic_field")}
                  </Field>
                </div>
              )}
          </Card>

          <Card>
            <SectionHead icon={<List className="h-4 w-4" />}>
              {t("node.form.headers")}
            </SectionHead>
            <Field label={t("node.form.forward_headers")} hint={t("node.form.forward_headers_hint")} help={t("node.help.forward_headers")}>
              <HeadersField
                value={form.forward_headers}
                onChange={(v) => set("forward_headers", v)}
              />
            </Field>
          </Card>

          <Card>
            <div className="mb-3 flex items-center justify-between">
              <SectionHead icon={<ListChecks className="h-4 w-4" />} className="mb-0">
                {t("node.form.logging")}
              </SectionHead>
              <div className="flex items-center gap-1">
                <Toggle
                  checked={form.logging_enabled}
                  onChange={(v) => set("logging_enabled", v)}
                  label={t("node.form.logging_enabled")}
                />
                <LabelHint content={t("node.help.logging_enabled")} />
              </div>
            </div>
            <fieldset
              disabled={!form.logging_enabled}
              className={form.logging_enabled ? "" : "pointer-events-none opacity-50"}
            >
              <Field label={t("node.fields.ch_template")} hint={t("node.fields.ch_template_hint")} help={t("node.fields.ch_template_hint")}>
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
              <Field label={t("node.fields.ch_table")} help={t("node.help.ch_table")} className="mt-3">
                <Input
                  mono
                  className={errCls("clickhouse_table")}
                  value={form.clickhouse_table}
                  onChange={(e) => set("clickhouse_table", e.target.value)}
                  placeholder="webhook_send"
                />
                {fieldErr("clickhouse_table")}
              </Field>
              <Field label={t("node.form.retention_days")} help={t("node.help.retention_days")} className="mt-3">
                <Input
                  type="number"
                  min={0}
                  value={form.clickhouse_retention_days}
                  onChange={(e) =>
                    set("clickhouse_retention_days", parseNumInput(e.target.value, form.clickhouse_retention_days))
                  }
                />
              </Field>
              <Field label={t("node.form.log_what")} help={t("node.help.log_what")} className="mt-3">
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
                <div className="flex items-center gap-1">
                  <Toggle
                    checked={form.max_body_size_enabled}
                    onChange={(v) => set("max_body_size_enabled", v)}
                    label={t("node.form.max_body_enabled")}
                  />
                  <LabelHint content={t("node.help.max_body")} />
                </div>
                <Field label={t("node.form.max_body_size")} hint={t("node.form.max_body_hint")} help={t("node.help.max_body")} className="mt-2">
                  <Input
                    type="number"
                    min={0}
                    className={errCls("max_body_size")}
                    disabled={!form.max_body_size_enabled}
                    value={form.max_body_size}
                    onChange={(e) => set("max_body_size", parseNumInput(e.target.value, form.max_body_size))}
                    placeholder="10000"
                  />
                  {fieldErr("max_body_size")}
                </Field>
              </div>
            </fieldset>
          </Card>

          {/* §36: авто-репроцессор DLQ — ОТДЕЛЬНЫЙ блок (не логирование, не внутри
              disabled-fieldset логирования). Только для async-узлов (у sync `request`
              нет очереди/DLQ). TTL в секундах + подсказка в часах; задержка повтора. */}
          {form.root_method !== "request" && (
            <Card>
              <SectionHead icon={<RefreshCw className="h-4 w-4" />}>
                {t("node.form.dlq_section")}
              </SectionHead>
              <Field label={t("node.form.dlq_ttl_seconds")} help={t("node.help.dlq_ttl")}>
                <Input
                  type="number"
                  min={60}
                  max={2592000}
                  className={errCls("dlq_ttl_seconds")}
                  value={form.dlq_ttl_seconds}
                  onChange={(e) => set("dlq_ttl_seconds", parseNumInput(e.target.value, form.dlq_ttl_seconds))}
                />
                <div className="mt-1 text-xs text-fg-muted">
                  ≈ {Math.round((form.dlq_ttl_seconds / 3600) * 10) / 10} {t("node.form.hours")}
                </div>
                {fieldErr("dlq_ttl_seconds")}
              </Field>
              <Field label={t("node.form.dlq_retry_delay_seconds")} help={t("node.help.dlq_retry_delay")} className="mt-3">
                <Input
                  type="number"
                  min={1}
                  max={86400}
                  className={errCls("dlq_retry_delay_seconds")}
                  value={form.dlq_retry_delay_seconds}
                  onChange={(e) =>
                    set("dlq_retry_delay_seconds", parseNumInput(e.target.value, form.dlq_retry_delay_seconds))
                  }
                />
                <div className="mt-1 text-xs text-fg-muted">{t("node.form.dlq_retry_delay_hint")}</div>
                {fieldErr("dlq_retry_delay_seconds")}
              </Field>
            </Card>
          )}

          {/* §29: комментарий-описание узла — отдельным блоком в самом низу. */}
          <Card>
            <SectionHead icon={<MessageSquare className="h-4 w-4" />}>
              {t("node.form.comment")}
            </SectionHead>
            <Field label={t("node.form.comment_label")} hint={t("node.form.comment_hint")} help={t("node.help.comment")}>
              <Textarea
                mono={false}
                rows={4}
                maxLength={2000}
                value={form.comment}
                onChange={(e) => set("comment", e.target.value)}
                placeholder={t("node.form.comment_placeholder")}
              />
            </Field>
          </Card>
        </div>

        <div className="space-y-3">
          <Card>
            <div className="mb-2.5 text-sm font-semibold">{t("node.form.preview")}</div>
            <div className="space-y-1 text-[12px] leading-7 text-fg-muted">
              {isPull ? (
                <>
                  <div>
                    <span className="rounded bg-warn/10 px-2 py-0.5 text-[11px] text-warn">RabbitMQ</span>{" "}
                    <span className="font-mono">{form.rmq_queue || "{queue}"}</span>
                  </div>
                  <div className="pl-2">↓ Puller (Receiver)</div>
                  <div className="pl-2">↓ Kafka (async)</div>
                </>
              ) : (
                <>
                  <div className="flex items-center gap-1.5">
                    <span className="rounded bg-accent/10 px-2 py-0.5 text-[11px] text-accent">
                      {form.incoming_method}
                    </span>
                    <span className="min-w-0 break-all font-mono">{fullAddress}</span>
                    <CopyButton value={fullAddress} />
                  </div>
                  <div className="pl-2">↓ Receiver</div>
                  <div className="pl-2">
                    ↓ {form.root_method === "request" ? "gRPC (sync)" : "Kafka (async)"}
                  </div>
                </>
              )}
              <div className="pl-2">↓ Sender</div>
              <div className="pl-2">
                <span className="rounded bg-bg-muted px-1.5 py-0.5 text-[11px] text-fg-muted">
                  {form.outgoing_method}
                </span>{" "}
                → <span className="font-mono text-fg">{form.target_url || "{target_url}"}</span>
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
