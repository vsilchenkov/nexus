import { useParams, Link, useSearchParams, useNavigate } from "react-router-dom";
import {
  useMutation,
  useQuery,
  useQueryClient,
  useIsFetching,
  type Query,
} from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Copy as CopyIcon, Pencil, Play, RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { api, isNotFound, type Node } from "../api/client";
import { Button, Chip, Field, Input, Pill, type LogsRange } from "../components/ui";
import { Modal } from "../components/ui/Modal";
import { cn } from "../lib/cn";
import { validateNodePath } from "../lib/nodeValidation";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { useEnsureNodeTeam } from "../lib/nodeShare";
import {
  isKnownNodeTab,
  parseLogsInitialFilter,
  parseNodeTab,
  withFailedLogs,
  withLogsWindow,
  withNodeTab,
  type NodeTab,
} from "../lib/nodeTabUrl";
import { ShareNodeButton } from "../components/node/ShareNodeButton";
import { NodeRuntimeBadge } from "../components/node/NodeRuntimeBadge";
import { DryRunDialog } from "../components/DryRunDialog";
import { LogsTab } from "../components/node/LogsTab";
import { OverviewTab } from "../components/node/OverviewTab";
import { ConfigTab } from "../components/node/ConfigTab";
import { MetricsTab } from "../components/node/MetricsTab";
import { QueueTab } from "../components/node/QueueTab";

type Tab = NodeTab;

const BASE_TABS: Tab[] = ["overview", "logs", "config", "metrics"];

