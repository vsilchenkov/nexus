import { useState } from "react";

import { api } from "../api/client";

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
  const [bodyOverride, setBodyOverride] = useState<string>("");
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
    } catch (e: any) {
      setError(e?.response?.data?.error ?? String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
      <div className="bg-bg-elev border border-bg-muted rounded-xl shadow-xl w-full max-w-xl">
        <header className="flex items-center justify-between p-4 border-b border-bg-muted">
          <h3 className="font-semibold">replay log</h3>
          <button onClick={onClose} className="text-fg-muted hover:text-fg">
            ✕
          </button>
        </header>

        <div className="p-4 space-y-3 text-sm">
          <div className="font-mono text-xs text-fg-muted">log_id: {logId}</div>

          <label className="block">
            <div className="text-fg-muted mb-1">body override (leave empty to reuse original)</div>
            <textarea
              value={bodyOverride}
              onChange={(e) => setBodyOverride(e.target.value)}
              rows={4}
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
            />
          </label>

          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={syncOverride}
              onChange={(e) => setSyncOverride(e.target.checked)}
            />
            send as sync (instead of async)
          </label>

          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={useNodeAuth}
              onChange={(e) => setUseNodeAuth(e.target.checked)}
            />
            use node auth config
          </label>

          {!useNodeAuth && (
            <label className="block">
              <div className="text-fg-muted mb-1">custom Authorization header</div>
              <input
                value={customAuth}
                onChange={(e) => setCustomAuth(e.target.value)}
                placeholder="Bearer ..."
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
              />
            </label>
          )}

          <button
            onClick={run}
            disabled={busy}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm font-medium disabled:opacity-50"
          >
            {busy ? "…" : "send replay"}
          </button>

          {error && <div className="text-err">{error}</div>}

          {result && (
            <div className="mt-3 space-y-1 border-t border-bg-muted pt-3">
              <div className={result.status_code < 300 ? "text-ok" : "text-err"}>
                status {result.status_code}
              </div>
              {result.body_preview && (
                <pre className="text-xs text-fg-muted bg-bg-muted/40 p-2 rounded overflow-auto">
                  {result.body_preview}
                </pre>
              )}
              <div className="text-xs text-fg-muted">new_log_id: {result.new_log_id}</div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
