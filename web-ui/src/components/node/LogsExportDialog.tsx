import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Download, RefreshCw } from "lucide-react";

import { api, type Node } from "../../api/client";
import { Button, ErrorAlert, Hint, Modal, Seg, Select } from "../ui";
import { saveBlob } from "../../lib/download";
import { fmtSize } from "../../lib/format";
import {
  advFormEqual,
  logsFilterParams,
  type LogsAdvForm,
  type LogsDoneFilter,
  type LogsFilterState,
  type LogsStatusFilter,
} from "../../lib/logsQuery";
import {
  browserTimeZone,
  exportErrorText,
  exportParams,
  exportPlan,
  exportRowChoices,
  exportRowLimit,
  isExportCanceled,
  type ExportRowsChoice,
  type LogsExportEstimate,
} from "../../lib/logsExport";
import { LogsAdvancedFilters } from "./LogsAdvancedFilters";

// §115.4: выгрузка журнала узла в Excel.
//
// Один экран (эскиз утверждён): фильтры вкладки «Логи» подставлены и
// редактируемы, оценка пересчитывается сама, выгрузка идёт потоком с
// прогрессом и «Остановить». Полнота файла (упёрлись в объём, ClickHouse
// отказал посреди потока) видна только на листе «Параметры»: HTTP-статус
// уходит до первой строки и потом не меняется.

// DownloadState — шаг выгрузки. loaded — сколько байт файла уже пришло.
type DownloadState =
  | { phase: "idle" }
  | { phase: "running"; loaded: number }
  | { phase: "done"; loaded: number }
  | { phase: "stopped" }
  | { phase: "error"; error: string };

// FALLBACK_FILE — имя, если заголовок с именем файла не дошёл (прокси срезал).
const FALLBACK_FILE = "nexus-logs.xlsx";

function advOf(f: LogsFilterState): LogsAdvForm {
  return {
    q: f.q,
    qCase: f.qCase,
    qWord: f.qWord,
    qRegex: f.qRegex,
    method: f.method,
    clientIp: f.clientIp,
    clientHost: f.clientHost,
    from: f.from,
    to: f.to,
  };
}

