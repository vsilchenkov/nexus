import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { RefreshCw, ShieldQuestion } from "lucide-react";

import { api } from "../api/client";
import { Button, Field, Hint, Input, Modal, Textarea } from "./ui";
import type { LogDetail } from "./node/types";

type ReplayResult = {
  // §85.10: идентификатор новой записи приходит ИЗ ОТВЕТА ШИНЫ и потому
  // необязателен — у sync-повтора отвечает приёмник, и идентификатора записи в
  // его ответе нет ни в каком виде. Раньше сервер присылал сюда выдуманный
  // UUID, и строка под результатом вела в никуда.
  new_log_id?: string;
  status_code: number;
  body_preview?: string;
};

// isMultipartPlaceholder — зеркало domain.IsMultipartLogPlaceholder (Go,
// internal/domain/multipart.go): §68 у multipart-запроса в теле лога хранится
// плейсхолдер, а не исходное тело. Replay без ручного тела бэкенд отклонит (422),
// поэтому предупреждаем и не даём отправить с пустым телом. Держать в синхроне с
// Go-детектом.
function isMultipartPlaceholder(body?: string): boolean {
  if (!body) return false;
  const nl = body.indexOf("\n");
  if (nl < 0) return false;
  const first = body.slice(0, nl).trim().toLowerCase();
  const rest = body.slice(nl + 1);
  return first.startsWith("multipart/") && (rest.startsWith("- part ") || rest.startsWith("["));
}

export function ReplayDialog({
  logId,
  nodeId,
  httpMethod,
  incomingMethod,
  onClose,
}: {
  logId: string;
  nodeId: string;
  /** HTTP-глагол исходного запроса из строки лога (колонка http_method, §39). */
  httpMethod?: string;
  /** Входящий метод узла — replay реинъектит именно его (ANY → глагол лога). */
  incomingMethod?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [bodyOverride, setBodyOverride] = useState("");
  const [syncOverride, setSyncOverride] = useState(false);
  const [useNodeAuth, setUseNodeAuth] = useState(true);
  const [customAuth, setCustomAuth] = useState("");
  // null = prefill из детали лога ещё не применён (после — всегда строка).
  const [params, setParams] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ReplayResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Эффективный метод реинъекции — зеркалит выбор бэкенда (replay.go):
  // node.IncomingMethod; для ANY — глагол исходного запроса; fallback POST.
  // По нему решаем, нужно ли тело: у GET его нет by design.
  const effectiveMethod = (incomingMethod === "ANY" ? httpMethod : incomingMethod) || "POST";
  const isGet = effectiveMethod === "GET";

  // Prefill поля «Параметры» из детали лога (список параметры не отдаёт).
  // Тот же queryKey, что у раскрытой строки (LogBodies) — кеш общий.
  const detail = useQuery({
    queryKey: ["log", nodeId, logId],
    queryFn: () => api.get<LogDetail>(`/api/nodes/${nodeId}/log/${logId}`),
    staleTime: 60_000,
  });
  useEffect(() => {
    if (params !== null || !detail.data) return;
    const qp = new URLSearchParams(detail.data.parameters ?? "");
    // Служебный маркер прошлого replay не должен утекать в новый запрос —
    // бэкенд поставит свежий __replay_of сам.
    qp.delete("__replay_of");
    setParams(qp.toString());
  }, [detail.data, params]);

  // §68: оригинал был multipart — тело не сохранено, нужно ввести вручную.
  const multipartOriginal = isMultipartPlaceholder(detail.data?.request);
  const needsManualBody = multipartOriginal && !bodyOverride.trim();

  async function run() {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.post<ReplayResult>(`/api/logs/${logId}/replay`, {
        node_id: nodeId,
        // Пустое поле → undefined: бэкенд возьмёт оригинал из лога (для GET
        // оригинал обычно пуст → уйдёт без тела). Непустое — уходит и для GET.
        body_override: bodyOverride || undefined,
        // null (деталь не успела загрузиться, поле нетронуто) → не слать:
        // бэкенд возьмёт параметры оригинала сам.
        params_override: params ?? undefined,
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
          <Button variant="primary" disabled={busy || needsManualBody} onClick={run}>
            <RefreshCw className="h-4 w-4" /> {t("replay.send")}
          </Button>
        </>
      }
    >
      {/* Для GET тело необязательно (обычно его нет), но задать можно —
          некоторые API принимают GET с телом (напр. поисковые запросы). */}
      <Field label={isGet ? t("replay.body_label_get") : t("replay.body_label")}>
        <Textarea rows={4} value={bodyOverride} onChange={(e) => setBodyOverride(e.target.value)} />
        {multipartOriginal && (
          <Hint tone="warn" className="mt-1">
            {t("replay.multipart_warning")}
          </Hint>
        )}
      </Field>

      <Field label={t("replay.params_label")} className="mt-3">
        <Input
          mono
          value={params ?? ""}
          onChange={(e) => setParams(e.target.value)}
          placeholder="a=1&b=2"
        />
        <Hint tone="muted" className="mt-1">
          {t("replay.params_hint")}
        </Hint>
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
          {/* Пустой идентификатор не рисуем вовсе: пустая моноширинная строка
              читается как «запись есть, но безымянная», хотя означает «шина его
              не вернула» (sync-повтор). */}
          {result.new_log_id && (
            <div className="font-mono text-xs text-fg-muted">{result.new_log_id}</div>
          )}
        </div>
      )}
    </Modal>
  );
}
