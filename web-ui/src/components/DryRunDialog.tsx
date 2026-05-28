import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Play } from "lucide-react";

import { api } from "../api/client";
import { Button, Field, Modal, Pill, Select, Textarea } from "./ui";

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

export function DryRunDialog({ node, onClose }: { node: unknown; onClose: () => void }) {
  const { t } = useTranslation();
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
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } } };
      setError(err?.response?.data?.error ?? t("common.error"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={t("dryrun.title")}
      subtitle={t("dryrun.subtitle")}
      onClose={onClose}
      className="max-w-2xl"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="primary" disabled={busy} onClick={run}>
            <Play className="h-4 w-4" /> {t("dryrun.run")}
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-[120px_1fr] items-center gap-3">
        <Field label={t("dryrun.method")}>
          <Select value={method} onChange={(e) => setMethod(e.target.value)}>
            {["POST", "GET", "PUT", "DELETE", "PATCH"].map((m) => (
              <option key={m}>{m}</option>
            ))}
          </Select>
        </Field>
        <div />
      </div>
      <Field label={t("dryrun.body")} className="mt-3">
        <Textarea rows={3} value={body} onChange={(e) => setBody(e.target.value)} />
      </Field>

      {error && <div className="mt-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      {report && (
        <div className="mt-4 space-y-2 border-t border-line pt-4">
          <div className="text-[11px] uppercase tracking-wide text-fg-subtle">{t("dryrun.result")}</div>
          {report.steps.map((s, i) => (
            <div key={i} className="flex items-start gap-2 text-xs">
              <Pill tone={s.status === "ok" ? "ok" : s.status === "failed" ? "err" : "warn"}>
                {i + 1}
              </Pill>
              <div>
                <span className="font-mono">{s.name}</span>
                {s.message && <span className="ml-2 text-fg-muted">{s.message}</span>}
                {s.detail !== undefined && (
                  <pre className="mt-1 overflow-auto rounded bg-bg-muted/50 p-1 text-fg-muted">
                    {JSON.stringify(s.detail, null, 2) ?? ""}
                  </pre>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
