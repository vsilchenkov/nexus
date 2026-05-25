import { useState } from "react";

import { api } from "../api/client";

type DryRunStep = {
  name: string;
  status: "ok" | "failed" | "skipped";
  message?: string;
  detail?: unknown;
};
type DryRunReport = {
  id: string;
  ok: boolean;
  steps: DryRunStep[];
};

export function DryRunDialog({
  node,
  onClose,
}: {
  node: unknown;
  onClose: () => void;
}) {
  const [method, setMethod] = useState("POST");
  const [body, setBody] = useState('{"test": true}');
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<DryRunReport | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    setBusy(true);
    setError(null);
    setReport(null);
    try {
      const r = await api.post<DryRunReport>("/api/nodes/dry-run", {
        node,
        request: { method, body, query: {}, headers: {} },
        use_mock: true,
      });
      setReport(r);
    } catch (e: any) {
      setError(e?.response?.data?.error ?? String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
      <div className="bg-bg-elev border border-bg-muted rounded-xl shadow-xl w-full max-w-2xl max-h-[90vh] overflow-auto">
        <header className="flex items-center justify-between p-4 border-b border-bg-muted">
          <h3 className="font-semibold">dry-run (mock)</h3>
          <button onClick={onClose} className="text-fg-muted hover:text-fg">
            ✕
          </button>
        </header>

        <div className="p-4 space-y-3">
          <div className="grid grid-cols-[120px_1fr] gap-2 items-center">
            <label className="text-fg-muted text-sm">method</label>
            <select
              value={method}
              onChange={(e) => setMethod(e.target.value)}
              className="px-3 py-2 bg-bg-muted rounded-md outline-none"
            >
              {["POST", "GET", "PUT", "DELETE", "PATCH"].map((m) => (
                <option key={m}>{m}</option>
              ))}
            </select>
          </div>
          <label className="text-fg-muted text-sm block">body (JSON)</label>
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            rows={4}
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-sm"
          />

          <button
            onClick={run}
            disabled={busy}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm font-medium disabled:opacity-50"
          >
            {busy ? "…" : "run"}
          </button>

          {error && <div className="text-err">{error}</div>}

          {report && (
            <div className="space-y-2 mt-4 border-t border-bg-muted pt-4">
              <div className={`text-sm ${report.ok ? "text-ok" : "text-err"}`}>
                {report.ok ? "OK" : "FAILED"}
              </div>
              {report.steps.map((s, i) => (
                <div key={i} className="text-xs">
                  <span
                    className={`inline-block w-16 ${
                      s.status === "ok"
                        ? "text-ok"
                        : s.status === "failed"
                          ? "text-err"
                          : "text-fg-muted"
                    }`}
                  >
                    [{s.status}]
                  </span>
                  <span className="font-mono">{s.name}</span>
                  {s.message && <span className="text-fg-muted ml-2">{s.message}</span>}
                  {s.detail !== undefined && (
                    <pre className="ml-16 text-fg-muted overflow-auto bg-bg-muted/40 p-1 rounded mt-1">
                      {JSON.stringify(s.detail, null, 2) ?? ""}
                    </pre>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
