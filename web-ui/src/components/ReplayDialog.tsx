import { useState } from "react";
import { useTranslation } from "react-i18next";
import { RefreshCw, ShieldQuestion } from "lucide-react";

import { api } from "../api/client";
import { Button, Field, Hint, Input, Modal, Textarea } from "./ui";

type ReplayResult = {
  new_log_id: string;
  status_code: number;
  body_preview?: string;
};

export function ReplayDialog({
  logId,
  nodeId,
  onClose,
}: {
  logId: string;
  nodeId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [bodyOverride, setBodyOverride] = useState("");
  const [syncOverride, setSyncOverride] = useState(false);
  const [useNodeAuth, setUseNodeAuth] = useState(true);
  const [customAuth, setCustomAuth] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ReplayResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.post<ReplayResult>(`/api/logs/${logId}/replay`, {
        node_id: nodeId,
        body_override: bodyOverride || undefined,
        sync_override: syncOverride,
        use_node_auth: useNodeAuth,
        custom_auth: customAuth || undefined,
      });
      setResult(r);
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } };
      setError(err?.response?.data?.error ?? t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={t("replay.title")}
      subtitle={<span className="font-mono">{logId}</span>}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" disabled={busy} onClick={run}>
            <RefreshCw className="h-4 w-4" /> {t("replay.send")}
          </Button>
        </>
      }
    >
      <Field label={t("replay.body_label")}>
        <Textarea rows={4} value={bodyOverride} onChange={(e) => setBodyOverride(e.target.value)} />
      </Field>

      <label className="mt-3 flex items-center gap-2 text-[13px]">
        <input type="checkbox" checked={syncOverride} onChange={(e) => setSyncOverride(e.target.checked)} />
        {t("replay.sync")}
      </label>
      <label className="mt-2 flex items-center gap-2 text-[13px]">
        <input type="checkbox" checked={useNodeAuth} onChange={(e) => setUseNodeAuth(e.target.checked)} />
        {t("replay.use_node_auth")}
      </label>

      {!useNodeAuth && (
        <Field label={t("replay.custom_auth")} className="mt-3">
          <Input mono value={customAuth} onChange={(e) => setCustomAuth(e.target.value)} placeholder="Bearer …" />
        </Field>
      )}

      <Hint tone="muted" icon={<ShieldQuestion className="h-3.5 w-3.5" />} className="mt-3">
        {t("replay.security")}
      </Hint>

      {error && <div className="mt-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      {result && (
        <div className="mt-3 space-y-1 border-t border-line pt-3 text-sm">
          <div className={result.status_code < 300 ? "text-ok" : "text-err"}>
            {t("replay.result_status")} {result.status_code}
          </div>
          {result.body_preview && (
            <pre className="overflow-auto rounded bg-bg-muted/50 p-2 text-xs text-fg-muted">
              {result.body_preview}
            </pre>
          )}
          <div className="font-mono text-xs text-fg-muted">{result.new_log_id}</div>
        </div>
      )}
    </Modal>
  );
}
