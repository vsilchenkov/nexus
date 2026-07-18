import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, RefreshCw } from "lucide-react";

import { api } from "../api/client";
import { Button, Hint, Modal, Pill } from "./ui";

type Rejection = {
  kind: string;
  current: string;
  desired: string;
  reason: string;
};
type SchemaSyncResult = {
  table: string;
  table_missing: boolean;
  applied: number;
  plan: { statements: string[] | null; rejections: Rejection[] | null };
};

// CHSchemaSyncDialog — §56: предпросмотр ALTER'ов приведения существующей
// таблицы к шаблону узла и явное применение. План грузится при открытии
// (read-only), «Применить» исполняет ALTER'ы.
export function CHSchemaSyncDialog({ nodeId, onClose }: { nodeId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const [result, setResult] = useState<SchemaSyncResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [appliedMsg, setAppliedMsg] = useState<string | null>(null);

  useEffect(() => {
    void loadPlan();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeId]);

  async function call(path: string): Promise<SchemaSyncResult> {
    return api.post<SchemaSyncResult>(`/api/nodes/${nodeId}/ch-schema/${path}`);
  }

  async function loadPlan() {
    setBusy(true);
    setError(null);
    setAppliedMsg(null);
    try {
      setResult(await call("plan"));
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } };
      setError(err?.response?.data?.error ?? t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    setApplying(true);
    setError(null);
    try {
      const r = await call("apply");
      setResult(r);
      setAppliedMsg(t("ch_sync.applied", { n: r.applied }));
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } };
      setError(err?.response?.data?.error ?? t("common.error"));
    } finally {
      setApplying(false);
    }
  }

  const statements = result?.plan.statements ?? [];
  const rejections = result?.plan.rejections ?? [];
  const canApply = statements.length > 0 && !applying && !busy;

  return (
    <Modal
      title={t("ch_sync.title")}
      subtitle={t("ch_sync.subtitle")}
      onClose={onClose}
      className="max-w-2xl"
      footer={
        <>
          {canApply && (
            <span className="mr-auto flex items-center gap-1.5 text-[11px] text-err">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
              {t("ch_sync.apply_warning")}
            </span>
          )}
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="danger" disabled={!canApply} onClick={apply}>
            <RefreshCw className="h-4 w-4" /> {t("ch_sync.apply")}
          </Button>
        </>
      }
    >
      {result && <div className="font-mono text-xs text-fg-subtle">{result.table}</div>}

      {busy && <div className="mt-3 text-sm text-fg-muted">{t("ch_sync.loading")}</div>}

      {appliedMsg && (
        <div className="mt-3 rounded-md bg-ok/10 px-3 py-2 text-sm text-ok">{appliedMsg}</div>
      )}
      {error && <div className="mt-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      {result && !busy && result.table_missing && (
        <Hint className="mt-3">{t("ch_sync.table_missing")}</Hint>
      )}

      {result && !busy && !result.table_missing && statements.length === 0 && rejections.length === 0 && (
        <div className="mt-3 rounded-md bg-ok/10 px-3 py-2 text-sm text-ok">{t("ch_sync.up_to_date")}</div>
      )}

      {statements.length > 0 && (
        <div className="mt-4 space-y-1">
          <div className="text-[11px] uppercase tracking-wide text-fg-subtle">
            {t("ch_sync.statements_head")}
          </div>
          {statements.map((s, i) => (
            <pre
              key={i}
              className="overflow-auto rounded bg-bg-muted/50 px-2 py-1 font-mono text-xs text-fg-muted"
            >
              {s}
            </pre>
          ))}
        </div>
      )}

      {rejections.length > 0 && (
        <div className="mt-4 space-y-1">
          <div className="text-[11px] uppercase tracking-wide text-fg-subtle">
            {t("ch_sync.rejections_head")}
          </div>
          {rejections.map((r, i) => (
            <div key={i} className="flex items-start gap-2 text-xs">
              <Pill tone="warn">{r.kind}</Pill>
              <div className="min-w-0">
                <div className="text-fg-muted">{r.reason}</div>
                <div className="font-mono text-[11px] text-fg-subtle break-all">
                  {r.current} → {r.desired}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