export default function NodeDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  // §79.3: активная вкладка и окно журнала — производные АДРЕСА, а не состояния.
  // Так ссылка на вкладку пересылается и открывается в новом окне, а «Назад»
  // возвращает предыдущую вкладку, ничего не рассинхронизировав.
  // §47.4/§33.4: дип-линк со спарклайна дашборда — ?tab=logs&from&to (ms).
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseNodeTab(searchParams.get("tab"));
  const logsFilter = parseLogsInitialFilter(searchParams);

  // Нормализация мусорного ?tab=zzz — replace: чинить адрес в истории незачем,
  // а вот переключения вкладок пользователем в историю идут (push ниже).
  const rawTab = searchParams.get("tab");
  useEffect(() => {
    if (rawTab !== null && !isKnownNodeTab(rawTab)) {
      setSearchParams((prev) => withNodeTab(prev, "overview"), { replace: true });
    }
  }, [rawTab, setSearchParams]);
  // §26/§28 Пункт 3: редактирование узла — только manager+ (viewer не видит креды).
  const canEdit = useRoleAtLeast("manager");
  // §53: диалог «Скопировать узел» (manager+, как создание).
  const [copyOpen, setCopyOpen] = useState(false);
  // §55.2: тестовый запрос по сохранённому узлу, не заходя в редактирование.
  const [dryRunOpen, setDryRunOpen] = useState(false);
  // Ручное обновление: перезагружаем данные ЭТОЙ страницы — шапку узла и
  // текущую вкладку. Фильтр по id: у всех запросов страницы он идёт вторым
  // элементом ключа (["node", id], ["node-metrics", id, …], ["logs", id, …],
  // ["log", id, logId], ["log-methods", id], ["aq-*", id, …]).
  //
  // Раньше и счётчик, и инвалидация были глобальными (без фильтра): иконка
  // крутилась от любого фонового запроса в приложении (метрики шапки, поллинг
  // соседних страниц), а клик перезагружал кеш всего SPA.
  const qc = useQueryClient();
  const navigate = useNavigate();
  const nodeQueries = useMemo(
    () => ({ predicate: (q: Query) => q.queryKey[1] === id }),
    [id],
  );
  const fetching = useIsFetching(nodeQueries);

  // §58: гарантируем, что текущая команда сессии = команде узла (авто-переключение
  // при открытии шаренной ссылки). Пока не ready — сам узел не грузим.
  const ensure = useEnsureNodeTeam(id);

  // openLogsAt — переход на вкладку логов с временным окном бакета (§33.4).
  // Одна запись в адрес: вкладка и окно уезжают вместе, промежуточного рендера
  // «уже логи, но ещё без фильтра» не возникает.
  const openLogsAt = (r: LogsRange) => setSearchParams((prev) => withLogsWindow(prev, r));

  const nodeQ = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    // §58: узел грузим только когда его команда стала активной — иначе
    // team-scoped GET отдал бы 404 ещё до авто-переключения.
    enabled: !!id && ensure.status === "ready",
    // §27: для RabbitMQAsync live-обновляем health-снимок (queue depth/degraded).
    // Пока узел недоступен (напр. 404 после смены команды) — не поллим: иначе
    // цикл «поллинг → 404» крутится вечно.
    refetchInterval: (q) =>
      !q.state.error && (q.state.data as Node | undefined)?.root_method === "RabbitMQAsync"
        ? 5000
        : false,
  });

  const node = nodeQ.data;

  // Узел чужой команды (§18): после переключения команды в шапке GET отдаёт 404,
  // но react-query держит последние успешные data — без этой ветки страница
  // продолжала показывать узел ЧУЖОЙ команды со старыми данными, без ошибки и
  // редиректа (проверка `!node` ниже для этого случая не срабатывает).
  const notFound = isNotFound(nodeQ.error);
  useEffect(() => {
    if (notFound) navigate("/", { replace: true });
  }, [notFound, navigate]);

  // §58, п.3: узла нет или его команда пользователю недоступна.
  if (ensure.status === "unavailable")
    return <div className="text-fg-muted">{t("node.unavailable")}</div>;
  // §58, п.2: пока резолвим команду узла и переключаемся на неё — грузимся.
  if (ensure.status !== "ready") return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (nodeQ.isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (notFound) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (!node) return <div className="text-err">{t("node.not_found")}</div>;

  const statusTone = node.status === "enabled" ? "ok" : node.status === "paused" ? "warn" : "err";
  const isPull = node.root_method === "RabbitMQAsync";
  const rmq = node.rmq_status;
  // §34.4 + §69.1: вкладка доступна для всех типов узлов. У async-узлов это
  // управление очередью Kafka + неудачные доставки, у sync — только неудачные
  // доставки (ClickHouse done=0): без вкладки их нельзя было ни повторить, ни
  // очистить из UI, приходилось временно менять тип узла на requestAsync.
  const tabs: Tab[] = [...BASE_TABS, "queue"];

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex items-center gap-3">
        {/* Заголовок+бейджи усекаются (min-w-0 truncate), группа действий закреплена
            справа (shrink-0) — кнопки всегда в один ряд и не обрезаются даже при
            длинном пути узла (полный путь — в tooltip и предпросмотре «Конфиг»). */}
        <div className="flex min-w-0 items-center gap-3">
          <h1 className="min-w-0 truncate font-mono text-xl font-semibold" title={node.path}>
            {node.path}
          </h1>
          <Chip>{node.root_method}</Chip>
          <Pill tone={statusTone}>{t(`node.status.${node.status}`)}</Pill>
          {/* §84.7: рядом с КОНФИГУРАЦИОННЫМ статусом — RUNTIME-исход
              последнего вызова. Раньше «Активен» стоял и у узла, к которому
              доставка не проходит вовсе. */}
          <NodeRuntimeBadge nodeId={node.id} />
          {isPull && rmq?.degraded && <Pill tone="err">{t("node.rmq.degraded")}</Pill>}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          <Button
            sm
            variant="ghost"
            disabled={fetching > 0}
            onClick={() => void qc.invalidateQueries(nodeQueries)}
            title={t("node.actions.refresh")}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", fetching > 0 && "animate-spin")} />{" "}
            {t("node.actions.refresh")}
          </Button>
          {/* §58, п.1/п.4: «Поделиться» доступна всем ролям (viewer тоже). */}
          <ShareNodeButton nodeId={node.id} tab={tab} search={searchParams} sm />
          {canEdit && (
            <>
              <Button sm variant="ghost" onClick={() => setDryRunOpen(true)}>
                <Play className="h-3.5 w-3.5" /> {t("node.actions.dry_run")}
              </Button>
              <Button sm variant="ghost" onClick={() => setCopyOpen(true)}>
                <CopyIcon className="h-3.5 w-3.5" /> {t("node.actions.copy")}
              </Button>
              <Link to={`/nodes/${node.id}/edit`}>
                <Button sm variant="primary">
                  <Pencil className="h-3.5 w-3.5" /> {t("node.actions.edit")}
                </Button>
              </Link>
            </>
          )}
        </div>
      </div>

      {copyOpen && <CopyNodeDialog node={node} onClose={() => setCopyOpen(false)} />}

      {/* §55.2: сохранённый узел тестируется как есть. node_id обязателен —
          по нему сервер подмешает креды, наружу они не отдаются (§55.6). */}
      {dryRunOpen && (
        <DryRunDialog node={node} nodeId={node.id} onClose={() => setDryRunOpen(false)} />
      )}

      {isPull && rmq && (
        <div
          className={cn(
            "rounded-md border px-4 py-3",
            rmq.degraded ? "border-err/40 bg-err/10" : "border-line bg-bg-soft",
          )}
        >
          {rmq.degraded ? (
            <div className="text-sm text-err">
              <div className="font-semibold">{t("node.rmq.test_fail")}</div>
              <div className="mt-1 text-fg-muted">{rmq.reason || t("node.rmq.degraded_desc")}</div>
              <div className="mt-1 text-[11px] text-fg-subtle">
                {rmq.attempts} {t("node.rmq.attempts")}
              </div>
            </div>
          ) : (
            <div className="flex flex-wrap gap-6 text-sm">
              <Kpi label={t("node.rmq.queue_depth")} value={String(rmq.queue_depth)} />
              <Kpi label="consumers" value={String(rmq.consumer_count)} />
              <Kpi
                label="connection"
                value={rmq.connection_state}
                tone={rmq.connection_state === "up" ? "ok" : "warn"}
              />
            </div>
          )}
        </div>
      )}

      {/* §79.3: вкладки — НАСТОЯЩИЕ ссылки (<a href>), а не кнопки. С кнопкой
          браузеру нечего открывать: контекстное меню «Открыть в новой вкладке»,
          Ctrl/Cmd+клик и клик средней кнопкой не работают вовсе, хотя адрес у
          вкладки теперь есть. Link рисует href и при обычном клике остаётся
          SPA-навигацией (push — «Назад» возвращает предыдущую вкладку). */}
      <div className="flex gap-1 border-b border-line">
        {tabs.map((tb) => (
          <Link
            key={tb}
            // Только search: pathname у вкладок общий, а чужие параметры адреса
            // обязаны выживать (правило §54). Уход с «Логов» чистит окно
            // журнала внутри withNodeTab.
            to={{ search: `?${withNodeTab(searchParams, tb)}` }}
            className={cn(
              "px-3.5 py-2.5 text-[13px]",
              tab === tb
                ? "border-b-2 border-accent font-medium text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            {t(`node.tabs.${tb}`)}
          </Link>
        ))}
      </div>

      {/* Рендер вкладок остаётся УСЛОВНЫМ: LogsTab читает initialFilter только
          при монтировании (§48), поэтому скрытие через CSS молча сломало бы
          переходы «Обзор/Очередь → Логи». */}
      {tab === "overview" && (
        <OverviewTab
          node={node}
          onAllLogs={() => setSearchParams((prev) => withNodeTab(prev, "logs"))}
          onOpenLogs={openLogsAt}
        />
      )}
      {tab === "logs" && <LogsTab node={node} initialFilter={logsFilter ?? undefined} />}
      {tab === "config" && <ConfigTab node={node} />}
      {tab === "metrics" && (
        <MetricsTab
          node={node}
          onOpenLogs={openLogsAt}
          onOpenQueue={() => setSearchParams((prev) => withNodeTab(prev, "queue"))}
        />
      )}
      {tab === "queue" && (
        <QueueTab
          node={node}
          onOpenFailedLogs={(f) => setSearchParams((prev) => withFailedLogs(prev, f))}
        />
      )}
    </div>
  );
}

// CopyNodeDialog — диалог «Скопировать узел» (§53, по образцу MoveNodeDialog):
// вводится только новый path, копию создаёт бэкенд (POST /api/nodes/:id/copy —
// все настройки включая креды, статус всегда paused), затем переход на страницу
// копии.
function CopyNodeDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [path, setPath] = useState(`${node.path}-copy`);
  const [error, setError] = useState<string | null>(null);

  const pathCode = validateNodePath(path);

  const copy = useMutation({
    mutationFn: () => api.post<Node>(`/api/nodes/${node.id}/copy`, { path }),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      onClose();
      navigate(`/nodes/${created.id}`);
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <Modal
      title={t("node.copy.title")}
      subtitle={node.path}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            disabled={!!pathCode || copy.isPending}
            onClick={() => copy.mutate()}
          >
            {t("node.copy.submit")}
          </Button>
        </>
      }
    >
      {error && (
        <div className="mb-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
      )}
      <Field label={t("node.copy.new_path")} hint={t("node.copy.hint")}>
        <Input
          value={path}
          onChange={(e) => setPath(e.target.value)}
          className="font-mono"
          autoFocus
        />
      </Field>
      {pathCode && <div className="mt-1 text-xs text-err">{t(pathCode)}</div>}
    </Modal>
  );
}

// Kpi — компактный показатель для RabbitMQAsync-баннера (§27.11).
function Kpi({ label, value, tone }: { label: string; value: string; tone?: "ok" | "warn" }) {
  return (
    <div>
      <div className="text-[11px] uppercase tracking-wide text-fg-subtle">{label}</div>
      <div
        className={cn(
          "text-lg font-semibold",
          tone === "ok" ? "text-ok" : tone === "warn" ? "text-warn" : "text-fg",
        )}
      >
        {value}
      </div>
    </div>
  );
}
