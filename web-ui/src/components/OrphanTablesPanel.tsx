import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";

type Orphan = {
  database: string;
  table: string;
  full_name: string;
  engine: string;
  total_rows: number;
  total_bytes: number;
  created_at: string;
};
type Resp = { items: Orphan[] };

function formatBytes(b: number): string {
  if (!b) return "0";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = b;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

// Двойное подтверждение DROP: первый шаг — checkbox-confirm,
// второй — нужно ввести имя таблицы вручную.
function DropDialog({
  table,
  onClose,
  onConfirm,
  pending,
}: {
  table: string;
  onClose: () => void;
  onConfirm: () => void;
  pending: boolean;
}) {
  const { t } = useTranslation();
  const [typed, setTyped] = useState("");
  const match = typed === table;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="bg-bg-elev rounded-xl border border-bg-muted max-w-md w-full p-5 space-y-3"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="text-base font-semibold text-err">
          {t("settings.clickhouse.orphans.drop_confirm_1", { table })}
        </h3>
        <p className="text-sm text-fg-muted">
          {t("settings.clickhouse.orphans.drop_confirm_2")}
        </p>
        <input
          autoFocus
          type="text"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder={table}
          className="w-full px-3 py-2 bg-bg-muted rounded-md font-mono text-sm outline-none"
        />
        {typed && !match && (
          <p className="text-xs text-err">
            {t("settings.clickhouse.orphans.drop_mismatch")}
          </p>
        )}
        <div className="flex justify-end gap-2 pt-2">
          <button
            onClick={onClose}
            className="px-3 py-2 bg-bg-muted hover:bg-bg-muted/70 rounded-md text-sm"
          >
            {t("common.cancel")}
          </button>
          <button
            disabled={!match || pending}
            onClick={onConfirm}
            className="px-3 py-2 bg-err hover:bg-err/80 text-white rounded-md text-sm disabled:opacity-50"
          >
            {pending
              ? t("settings.clickhouse.orphans.dropping")
              : t("settings.clickhouse.orphans.drop")}
          </button>
        </div>
      </div>
    </div>
  );
}

export function OrphanTablesPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [enabled, setEnabled] = useState(false);
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  const q = useQuery({
    queryKey: ["orphan-tables"],
    queryFn: () => api.get<Resp>("/api/settings/clickhouse/orphans"),
    enabled,
  });

  const drop = useMutation({
    mutationFn: (table: string) =>
      api.del(`/api/settings/clickhouse/orphans/${encodeURIComponent(table)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["orphan-tables"] });
      setDropTarget(null);
    },
  });

  return (
    <section className="space-y-3 pt-6 border-t border-bg-muted">
      <header className="flex items-center justify-between">
        <h3 className="text-base font-semibold">
          {t("settings.clickhouse.orphans.title")}
        </h3>
        <button
          onClick={() => {
            if (!enabled) setEnabled(true);
            else q.refetch();
          }}
          disabled={q.isFetching}
          className="bg-bg-muted hover:bg-bg-muted/70 px-3 py-1.5 rounded-md text-sm disabled:opacity-50"
        >
          {q.isFetching
            ? t("settings.clickhouse.orphans.scanning")
            : enabled
              ? t("settings.clickhouse.orphans.rescan")
              : t("settings.clickhouse.orphans.scan")}
        </button>
      </header>
      <p className="text-sm text-fg-muted">
        {t("settings.clickhouse.orphans.hint")}
      </p>

      {q.error && (
        <div className="text-err text-sm">{(q.error as Error).message}</div>
      )}
      {drop.error && (
        <div className="text-err text-sm">{(drop.error as Error).message}</div>
      )}

      {enabled && q.data && (
        <div className="bg-bg-elev rounded-xl border border-bg-muted overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-bg-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 text-left">
                  {t("settings.clickhouse.orphans.col.table")}
                </th>
                <th className="px-3 py-2 text-left">
                  {t("settings.clickhouse.orphans.col.engine")}
                </th>
                <th className="px-3 py-2 text-right">
                  {t("settings.clickhouse.orphans.col.rows")}
                </th>
                <th className="px-3 py-2 text-right">
                  {t("settings.clickhouse.orphans.col.size")}
                </th>
                <th className="px-3 py-2 text-left">
                  {t("settings.clickhouse.orphans.col.modified")}
                </th>
                <th className="px-3 py-2 text-right w-px"></th>
              </tr>
            </thead>
            <tbody>
              {q.data.items.length === 0 && (
                <tr>
                  <td
                    colSpan={6}
                    className="px-3 py-6 text-center text-fg-muted"
                  >
                    {t("settings.clickhouse.orphans.empty")}
                  </td>
                </tr>
              )}
              {q.data.items.map((it) => (
                <tr key={it.full_name} className="border-t border-bg-muted">
                  <td className="px-3 py-2 font-mono text-xs">{it.full_name}</td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                    {it.engine}
                  </td>
                  <td className="px-3 py-2 text-right font-mono text-xs">
                    {it.total_rows.toLocaleString()}
                  </td>
                  <td className="px-3 py-2 text-right font-mono text-xs">
                    {formatBytes(it.total_bytes)}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted whitespace-nowrap">
                    {new Date(it.created_at).toLocaleString()}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <button
                      onClick={() => setDropTarget(it.full_name)}
                      className="px-2 py-1 bg-err/20 hover:bg-err/30 text-err rounded text-xs font-mono"
                    >
                      {t("settings.clickhouse.orphans.drop")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {dropTarget && (
        <DropDialog
          table={dropTarget}
          pending={drop.isPending}
          onClose={() => setDropTarget(null)}
          onConfirm={() => drop.mutate(dropTarget)}
        />
      )}
    </section>
  );
}
