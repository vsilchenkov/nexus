import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search, Plus } from "lucide-react";

import {
  api,
  type Node,
  type OverviewKPI,
  type NodesThroughputResp,
} from "../api/client";
import { Button, Card, Chip, Field, Input, Kpi, KpiRow, Pill, Seg, Select } from "../components/ui";
import { Modal } from "../components/ui/Modal";
import { cn } from "../lib/cn";

type ListResp = { items: Node[] };
type View = "table" | "cards";
type StatusFilter = "all" | "ok" | "warn" | "err" | "paused" | "disabled";
type Throughput = { in: number; out: number; errors: number; p95: number; spark: number[] };

const VIEW_KEY = "nexus.overview.view";

// fmtNum — компактный формат больших чисел (1.24M / 12.0k) и разделители для мелких.
function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (n >= 10_000) return (n / 1000).toFixed(1) + "k";
  return n.toLocaleString();
}

export default function Overview() {
  const { t } = useTranslation();
  const [search, setSearch] = useState("");
  const [method, setMethod] = useState<"" | "request" | "requestAsync">("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [view, setView] = useState<View>(
    () => (localStorage.getItem(VIEW_KEY) as View) || "table",
  );
  const [moveTarget, setMoveTarget] = useState<Node | null>(null);

  useEffect(() => localStorage.setItem(VIEW_KEY, view), [view]);

  const nodesQ = useQuery({
    queryKey: ["nodes", search],
    queryFn: () => api.get<ListResp>("/api/nodes", { search }),
  });

  const kpiQ = useQuery({
    queryKey: ["metrics-overview"],
    queryFn: () => api.get<OverviewKPI>("/api/metrics/overview"),
    refetchInterval: 30_000,
  });

  const thrQ = useQuery({
    queryKey: ["metrics-nodes", "1h"],
    queryFn: () => api.get<NodesThroughputResp>("/api/metrics/nodes", { range: "1h" }),
    refetchInterval: 30_000,
  });

  const throughput = useMemo(() => {
    const m = new Map<string, Throughput>();
    for (const it of thrQ.data?.items ?? []) {
      m.set(it.node, { in: it.in, out: it.out, errors: it.errors, p95: it.p95_ms, spark: it.spark ?? [] });
    }
    return m;
  }, [thrQ.data]);

  // Сортировка: проблемные первыми (err → warn → paused → ok → disabled),
  // внутри статуса — по убыванию входящего трафика (§22, ui_cards.html).
  const sortRank: Record<Variant, number> = useMemo(
    () => ({ err: 0, warn: 1, paused: 2, ok: 3, disabled: 4 }),
    [],
  );

  const nodes = useMemo(() => {
    let items = nodesQ.data?.items ?? [];
    if (method) items = items.filter((n) => n.root_method === method);
    if (statusFilter !== "all") {
      items = items.filter((n) => nodeVariant(n, throughput.get(n.path)) === statusFilter);
    }
    return [...items].sort((a, b) => {
      const va = nodeVariant(a, throughput.get(a.path));
      const vb = nodeVariant(b, throughput.get(b.path));
      if (sortRank[va] !== sortRank[vb]) return sortRank[va] - sortRank[vb];
      return (throughput.get(b.path)?.in ?? 0) - (throughput.get(a.path)?.in ?? 0);
    });
  }, [nodesQ.data, method, statusFilter, throughput, sortRank]);

  const kpi = kpiQ.data;
  const errPct = kpi && kpi.error_rate > 0 ? (kpi.error_rate * 100).toFixed(2) + "%" : "0%";

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <KpiRow>
        <Kpi label={t("overview.kpi.incoming")} value={kpi ? fmtNum(kpi.incoming_24h) : "—"} />
        <Kpi label={t("overview.kpi.outgoing")} value={kpi ? fmtNum(kpi.outgoing_24h) : "—"} />
        <Kpi label={t("overview.kpi.queue")} value={kpi ? fmtNum(kpi.kafka_queue) : "—"} />
        <Kpi
          label={t("overview.kpi.errors")}
          value={kpi ? fmtNum(kpi.errors_24h) : "—"}
          delta={kpi ? errPct : undefined}
          deltaTone={kpi && kpi.error_rate > 0.01 ? "down" : "muted"}
        />
      </KpiRow>

      {kpi && !kpi.prometheus_available && (
        <div className="text-xs text-fg-subtle">{t("metrics.prometheus_off")}</div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
          <Input
            className="pl-9"
            placeholder={t("overview.search_placeholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <Select
          className="w-40"
          value={method}
          onChange={(e) => setMethod(e.target.value as typeof method)}
        >
          <option value="">{t("overview.filter.all_methods")}</option>
          <option value="request">request</option>
          <option value="requestAsync">requestAsync</option>
        </Select>
        <Select
          className="w-40"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
        >
          <option value="all">{t("overview.filter.all_statuses")}</option>
          <option value="ok">{t("overview.status.ok")}</option>
          <option value="warn">{t("overview.status.queue")}</option>
          <option value="err">{t("overview.status.down")}</option>
          <option value="paused">{t("node.status.paused")}</option>
          <option value="disabled">{t("node.status.disabled")}</option>
        </Select>
        <Seg
          value={view}
          onChange={setView}
          options={[
            { value: "table", label: t("overview.view.table") },
            { value: "cards", label: t("overview.view.cards") },
          ]}
        />
        <Link to="/nodes/new">
          <Button variant="primary">
            <Plus className="h-4 w-4" /> {t("overview.new_node")}
          </Button>
        </Link>
      </div>

      {nodesQ.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {nodesQ.error && <div className="text-err">{(nodesQ.error as Error).message}</div>}
      {nodesQ.data && nodes.length === 0 && (
        <div className="text-fg-muted">{t("overview.empty")}</div>
      )}

      {nodes.length > 0 &&
        (view === "table" ? (
          <NodeTable nodes={nodes} throughput={throughput} onMove={setMoveTarget} />
        ) : (
          <NodeCards nodes={nodes} throughput={throughput} onMove={setMoveTarget} />
        ))}

      {moveTarget && (
        <MoveNodeDialog node={moveTarget} onClose={() => setMoveTarget(null)} />
      )}
    </div>
  );
}

type Variant = "ok" | "warn" | "err" | "paused" | "disabled";
type StatusInfo = { tone: "ok" | "err" | "warn"; label: string; variant: Variant };

// nodeVariant — чистая классификация статуса узла (без i18n), для фильтра,
// сортировки и цвета акцента карточки (§22, ui_cards.html).
function nodeVariant(n: Node, m?: Throughput): Variant {
  if (n.status === "disabled") return "disabled";
  if (n.status === "paused") return "paused";
  if (m) {
    const rate = m.out > 0 ? m.errors / m.out : 0;
    if (rate > 0.3) return "err";
    if (n.root_method === "requestAsync" && m.in - m.out > Math.max(50, m.in * 0.1)) {
      return "warn";
    }
  }
  return "ok";
}

// accentByVariant — класс полосы-акцента слева (border-l) по статусу.
const accentByVariant: Record<Variant, string> = {
  ok: "border-l-ok",
  warn: "border-l-warn",
  err: "border-l-err",
  paused: "border-l-accent",
  disabled: "border-l-fg-subtle",
};

function useStatus() {
  const { t } = useTranslation();
  return (n: Node, m?: Throughput): StatusInfo => {
    const variant = nodeVariant(n, m);
    switch (variant) {
      case "disabled":
        return { variant, tone: "err", label: t("node.status.disabled") };
      case "paused":
        return { variant, tone: "warn", label: t("node.status.paused") };
      case "err":
        return { variant, tone: "err", label: t("overview.status.down") };
      case "warn":
        return { variant, tone: "warn", label: t("overview.status.queue") };
      default:
        return { variant, tone: "ok", label: t("overview.status.ok") };
    }
  };
}

function NodeTable({
  nodes,
  throughput,
  onMove,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  onMove: (n: Node) => void;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  return (
    <Card className="overflow-hidden p-0">
      <table className="w-full text-[12.5px]">
        <thead>
          <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
            <th className="px-3 py-2 font-medium">{t("overview.table.path")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.method")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.in1h")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.out1h")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.errors")}</th>
            <th className="px-3 py-2 font-medium">{t("overview.table.status")}</th>
            <th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {nodes.map((n) => {
            const m = throughput.get(n.path);
            const s = status(n, m);
            return (
              <tr key={n.id} className="border-b border-line last:border-0 hover:bg-bg-muted">
                <td className="px-3 py-2.5 font-mono">
                  <Link to={`/nodes/${n.id}`} className="hover:text-accent">
                    {n.path}
                  </Link>
                </td>
                <td className="px-3 py-2.5">
                  <Chip>{n.root_method}</Chip>
                </td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.in) : "—"}</td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.out) : "—"}</td>
                <td className="px-3 py-2.5 font-mono">{m ? fmtNum(m.errors) : "—"}</td>
                <td className="px-3 py-2.5">
                  <Pill tone={s.tone}>{s.label}</Pill>
                </td>
                <td className="px-3 py-2.5 text-right">
                  <button
                    type="button"
                    onClick={() => onMove(n)}
                    className="text-xs text-fg-subtle hover:text-accent"
                  >
                    {t("overview.move.action")}
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Card>
  );
}

// fmtMs — компактный формат p95-латентности (ms / s).
function fmtMs(ms: number): string {
  if (ms <= 0) return "—";
  if (ms >= 1000) return (ms / 1000).toFixed(1) + "s";
  return Math.round(ms) + "ms";
}

function NodeCards({
  nodes,
  throughput,
  onMove,
}: {
  nodes: Node[];
  throughput: Map<string, Throughput>;
  onMove: (n: Node) => void;
}) {
  const { t } = useTranslation();
  const status = useStatus();
  return (
    <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3">
      {nodes.map((n) => {
        const m = throughput.get(n.path);
        const s = status(n, m);
        const target =
          n.url_mode === "from_request" ? t("overview.card.dynamic_url") : n.target_url || "—";
        return (
          <Card
            key={n.id}
            className={cn(
              "flex h-full flex-col gap-3 border-l-[3px]",
              accentByVariant[s.variant],
              n.status === "disabled" && "opacity-70",
            )}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <Link
                  to={`/nodes/${n.id}`}
                  className="block truncate font-mono text-[13px] font-medium hover:text-accent"
                >
                  {n.path}
                </Link>
                <div className="mt-1 flex items-center gap-1.5">
                  <Chip>{n.root_method}</Chip>
                  <Pill tone={s.tone}>
                    <span className="mr-1 inline-block h-1.5 w-1.5 rounded-full bg-current" />
                    {s.label}
                  </Pill>
                </div>
              </div>
            </div>
            <div className="grid grid-cols-3 gap-2 border-y border-line py-2.5 text-center">
              <CardStat label={t("overview.table.in1h")} value={m ? fmtNum(m.in) : "—"} />
              <CardStat label="p95" value={m ? fmtMs(m.p95) : "—"} />
              <CardStat
                label={t("overview.table.errors")}
                value={m ? fmtNum(m.errors) : "—"}
                tone={m && m.errors > 0 ? "err" : undefined}
              />
            </div>
            <Sparkline data={m?.spark ?? []} variant={s.variant} />
            <div className="flex items-center justify-between gap-2 text-[11px] text-fg-subtle">
              <span className="truncate font-mono" title={target}>
                {target}
              </span>
              <button
                type="button"
                onClick={() => onMove(n)}
                className="shrink-0 hover:text-accent"
              >
                {t("overview.move.action")}
              </button>
            </div>
          </Card>
        );
      })}
    </div>
  );
}

function CardStat({ label, value, tone }: { label: string; value: string; tone?: "err" }) {
  return (
    <div>
      <div className={cn("font-mono text-[15px]", tone === "err" && "text-err")}>{value}</div>
      <div className="text-[10px] uppercase tracking-wide text-fg-subtle">{label}</div>
    </div>
  );
}

// Sparkline — мини-график входящего трафика за час (§22, ui_cards.html).
function Sparkline({ data, variant }: { data: number[]; variant: Variant }) {
  if (data.length === 0) {
    return <div className="h-7" />;
  }
  const max = Math.max(1, ...data);
  const color =
    variant === "err"
      ? "bg-err"
      : variant === "warn"
        ? "bg-warn"
        : variant === "disabled"
          ? "bg-fg-subtle"
          : "bg-accent";
  return (
    <div className="flex h-7 items-end gap-px">
      {data.map((v, i) => (
        <span
          key={i}
          className={cn("flex-1 rounded-sm opacity-80", color)}
          style={{ height: `${Math.max(4, (v / max) * 100)}%` }}
        />
      ))}
    </div>
  );
}

type Team = { id: string; slug: string; name: string };

function MoveNodeDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [error, setError] = useState<string | null>(null);

  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => api.get<{ items: Team[] }>("/api/teams"),
  });

  const move = useMutation({
    mutationFn: () => api.post(`/api/nodes/${node.id}/move`, { target_team_slug: slug }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["nodes"] });
      onClose();
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <Modal
      title={t("overview.move.title")}
      subtitle={node.path}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={!slug || move.isPending} onClick={() => move.mutate()}>
            {t("overview.move.submit")}
          </Button>
        </>
      }
    >
      {error && (
        <div className="mb-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>
      )}
      <Field label={t("overview.move.target_team")} hint={t("overview.move.hint")}>
        <Select value={slug} onChange={(e) => setSlug(e.target.value)}>
          <option value="">{t("overview.move.pick_team")}</option>
          {(teams.data?.items ?? []).map((tm) => (
            <option key={tm.id} value={tm.slug}>
              {tm.name} ({tm.slug})
            </option>
          ))}
        </Select>
      </Field>
    </Modal>
  );
}
