import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Play } from "lucide-react";

import { api } from "../api/client";
import { incomingAuthHint } from "../lib/dryRunHint";
import { Button, Field, Hint, Input, Modal, Pill, Select, Textarea, Toggle } from "./ui";
import { KeyValueChips, type KeyValuePair } from "./dryrun/KeyValueChips";

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

// Бэкенд ждёт map[string][]string — одно значение на ключ покрывает сценарии
// теста (дублирующиеся заголовки в диагностике не нужны).
function toMultiMap(pairs: KeyValuePair[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const p of pairs) out[p.key] = [p.value];
  return out;
}

export function DryRunDialog({
  node,
  nodeId,
  onClose,
}: {
  node: unknown;
  // nodeId — §55.6: для сохранённого узла сервер подмешает креды (наружу они не
  // отдаются). Пусто при создании узла.
  nodeId?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [method, setMethod] = useState("POST");
  const [pathTail, setPathTail] = useState("");
  const [body, setBody] = useState('{"test": true}');
  const [headers, setHeaders] = useState<KeyValuePair[]>([]);
  const [query, setQuery] = useState<KeyValuePair[]>([]);
  // §55.2: mock включён по умолчанию — реальный запрос во внешнюю систему
  // должен быть осознанным выбором, а не результатом клика по «Запустить».
  const [useMock, setUseMock] = useState(true);
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<DryRunReport | null>(null);
  const [error, setError] = useState<string | null>(null);

  const hint = useMemo(() => incomingAuthHint(node as never), [node]);
  const cfg = node as { target_url?: string; path_passthrough?: boolean; path?: string } | null;
  const targetURL = cfg?.target_url ?? "";
  // §39: хвост входящего пути имеет смысл только у passthrough-узлов — у
  // остальных Receiver его отбрасывает, и поле лишь путало бы.
  const passthrough = cfg?.path_passthrough === true;

  async function run() {
    setBusy(true);
    setError(null);
    setReport(null);
    try {
      const r = await api.post<DryRunReport>("/api/nodes/dry-run", {
        node,
        node_id: nodeId || undefined,
        request: {
          method,
          body,
          path_tail: passthrough ? pathTail : "",
          query: toMultiMap(query),
          headers: toMultiMap(headers),
        },
        use_mock: useMock,
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
          {!useMock && (
            <span className="mr-auto flex items-center gap-1.5 text-[11px] text-err">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
              {t("dryrun.real_warning", { target: targetURL || t("dryrun.real_target_unknown") })}
            </span>
          )}
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant={useMock ? "primary" : "danger"} disabled={busy} onClick={run}>
            <Play className="h-4 w-4" /> {t("dryrun.run")}
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-[120px_1fr] items-start gap-3">
        <Field label={t("dryrun.method")} help={t("dryrun.method_help")}>
          <Select value={method} onChange={(e) => setMethod(e.target.value)}>
            {["POST", "GET", "PUT", "DELETE", "PATCH"].map((m) => (
              <option key={m}>{m}</option>
            ))}
          </Select>
        </Field>
        {/* §39: хвост пути — только для passthrough-узлов (иначе Receiver его
            отбрасывает). Префикс показывает, куда именно он приклеится. */}
        {passthrough ? (
          <Field label={t("dryrun.path_tail")} help={t("dryrun.path_tail_help")}>
            <div className="flex items-center gap-1">
              <span className="shrink-0 font-mono text-[11px] text-fg-subtle">
                /{cfg?.path ?? ""}/
              </span>
              <Input
                value={pathTail}
                onChange={(e) => setPathTail(e.target.value.replace(/^\/+/, ""))}
                placeholder={t("dryrun.path_tail_ph")}
                className="font-mono text-xs"
              />
            </div>
          </Field>
        ) : (
          <div />
        )}
      </div>

      {/* §55.2: подсказка — какое поле ожидает узел. Без неё тест узла с
          авторизацией упирался в «authorization header missing». */}
      {hint && (
        <Hint className="mt-3">
          {t(`dryrun.hint.${hint.source}`, { field: hint.field })}{" "}
          {t(`dryrun.hint.scheme.${hint.scheme}`)}
        </Hint>
      )}

      <Field label={t("dryrun.headers")} className="mt-3">
        <KeyValueChips
          value={headers}
          onChange={setHeaders}
          keyPlaceholder={t("dryrun.header_name")}
          valuePlaceholder={t("dryrun.header_value")}
          suggestion={hint?.source === "header" ? hint.field : undefined}
        />
      </Field>

      <Field label={t("dryrun.query")} className="mt-3">
        <KeyValueChips
          value={query}
          onChange={setQuery}
          keyPlaceholder={t("dryrun.query_name")}
          valuePlaceholder={t("dryrun.query_value")}
          suggestion={hint?.source === "query" ? hint.field : undefined}
        />
      </Field>

      <Field label={t("dryrun.body")} className="mt-3">
        <Textarea rows={3} value={body} onChange={(e) => setBody(e.target.value)} />
      </Field>

      <div className="mt-3">
        <Toggle checked={useMock} onChange={setUseMock} label={t("dryrun.use_mock")} />
        <div className="mt-1 text-[11px] text-fg-subtle">
          {useMock ? t("dryrun.use_mock_help") : t("dryrun.real_help")}
        </div>
      </div>

      {error && <div className="mt-3 rounded-md bg-err/10 px-3 py-2 text-sm text-err">{error}</div>}

      {report && (
        <div className="mt-4 space-y-2 border-t border-line pt-4">
          <div className="text-[11px] uppercase tracking-wide text-fg-subtle">{t("dryrun.result")}</div>
          {report.steps.map((s, i) => (
            <div key={i} className="flex items-start gap-2 text-xs">
              <Pill tone={s.status === "ok" ? "ok" : s.status === "failed" ? "err" : "warn"}>
                {i + 1}
              </Pill>
              <div className="min-w-0">
                <span className="font-mono">{s.name}</span>
                {s.message && <span className="ml-2 break-all text-fg-muted">{s.message}</span>}
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
