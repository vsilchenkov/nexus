import { useParams, Link, useSearchParams, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient, useIsFetching } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Copy as CopyIcon, Pencil, RefreshCw } from "lucide-react";
import { useState } from "react";

import { api, type Node } from "../api/client";
import { Button, Chip, Field, Input, Pill, type LogsRange } from "../components/ui";
import { Modal } from "../components/ui/Modal";
import { cn } from "../lib/cn";
import { msToDatetimeLocal } from "../lib/format";
import { validateNodePath } from "../lib/nodeValidation";
import { useRoleAtLeast } from "../lib/useCurrentRole";
import { LogsTab, type LogsInitialFilter } from "../components/node/LogsTab";
import { OverviewTab } from "../components/node/OverviewTab";
import { ConfigTab } from "../components/node/ConfigTab";
import { MetricsTab } from "../components/node/MetricsTab";
import { QueueTab } from "../components/node/QueueTab";

type Tab = "overview" | "logs" | "config" | "metrics" | "queue";

const BASE_TABS: Tab[] = ["overview", "logs", "config", "metrics"];

export default function NodeDetail() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  // §47.4: дип-линк со спарклайна дашборда — /nodes/:id?tab=logs&from&to (ms).
  // Стартуем на вкладке логов с окном бакета (как клик по графику узла, §33.4).
  const [searchParams] = useSearchParams();
  const qsFrom = Number(searchParams.get("from"));
  const qsTo = Number(searchParams.get("to"));
  const hasQsWindow =
    !!searchParams.get("from") && !!searchParams.get("to") && Number.isFinite(qsFrom) && Number.isFinite(qsTo);
  const [tab, setTab] = useState<Tab>(searchParams.get("tab") === "logs" ? "logs" : "overview");
  // §33.4: фильтр логов, прокинутый кликом по столбцу графика (момент времени).
  const [logsFilter, setLogsFilter] = useState<LogsInitialFilter | null>(
    hasQsWindow
      ? { from: msToDatetimeLocal(qsFrom), to: msToDatetimeLocal(qsTo), status: "all" }
      : null,
  );
  // §26/§28 Пункт 3: редактирование узла — только manager+ (viewer не видит креды).
  const canEdit = useRoleAtLeast("manager");
  // §53: диалог «Скопировать узел» (manager+, как создание).
  const [copyOpen, setCopyOpen] = useState(false);
  // Ручное обновление: инвалидируем все активные запросы → перезагружаются данные
  // текущей вкладки и шапки узла. fetching>0 — крутим иконку.
  const qc = useQueryClient();
  const fetching = useIsFetching();

  // openLogsAt — переход на вкладку логов с временным окном бакета (§33.4).
  const openLogsAt = (r: LogsRange) => {
    setLogsFilter({
      from: msToDatetimeLocal(r.from),
      to: msToDatetimeLocal(r.to),
      status: r.onlyErrors ? "err" : "all",
    });
    setTab("logs");
  };

  const nodeQ = useQuery({
    queryKey: ["node", id],
    queryFn: () => api.get<Node>(`/api/nodes/${id}`),
    enabled: !!id,
    // §27: для RabbitMQAsync live-обновляем health-снимок (queue depth/degraded).
    refetchInterval: (q) =>
      (q.state.data as Node | undefined)?.root_method === "RabbitMQAsync" ? 5000 : false,
  });

  const node = nodeQ.data;

  if (nodeQ.isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;
  if (!node) return <div className="text-err">{t("node.not_found")}</div>;

  const statusTone = node.status === "enabled" ? "ok" : node.status === "paused" ? "warn" : "err";
  const isPull = node.root_method === "RabbitMQAsync";
  const rmq = node.rmq_status;
  // §34.4: вкладка управления async-очередью — только для requestAsync.
  const tabs: Tab[] =
    node.root_method === "requestAsync" ? [...BASE_TABS, "queue"] : BASE_TABS;

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="font-mono text-xl font-semibold">{node.path}</h1>
        <Chip>{node.root_method}</Chip>
        <Pill tone={statusTone}>{t(`node.status.${node.status}`)}</Pill>
        {isPull && rmq?.degraded && <Pill tone="err">{t("node.rmq.degraded")}</Pill>}
        <div className="ml-auto flex items-center gap-2">
          <Button
            sm
            variant="ghost"
            disabled={fetching > 0}
            onClick={() => void qc.invalidateQueries()}
            title={t("node.actions.refresh")}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", fetching > 0 && "animate-spin")} />{" "}
            {t("node.actions.refresh")}
          </Button>
          {canEdit && (
            <>
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

      <div className="flex gap-1 border-b border-line">
        {tabs.map((tb) => (
          <button
            key={tb}
            type="button"
            onClick={() => {
              setTab(tb);
              // Ручной переход на логи — без унаследованного фильтра клика по графику.
              if (tb !== "logs") setLogsFilter(null);
            }}
            className={cn(
              "px-3.5 py-2.5 text-[13px]",
              tab === tb
                ? "border-b-2 border-accent font-medium text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            {t(`node.tabs.${tb}`)}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <OverviewTab node={node} onAllLogs={() => setTab("logs")} onOpenLogs={openLogsAt} />
      )}
      {tab === "logs" && <LogsTab node={node} initialFilter={logsFilter ?? undefined} />}
      {tab === "config" && <ConfigTab node={node} />}
      {tab === "metrics" && <MetricsTab node={node} onOpenLogs={openLogsAt} />}
      {tab === "queue" && (
        <QueueTab
          node={node}
          onOpenFailedLogs={(f) => {
            setLogsFilter(f);
            setTab("logs");
          }}
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