export function LogsExportDialog({ node, initial, onClose }: {
  node: Node;
  /** Фильтры вкладки «Логи» на момент открытия. */
  initial: LogsFilterState;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [draft, setDraft] = useState<LogsAdvForm>(() => advOf(initial));
  const [applied, setApplied] = useState<LogsAdvForm>(() => advOf(initial));
  const [status, setStatus] = useState<LogsStatusFilter>(initial.status);
  const [done, setDone] = useState<LogsDoneFilter>(initial.done);
  const [rowsChoice, setRowsChoice] = useState<ExportRowsChoice>("max");
  const [bodies, setBodies] = useState(true);
  const [dl, setDl] = useState<DownloadState>({ phase: "idle" });
  const ctrlRef = useRef<AbortController | null>(null);

  // Закрытие диалога посреди выгрузки рвёт её — и запрос в ClickHouse вместе
  // с ней (разрыв соединения отменяет контекст хендлера).
  useEffect(() => () => ctrlRef.current?.abort(), []);

  // §77.3: коммит идемпотентен — Enter+blur не дают двух одинаковых оценок.
  const commit = (next: LogsAdvForm) => {
    setDraft(next);
    setApplied((prev) => (advFormEqual(prev, next) ? prev : next));
  };

  const filter = useMemo<LogsFilterState>(() => ({ ...applied, status, done }), [applied, status, done]);
  const params = useMemo(() => logsFilterParams(filter), [filter]);
  const estQ = useQuery({
    queryKey: ["logs-export-estimate", node.id, params],
    queryFn: ({ signal }) =>
      api.get<LogsExportEstimate>(`/api/nodes/${node.id}/logs/export/estimate`, params, { signal }),
    placeholderData: keepPreviousData, // пока считается новая оценка, видна прошлая
    retry: false,
  });
  const badQuery = (estQ.error as { response?: { status?: number } } | null)?.response?.status === 400;
  const est = estQ.data;
  // Оценка не пришла (сеть) — потолок неизвестен: пресет уходит как есть,
  // «все» — без rows, и сервер берёт свой max_rows. Выгрузку это не блокирует.
  const rowLimit = exportRowLimit(rowsChoice, est?.max_rows ?? 0);
  const running = dl.phase === "running";

  async function doExport() {
    const ctrl = new AbortController();
    ctrlRef.current = ctrl;
    setDl({ phase: "running", loaded: 0 });
    // Выгружается то, что видно в полях (черновик), даже если поиск не
    // подтверждён Enter'ом: кнопка не должна выгружать «не то, что на экране».
    const f: LogsFilterState = { ...draft, status, done };
    try {
      const { blob, filename } = await api.getBlob(
        `/api/nodes/${node.id}/logs/export.xlsx`,
        exportParams(f, { rows: rowLimit, bodies, tz: browserTimeZone() }),
        {
          signal: ctrl.signal,
          headers: { "Accept-Language": i18n.language },
          onDownloadProgress: (loaded) => setDl({ phase: "running", loaded }),
        },
      );
      saveBlob(blob, filename ?? FALLBACK_FILE);
      setDl({ phase: "done", loaded: blob.size });
    } catch (e) {
      setDl(isExportCanceled(e) ? { phase: "stopped" } : { phase: "error", error: exportErrorText(e, t) });
    } finally {
      if (ctrlRef.current === ctrl) ctrlRef.current = null;
    }
  }

  const nothing = est !== undefined && est.logs_available && est.records === 0;
  return (
    <Modal
      title={t("logs.export.title")}
      subtitle={
        <span className="font-mono">
          {node.path} · {node.root_method}
        </span>
      }
      onClose={onClose}
      className="max-w-3xl"
      footer={
        <ExportFooter
          dl={dl}
          disabled={badQuery || nothing}
          onClose={onClose}
          onExport={doExport}
          onStop={() => ctrlRef.current?.abort()}
        />
      }
    >
      <fieldset disabled={running} className="space-y-4">
        <section className="space-y-2">
          <h4 className="text-[11px] uppercase tracking-wider text-fg-muted">{t("logs.export.filters")}</h4>
          <div className="-mx-1 rounded-md border border-line">
            <LogsAdvancedFilters
              nodeId={node.id}
              draft={draft}
              applied={applied}
              onDraft={setDraft}
              onCommit={commit}
              badQuery={badQuery}
            />
            <div className="flex flex-wrap items-center gap-3 px-4 py-3">
              <span className="text-[10px] uppercase tracking-wider text-fg-muted">{t("logs.export.state")}</span>
              <Seg
                value={status}
                onChange={setStatus}
                options={[
                  { value: "all", label: t("logs.filter.all") },
                  { value: "ok", label: t("logs.filter.ok") },
                  { value: "err", label: t("logs.filter.err") },
                ]}
              />
              {/* §77.4: done приходит только дип-линком «Очереди» — виден чипом
                  и снимается, своего переключателя у него нет. */}
              {done !== "all" && (
                <button
                  type="button"
                  onClick={() => setDone("all")}
                  title={t("logs.advanced.reset")}
                  className="inline-flex items-center gap-1 rounded-full bg-accent/15 px-2 py-0.5 text-xs text-accent hover:bg-accent/25"
                >
                  {done === "done" ? t("logs.filter.done_yes") : t("logs.filter.done_no")}
                  <span aria-hidden>✕</span>
                </button>
              )}
            </div>
          </div>
        </section>

        <ExportVolume
          est={est}
          estFailed={estQ.isError && !badQuery}
          estFetching={estQ.isFetching}
          rowsChoice={rowsChoice}
          onRowsChoice={setRowsChoice}
          rowLimit={rowLimit}
          bodies={bodies}
          onBodies={setBodies}
        />

        {!draft.from && !draft.to && (
          <Hint tone="warn" icon={<AlertTriangle className="h-3.5 w-3.5" />}>
            {draft.q ? t("logs.export.warn_no_period_search") : t("logs.export.warn_no_period")}
          </Hint>
        )}
        {bodies && (
          <Hint tone="warn" icon={<AlertTriangle className="h-3.5 w-3.5" />}>
            {t("logs.export.warn_bodies")}
          </Hint>
        )}
      </fieldset>
      <ExportResult dl={dl} />
    </Modal>
  );
}

// ExportVolume — предел строк, тела и оценка того, что окажется в файле.
function ExportVolume({ est, estFailed, estFetching, rowsChoice, onRowsChoice, rowLimit, bodies, onBodies }: {
  est: LogsExportEstimate | undefined;
  estFailed: boolean;
  estFetching: boolean;
  rowsChoice: ExportRowsChoice;
  onRowsChoice: (c: ExportRowsChoice) => void;
  rowLimit: number;
  bodies: boolean;
  onBodies: (b: boolean) => void;
}) {
  const { t } = useTranslation();
  const rowsId = useId();
  const maxRows = est?.max_rows ?? 0;
  const choices = maxRows > 0 ? exportRowChoices(maxRows) : (["1000", "10000", "max"] as ExportRowsChoice[]);
  return (
    <section className="space-y-2">
      <h4 className="text-[11px] uppercase tracking-wider text-fg-muted">{t("logs.export.volume")}</h4>
      <div className="flex flex-wrap items-center gap-3">
        <label htmlFor={rowsId} className="text-[13px]">
          {t("logs.export.rows_label")}
        </label>
        <Select
          id={rowsId}
          className="w-36"
          value={rowsChoice}
          onChange={(e) => onRowsChoice(e.target.value as ExportRowsChoice)}
        >
          {choices.map((c) => (
            <option key={c} value={c}>
              {c !== "max"
                ? Number(c).toLocaleString()
                : maxRows > 0
                  ? maxRows.toLocaleString()
                  : t("logs.export.rows_max_unknown")}
            </option>
          ))}
        </Select>
      </div>
      <label className="flex items-center gap-2 text-[13px]">
        <input type="checkbox" checked={bodies} onChange={(e) => onBodies(e.target.checked)} />
        {est
          ? t("logs.export.bodies", { n: est.max_body_chars.toLocaleString() })
          : t("logs.export.bodies_plain")}
      </label>
      <ExportEstimate est={est} failed={estFailed} fetching={estFetching} rowLimit={rowLimit} bodies={bodies} />
    </section>
  );
}

// ExportEstimate — блок «Оценка»: найдено, в файл, объём и упоры в лимиты.
function ExportEstimate({ est, failed, fetching, rowLimit, bodies }: {
  est: LogsExportEstimate | undefined;
  failed: boolean;
  fetching: boolean;
  rowLimit: number;
  bodies: boolean;
}) {
  const { t } = useTranslation();
  const units = useMemo(() => t("logs.size_units").split("|"), [t]);
  const box = (children: ReactNode) => (
    <div className="space-y-1 rounded-md border border-line px-3 py-2 text-[13px]" aria-live="polite">
      <div className="flex items-center gap-2 text-[11px] uppercase tracking-wider text-fg-muted">
        {t("logs.export.estimate")}
        {fetching && <RefreshCw className="h-3 w-3 animate-spin text-accent" />}
      </div>
      {children}
    </div>
  );

  if (failed || (est && !est.logs_available)) {
    return box(<div className="text-fg-muted">{t("logs.export.estimate_unavailable")}</div>);
  }
  if (!est) return box(<div className="text-fg-muted">{t("logs.export.estimate_loading")}</div>);
  if (est.records === 0) return box(<div className="text-fg-muted">{t("logs.export.nothing")}</div>);

  const plan = exportPlan(est, rowLimit, bodies);
  return box(
    <>
      <div>
        {t("logs.export.found", {
          records: est.records.toLocaleString(),
          inFile: plan.inFile.toLocaleString(),
        })}
      </div>
      <div>{t("logs.export.size", { size: fmtSize(plan.bytes, units) })}</div>
      {plan.limitedByRows && (
        <div className="text-fg-muted">{t("logs.export.limited_rows", { n: plan.inFile.toLocaleString() })}</div>
      )}
      {plan.limitedByBytes && (
        <div className="text-warn">
          {t("logs.export.limited_bytes", {
            max: fmtSize(est.max_bytes, units),
            n: plan.fitRows.toLocaleString(),
          })}
        </div>
      )}
    </>,
  );
}

// ExportResult — итог выгрузки под формой.
function ExportResult({ dl }: { dl: DownloadState }) {
  const { t } = useTranslation();
  const units = useMemo(() => t("logs.size_units").split("|"), [t]);
  switch (dl.phase) {
    case "done":
      return (
        <Hint tone="info" className="mt-4">
          {t("logs.export.saved", { size: fmtSize(dl.loaded, units) })}
        </Hint>
      );
    case "stopped":
      return (
        <Hint tone="muted" className="mt-4">
          {t("logs.export.stopped")}
        </Hint>
      );
    case "error":
      return <ErrorAlert className="mt-4">{dl.error}</ErrorAlert>;
    default:
      return null;
  }
}

// ExportFooter — кнопки и прогресс. Длины файла заранее нет (он пишется по
// мере чтения журнала), поэтому прогресс — полученные байты и бегущая полоса.
function ExportFooter({ dl, disabled, onClose, onExport, onStop }: {
  dl: DownloadState;
  disabled: boolean;
  onClose: () => void;
  onExport: () => void;
  onStop: () => void;
}) {
  const { t } = useTranslation();
  const units = useMemo(() => t("logs.size_units").split("|"), [t]);
  if (dl.phase === "running") {
    return (
      <div className="flex w-full items-center gap-3">
        <span className="text-[13px] text-fg-muted">
          {t("logs.export.running", { size: dl.loaded > 0 ? fmtSize(dl.loaded, units) : "…" })}
        </span>
        <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-bg-muted">
          <div className="h-full w-1/3 animate-pulse rounded-full bg-accent" />
        </div>
        <Button variant="ghost" onClick={onStop}>
          {t("logs.export.stop")}
        </Button>
      </div>
    );
  }
  return (
    <>
      <Button variant="ghost" onClick={onClose}>
        {dl.phase === "done" ? t("common.close") : t("common.cancel")}
      </Button>
      <Button variant="primary" disabled={disabled} onClick={onExport}>
        <Download className="h-4 w-4" /> {t("logs.export.submit")}
      </Button>
    </>
  );
}
