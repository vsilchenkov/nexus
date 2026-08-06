import { useEffect, useRef, useState } from "react";
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
  ArrowRightLeft,
} from "lucide-react";

import {
  api,
  isNotFound,
  type Node,
  type CHTemplate,
  type CHTableVerifyResult,
  type HostAllowlistEntry,
} from "../api/client";
import { useNodeUrlBuilder } from "../lib/nodeUrl";
import { useCurrentTeamCHDatabase, useMyTeams } from "../lib/teams";
import { useNodeFormDefaults } from "../lib/nodeDefaults";
import { useEnsureNodeTeam, useNodeTeam } from "../lib/nodeShare";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { parseNumInput } from "../lib/numField";
import { validateNodeForm } from "../lib/nodeValidation";
import { chSchemaChangeWontApply, chSyncFormDirty } from "../lib/chSchema";
import { buildVerifyMessage } from "../lib/chTableVerify";
import { useConfirm } from "../lib/confirm";
import { DryRunDialog } from "../components/DryRunDialog";
import { CHSchemaSyncDialog } from "../components/CHSchemaSyncDialog";
import { DeleteNodeDialog } from "../components/node/DeleteNodeDialog";
import { AllowedHostsField } from "../components/node/AllowedHostsField";
import { HeadersField } from "../components/node/HeadersField";
import { RequestFieldField } from "../components/node/RequestFieldField";
import { RabbitMQSection, type RMQSetter } from "../components/node/RabbitMQSection";
import { ShareNodeButton } from "../components/node/ShareNodeButton";
import { MoveNodeDialog } from "../components/node/MoveNodeDialog";
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
  // Для типа basic креды вводятся раздельно: Логин (открытый) + Пароль
  // (скрытый); перед отправкой склеиваются в auth_credentials
  // "login:password" (формат хранения не меняется). Логин prefill'ится из
  // API (auth_login), пароль никогда не приходит с сервера.
  auth_login: string;
  auth_password: string;
  incoming_auth_login: string;
  incoming_auth_password: string;
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
  external_table: boolean;
  dlq_ttl_seconds: number;
  dlq_retry_delay_seconds: number;
  circuit_breaker_threshold: number;
  circuit_breaker_cooldown_sec: number;
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
  auth_login: "",
  auth_password: "",
  incoming_auth_login: "",
  incoming_auth_password: "",
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
  external_table: false,
  dlq_ttl_seconds: 86400,
  dlq_retry_delay_seconds: 300,
  // §81.3: 0 = «как в конфигурации» — узел не переопределяет политику.
  circuit_breaker_threshold: 0,
  circuit_breaker_cooldown_sec: 0,
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

  // §58: шаренная ссылка на правку узла из другой команды тоже авто-переключает
  // сессию на команду узла. Пока не ready — узел не грузим (иначе 404).
  const ensure = useEnsureNodeTeam(id);

  // §65: строка «Команда» в сайдбаре — только имя. Для правки — резолвер §58
  // (ключ уже закеширован useEnsureNodeTeam, второго запроса нет), для
  // создания — текущая команда сессии из членств (узел будет создан в ней).
  const myTeams = useMyTeams();
  const nodeTeamQ = useNodeTeam(isNew ? undefined : id);
  const teamName = isNew
    ? myTeams.data?.items.find((m) => m.id === myTeams.data?.current_team_id)?.name
    : nodeTeamQ.data?.team_name;

  const existing = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !isNew && ensure.status === "ready",
  });

  const [form, setForm] = useState<Form>(emptyForm);
  // §23: SSRF-плашка в сайдбаре нужна только при ПУСТОМ allowlist. Список
  // существующего узла берём тем же queryKey, что AllowedHostsField, — кеш
  // общий, запрос не дублируется, а attach/detach в поле инвалидируют ключ,
  // и плашка исчезает/появляется реактивно.
  const nodeHosts = useQuery({
    queryKey: ["node-hosts", id],
    queryFn: () => api.get<{ items: HostAllowlistEntry[] }>(`/api/nodes/${id}/allowed-hosts`),
    enabled: !isNew && form.url_mode === "from_request",
  });
  // §23: для нового узла выбранные хосты копятся локально и привязываются после
  // создания (allowlist — производный снимок каталога, управляется link/unlink).
  const [pendingHosts, setPendingHosts] = useState<HostAllowlistEntry[]>([]);
  // Для существующего узла — строго isSuccess (не «?? []»), чтобы плашка не
  // мигала, пока список ещё грузится.
  const allowlistEmpty = isNew
    ? pendingHosts.length === 0
    : nodeHosts.isSuccess && (nodeHosts.data.items ?? []).length === 0;
  const [showDryRun, setShowDryRun] = useState(false);
  const [showChSync, setShowChSync] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [showMove, setShowMove] = useState(false);
  // Перенос узла между командами — admin-only (как и сам /move-эндпоинт):
  // viewer/manager кнопку не видят, иначе клик упрётся в 403.
  const canMove = useRoleAtLeast("admin");
  const confirm = useConfirm();
  // §56: «Синхронизировать схему» по грязной форме → предложить сохранить и,
  // при согласии, открыть диалог по уже сохранённым настройкам. Флаг переживает
  // async-сохранение и читается в save.onSuccess (там иначе идёт navigate).
  const syncAfterSaveRef = useRef(false);
  const [error, setError] = useState<string | null>(null);
  // §28 Пункт 5: имя поля с ошибкой валидации (для inline-подсветки) из
  // ответа { error, code, field } бэкенда.
  const [errField, setErrField] = useState<string | null>(null);
  // §64: ошибка САМОГО вызова проверки таблицы (400/503) — в отличие от
  // расхождений структуры, которые приходят с 200 в теле ответа.
  const [verifyError, setVerifyError] = useState<string | null>(null);

  const templates = useQuery({
    queryKey: ["ch-templates"],
    queryFn: () => api.get<{ items: CHTemplate[] }>("/api/ch-templates"),
  });

  // Форма заполняется серверными данными ОДИН раз — при первой загрузке узла.
  // Иначе любой фоновый рефетч (refetchOnWindowFocus: ушёл в другое окно и
  // вернулся; invalidate при смене команды) затирал несохранённые правки.
  const filledRef = useRef(false);
  useEffect(() => {
    if (existing.data && !filledRef.current) {
      filledRef.current = true;
      setForm({ ...emptyForm, ...(existing.data as unknown as Form) });
    }
  }, [existing.data]);

  // §64: дефолты формы СОЗДАНИЯ узла приходят асинхронно (список шаблонов,
  // членства команд, публичные настройки). Каждый подставляется ровно один раз
  // и только пока поле в исходном значении — иначе медленный ответ затёр бы то,
  // что оператор уже успел ввести.
  const chDatabase = useCurrentTeamCHDatabase();
  const nodeDefaults = useNodeFormDefaults();
  const defaultTemplateId = templates.data?.items.find((tpl) => tpl.is_default)?.id ?? "";
  const templateSeededRef = useRef(false);
  const tableSeededRef = useRef(false);
  const bodySizeSeededRef = useRef(false);

  useEffect(() => {
    if (!isNew || templateSeededRef.current || !defaultTemplateId) return;
    templateSeededRef.current = true;
    // Шаблон по умолчанию, а не «(нет / ручная таблица)»: типовой узел должен
    // получать таблицу, которой Nexus управляет.
    setForm((p) => (p.clickhouse_template_id === "" && !p.external_table
      ? { ...p, clickhouse_template_id: defaultTemplateId }
      : p));
  }, [isNew, defaultTemplateId]);

  useEffect(() => {
    if (!isNew || tableSeededRef.current || !chDatabase) return;
    tableSeededRef.current = true;
    // Префикс БД команды подставляется РЕДАКТИРУЕМЫМ: оператору остаётся дописать
    // имя таблицы, но для внешней таблицы префикс можно стереть — она вправе
    // жить в чужой БД.
    setForm((p) => (p.clickhouse_table === "" ? { ...p, clickhouse_table: `${chDatabase}.` } : p));
  }, [isNew, chDatabase]);

  useEffect(() => {
    if (!isNew || bodySizeSeededRef.current || !nodeDefaults.maxBodySize) return;
    bodySizeSeededRef.current = true;
    setForm((p) => (!p.max_body_size_enabled && p.max_body_size === 0
      ? { ...p, max_body_size_enabled: true, max_body_size: nodeDefaults.maxBodySize }
      : p));
  }, [isNew, nodeDefaults.maxBodySize]);

  // Узел чужой команды (§18): переключили команду, не выходя из формы. Редирект
  // здесь НЕ делаем — он уничтожил бы несохранённые правки; показываем баннер и
  // блокируем сохранение (оно всё равно вернёт 404 — team-scope в handler'е).
  const foreignTeam = isNotFound(existing.error);

  // buildPayload — форма → тело запроса API. Для basic склеивает Логин+Пароль
  // в auth_credentials "login:password" (формат хранения); пустой пароль →
  // пустые креды = «оставить старые» (контракт Update). Вспомогательные поля
  // *_login/*_password в API не уходят (бэкенд их не знает).
  function buildPayload(): Record<string, unknown> {
    const { auth_login, auth_password, incoming_auth_login, incoming_auth_password, ...rest } =
      form;
    const p: Record<string, unknown> = { ...rest };
    if (form.auth_type === "basic") {
      p.auth_credentials = auth_password ? `${auth_login}:${auth_password}` : "";
    }
    if (form.incoming_auth_type === "basic") {
      p.incoming_auth_credentials = incoming_auth_password
        ? `${incoming_auth_login}:${incoming_auth_password}`
        : "";
    }
    // §64: шаблон и внешняя таблица взаимоисключающи (бэкенд отвергает пару).
    // Страховка на случай, если галка осталась от прежнего выбора «ручная».
    if (form.clickhouse_template_id) p.external_table = false;
    return p;
  }

  // §64: «ручная таблица» = шаблон не выбран. Только в этом состоянии осмысленны
  // галка «Внешняя таблица» и проверка её структуры.
  const isManualTable = form.clickhouse_template_id === "";

  // onTemplateChange — выбор шаблона означает «таблицей управляет Nexus», поэтому
  // снимает признак внешней; возврат к «ручной» ставит его обратно (типовой
  // сценарий ручной таблицы — посторонний писатель).
  function onTemplateChange(templateId: string) {
    setForm((p) => ({ ...p, clickhouse_template_id: templateId, external_table: templateId === "" }));
    setErrField((f) => (f === "clickhouse_template_id" ? null : f));
    verify.reset();
  }

  // §64: проверка структуры таблицы. Расхождения приходят с HTTP 200 и ok=false —
  // это результат проверки, а не ошибка запроса; ошибкой считается только отказ
  // самого вызова (кривое имя → 400, ClickHouse недоступен → 503).
  const verify = useMutation({
    mutationFn: (table: string) => api.post<CHTableVerifyResult>("/api/ch-tables/verify", { table }),
    onError: (e: { response?: { data?: { code?: string; error?: string } } }) => {
      const d = e?.response?.data;
      setVerifyError(d?.code ? t(d.code) : (d?.error ?? t("common.error")));
    },
    onMutate: () => setVerifyError(null),
  });

  const save = useMutation({
    mutationFn: async () => {
      const payload = buildPayload();
      if (!isNew) return api.put(`/api/nodes/${id}`, payload);
      // §23: создаём узел, затем привязываем выбранные паттерны к нему.
      const node = await api.post<Node>("/api/nodes", payload);
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
      // §56: «Сохранить и синхронизировать» — остаёмся на форме и открываем
      // диалог синхронизации по уже сохранённым настройкам (без сброса формы и
      // без navigate). filledRef не даёт рефетчу затереть форму.
      if (syncAfterSaveRef.current && nodeId && !isNew) {
        syncAfterSaveRef.current = false;
        setShowChSync(true);
        return;
      }
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

  // runSave — клиентская валидация лимитов (зеркало domain.Node.Validate) до
  // запроса: те же i18n-коды node.validation.*, что возвращает backend. Куда
  // перейти после успеха (navigate или открыть синхронизацию) решает
  // save.onSuccess по syncAfterSaveRef. Возвращает false, если валидация не прошла.
  function runSave(): boolean {
    const v = validateNodeForm(form, {
      // Оригинальные логины для правила «сменил логин — введи пароль заново»:
      // сервер не может пересобрать строку "login:password", не зная пароля.
      // Для нового узла оригинал пуст: логин без пароля тоже ошибка.
      orig_auth_login: existing.data?.auth_login ?? "",
      orig_incoming_auth_login: existing.data?.incoming_auth_login ?? "",
    });
    if (v) {
      setErrField(v.field);
      setError(t(v.code));
      return false;
    }
    setError(null);
    setErrField(null);
    save.mutate();
    return true;
  }

  function submit() {
    runSave();
  }

  // onSyncClick — §56: план синхронизации строится по СОХРАНЁННОМУ узлу (эндпоинт
  // берёт только id, значения формы туда не уходят). При несохранённых CH-правках
  // предлагаем сохранить и продолжить; отказ — диалог не открываем.
  async function onSyncClick() {
    if (chSyncDirty) {
      const ok = await confirm({
        title: t("ch_sync.save_first_title"),
        message: t("ch_sync.save_first_message"),
        confirmLabel: t("ch_sync.save_and_sync"),
      });
      if (!ok) return;
      syncAfterSaveRef.current = true;
      if (!runSave()) syncAfterSaveRef.current = false; // валидация не прошла — откат
      return;
    }
    setShowChSync(true);
  }

  const isPull = form.root_method === "RabbitMQAsync";
  const verb = form.root_method === "request" ? "request" : "requestAsync";
  // C.1: смена CH-шаблона у существующего узла БЕЗ смены имени таблицы молча
  // не применяется — таблица создаётся один раз (CREATE TABLE IF NOT EXISTS), и
  // новые кодеки/индексы/движок к ней не приезжают (нет ALTER). Предупреждаем
  // честно: чтобы применить схему, нужно новое имя таблицы или ALTER вручную.
  // Retention (дни) — исключение: применяется housekeeping'ом автоматически.
  const savedNode = existing.data as unknown as Form | undefined;
  const chSchemaWontApply = chSchemaChangeWontApply({
    isNew,
    loggingEnabled: form.logging_enabled,
    currentTemplateId: form.clickhouse_template_id,
    currentTable: form.clickhouse_table,
    saved: savedNode,
  });
  // §56: есть ли несохранённые CH-правки (шаблон/таблица/retention) — от этого
  // зависит, предложит ли onSyncClick сохранить перед синхронизацией.
  const chSyncDirty = chSyncFormDirty({
    isNew,
    currentTemplateId: form.clickhouse_template_id,
    currentTable: form.clickhouse_table,
    currentRetentionDays: form.clickhouse_retention_days,
    currentExternalTable: form.external_table,
    saved: savedNode,
  });
  // §64: текст под кнопкой «Проверить». Ошибка вызова важнее результата: она
  // означает, что проверка не состоялась вовсе.
  const verifyMessage = buildVerifyMessage(verifyError, verify.data, t);
  // §28 Пункт 1: полный адрес собирается из публичного адреса приложения
  // (если задан в настройках) или origin браузера + slug текущей команды.
  const buildUrl = useNodeUrlBuilder();
  const address = buildUrl(verb, form.path);

  // §26/§28 Пункт 3: форму узла (с полями авторизации) открывает только
  // manager+. viewer перенаправляется на просмотр/список.
  const canEdit = useRoleAtLeast("manager");
  if (!canEdit) {
    return <Navigate to={isNew ? "/" : `/nodes/${id}`} replace />;
  }

  // §58, п.3: правка узла, чья команда пользователю недоступна.
  if (!isNew && ensure.status === "unavailable") {
    return <div className="mx-auto max-w-6xl text-fg-muted">{t("node.unavailable")}</div>;
  }
  // §58, п.2: пока резолвим/переключаем команду узла — грузимся (форма не мигает).
  if (!isNew && ensure.status !== "ready") {
    return <div className="mx-auto max-w-6xl text-fg-muted">{t("common.loading")}</div>;
  }

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex items-center justify-between gap-3">
        {/* Заголовок усекается, группа действий закреплена справа (shrink-0) —
            кнопки всегда в один ряд даже при длинном пути узла. */}
        <h1
          className="min-w-0 truncate text-lg font-semibold"
          title={isNew ? undefined : `${t("node.actions.edit")}: ${form.path}`}
        >
          {isNew ? t("overview.new_node") : `${t("node.actions.edit")}: ${form.path}`}
        </h1>
        <div className="flex shrink-0 items-center gap-2">
          <Link to={isNew ? "/" : `/nodes/${id}`}>
            <Button variant="ghost">{t("common.cancel")}</Button>
          </Link>
          {/* Перенос узла в другую команду — из формы правки (раньше жил кнопкой
              в списке узлов, где его место занял мини-график трафика). */}
          {!isNew && canMove && existing.data && (
            <Button variant="ghost" onClick={() => setShowMove(true)}>
              <ArrowRightLeft className="h-4 w-4" /> {t("overview.move.action")}
            </Button>
          )}
          {/* §58, п.4: «Поделиться» выводится и в форме правки (у нового узла нет id). */}
          {!isNew && id && <ShareNodeButton nodeId={id} />}
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
            disabled={save.isPending || form.path.trim() === "" || foreignTeam}
            onClick={submit}
          >
            <Save className="h-4 w-4" /> {t("common.save")}
          </Button>
        </div>
      </div>

      {/* §18: узел остался в другой команде — сохранить нельзя (сервер вернёт
          404), но правки на экране сохраняем: уводить со страницы без спроса
          нельзя. */}
      {foreignTeam && <ErrorAlert>{t("node.foreign_team")}</ErrorAlert>}

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
                    {address.short}
                  </span>
                  <CopyButton value={address.short} />
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
                  max={600000}
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
            {/* §81.3: политика защиты узла. Пусто (0) = «как в конфигурации» —
                у узла со штатным ответом в 20 секунд и у узла с ответом в 200 мс
                разная норма отказов. Поля есть у всех типов узлов: защита общая
                для sync и async. */}
            <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field
                label={t("node.form.circuit_breaker_threshold")}
                help={t("node.help.circuit_breaker_threshold")}
              >
                <Input
                  type="number"
                  min={0}
                  max={100}
                  placeholder={t("node.form.breaker_default_placeholder")}
                  className={errCls("circuit_breaker_threshold")}
                  value={form.circuit_breaker_threshold || ""}
                  onChange={(e) =>
                    set("circuit_breaker_threshold", parseNumInput(e.target.value, 0))
                  }
                />
                {fieldErr("circuit_breaker_threshold")}
              </Field>
              <Field
                label={t("node.form.circuit_breaker_cooldown_sec")}
                help={t("node.help.circuit_breaker_cooldown")}
              >
                <Input
                  type="number"
                  min={0}
                  max={3600}
                  placeholder={t("node.form.breaker_default_placeholder")}
                  className={errCls("circuit_breaker_cooldown_sec")}
                  value={form.circuit_breaker_cooldown_sec || ""}
                  onChange={(e) =>
                    set("circuit_breaker_cooldown_sec", parseNumInput(e.target.value, 0))
                  }
                />
                {fieldErr("circuit_breaker_cooldown_sec")}
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
            {/* basic — раздельные Логин (открытый) + Пароль (скрытый);
                перед отправкой склеиваются в "login:password" (buildPayload).
                Пустой пароль при неизменном логине = оставить старые креды. */}
            {form.incoming_auth_type === "basic" && (
              <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label={t("auth.login_field")} help={t("node.help.basic_login")}>
                  <Input
                    mono
                    autoComplete="off"
                    className={errCls("incoming_auth_login")}
                    value={form.incoming_auth_login}
                    onChange={(e) => set("incoming_auth_login", e.target.value)}
                  />
                  {fieldErr("incoming_auth_login")}
                </Field>
                <Field label={t("auth.password_field")} help={t("node.help.basic_password")}>
                  <SecretInput
                    className={errCls("incoming_auth_password")}
                    value={form.incoming_auth_password}
                    onChange={(e) => set("incoming_auth_password", e.target.value)}
                    placeholder={isNew ? "" : t("node.form.keep_secret")}
                  />
                  {fieldErr("incoming_auth_password")}
                </Field>
              </div>
            )}
            {form.incoming_auth_type !== "none" && form.incoming_auth_type !== "basic" && (
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
            {form.auth_type === "basic" && (
              <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label={t("auth.login_field")} help={t("node.help.basic_login")}>
                  <Input
                    mono
                    autoComplete="off"
                    className={errCls("auth_login")}
                    value={form.auth_login}
                    onChange={(e) => set("auth_login", e.target.value)}
                  />
                  {fieldErr("auth_login")}
                </Field>
                <Field label={t("auth.password_field")} help={t("node.help.basic_password")}>
                  <SecretInput
                    className={errCls("auth_password")}
                    value={form.auth_password}
                    onChange={(e) => set("auth_password", e.target.value)}
                    placeholder={isNew ? "" : t("node.form.keep_secret")}
                  />
                  {fieldErr("auth_password")}
                </Field>
              </div>
            )}
            {form.auth_type === "token" && (
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
                  onChange={(e) => onTemplateChange(e.target.value)}
                >
                  <option value="">{t("node.fields.ch_template_manual")}</option>
                  {templates.data?.items.map((tpl) => (
                    <option key={tpl.id} value={tpl.id}>
                      {tpl.name}
                      {tpl.is_default ? " ★" : ""}
                    </option>
                  ))}
                </Select>
                {fieldErr("clickhouse_template_id")}
                {/* §64: ручная таблица → выбор, ведёт её Nexus или сам оператор.
                    По умолчанию внешняя: ручную таблицу обычно и заводят под
                    постороннего писателя. */}
                {isManualTable && (
                  <div className="mt-2 flex items-center gap-1">
                    <Toggle
                      checked={form.external_table}
                      onChange={(v) => set("external_table", v)}
                      label={t("node.fields.external_table")}
                    />
                    <LabelHint content={t("node.help.external_table")} />
                  </div>
                )}
                {chSchemaWontApply && (
                  <div className="mt-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
                    {t("node.help.ch_schema_change_warning")}
                  </div>
                )}
              </Field>
              <Field label={t("node.fields.ch_table")} help={t("node.help.ch_table")} className="mt-3">
                <Input
                  mono
                  className={errCls("clickhouse_table")}
                  value={form.clickhouse_table}
                  onChange={(e) => {
                    set("clickhouse_table", e.target.value);
                    verify.reset(); // результат относится к прежнему имени
                  }}
                  placeholder="webhook_send"
                />
                {fieldErr("clickhouse_table")}
                {/* §64: структуру внешней таблицы Nexus не правит, поэтому её
                    пригодность оператор проверяет явно — до сохранения узла
                    (работает и для несохранённого: проверяем имя, не id). */}
                {isManualTable && form.clickhouse_table.trim() !== "" && (
                  <div className="mt-2">
                    <button
                      type="button"
                      onClick={() => verify.mutate(form.clickhouse_table.trim())}
                      disabled={verify.isPending}
                      className="flex items-center gap-1.5 text-xs text-accent hover:underline disabled:opacity-60 disabled:hover:no-underline"
                    >
                      <ListChecks className="h-3.5 w-3.5" />
                      {verify.isPending ? t("common.loading") : t("node.verify.button")}
                    </button>
                    {verifyMessage && (
                      <p className={`mt-1 text-xs ${verifyMessage.ok ? "text-ok" : "text-err"}`}>
                        {verifyMessage.text}
                      </p>
                    )}
                  </div>
                )}
              </Field>
              {/* §64: у внешней таблицы retention не применяется — партиции
                  дропает владелец таблицы, не Nexus. Поле скрываем, чтобы не
                  обещать несуществующего поведения. */}
              {!form.external_table && (
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
              )}
              {/* §56: применить настройки схемы CH к УЖЕ существующей таблице
                  через ALTER (предпросмотр + явное применение). Только у
                  сохранённого узла с таблицей; внешнюю таблицу Nexus не альтерит
                  (бэкенд вернул бы 409). */}
              {!isNew && form.clickhouse_table && !form.external_table && (
                <button
                  type="button"
                  onClick={() => void onSyncClick()}
                  className="mt-3 flex items-center gap-1.5 text-xs text-accent hover:underline"
                >
                  <RefreshCw className="h-3.5 w-3.5" /> {t("ch_sync.button")}
                </button>
              )}
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
                    <span className="break-all font-mono">{form.rmq_queue || "{queue}"}</span>
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
                    <span className="min-w-0 break-all font-mono">{address.short}</span>
                    <CopyButton value={address.short} />
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
                → <span className="break-all font-mono text-fg">{form.target_url || "{target_url}"}</span>
              </div>
            </div>
          </Card>
          {/* §65: команда узла и даты §63 — ОТДЕЛЬНАЯ карточка под
              «Предпросмотром», строки в формате вкладки «Конфиг» (лейбл слева,
              разделители, 13px). Даты — только у сохранённого узла;
              «Обновлено» — только если узел реально меняли (updated_at ≠
              created_at); пустой автор не выводится. */}
          <Card>
            <dl className="divide-y divide-line text-[13px]">
              <div className="grid grid-cols-[96px_1fr] gap-3 py-2.5 first:pt-0 last:pb-0">
                <dt className="text-fg-muted">{t("node.fields.team")}</dt>
                <dd className="font-medium">{teamName ?? "—"}</dd>
              </div>
              {!isNew && existing.data && (
                <>
                  <div className="grid grid-cols-[96px_1fr] gap-3 py-2.5 first:pt-0 last:pb-0">
                    <dt className="text-fg-muted">{t("common.created_at")}</dt>
                    <dd>
                      {new Date(existing.data.created_at).toLocaleString()}
                      {existing.data.created_by && (
                        <span className="block text-fg-subtle">
                          {t("common.author")}:{" "}
                          <span className="font-mono">{existing.data.created_by}</span>
                        </span>
                      )}
                    </dd>
                  </div>
                  {existing.data.updated_at !== existing.data.created_at && (
                    <div className="grid grid-cols-[96px_1fr] gap-3 py-2.5 first:pt-0 last:pb-0">
                      <dt className="text-fg-muted">{t("common.updated_at")}</dt>
                      <dd>
                        {new Date(existing.data.updated_at).toLocaleString()}
                        {existing.data.updated_by && (
                          <span className="block text-fg-subtle">
                            {t("common.author")}:{" "}
                            <span className="font-mono">{existing.data.updated_by}</span>
                          </span>
                        )}
                      </dd>
                    </div>
                  )}
                </>
              )}
            </dl>
          </Card>
          {form.url_mode === "from_request" && allowlistEmpty && (
            <Hint tone="danger" icon={<ShieldAlert className="h-4 w-4" />}>
              {t("node.form.ssrf_warn")}
            </Hint>
          )}
        </div>
      </div>

      {/* §55.6: при редактировании поле кредов пустое по смыслу («оставить
          старое»), поэтому id обязателен — иначе реальный тест ушёл бы без
          авторизации. При создании (isNew) сохранённого конфига нет. */}
      {showDryRun && (
        <DryRunDialog node={form} nodeId={id} onClose={() => setShowDryRun(false)} />
      )}
      {showChSync && id && (
        <CHSchemaSyncDialog nodeId={id} onClose={() => setShowChSync(false)} />
      )}
      {showDelete && existing.data && (
        <DeleteNodeDialog node={existing.data} onClose={() => setShowDelete(false)} />
      )}
      {/* После переноса узел уходит в другую команду и текущая форма правки
          становится чужой (сервер вернёт 404) — уводим на список узлов. */}
      {showMove && existing.data && (
        <MoveNodeDialog
          node={existing.data}
          onClose={() => setShowMove(false)}
          onMoved={() => navigate("/")}
        />
      )}
    </div>
  );
}
