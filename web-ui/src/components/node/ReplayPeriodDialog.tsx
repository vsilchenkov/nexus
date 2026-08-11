import { useCallback, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, RotateCcw } from "lucide-react";

import { api, type Node } from "../../api/client";
import {
  Button,
  ErrorAlert,
  Hint,
  Modal,
  PeriodPicker,
  Seg,
  defaultPeriod,
  periodWindow,
  type Period,
} from "../ui";
import { nodeLookbackMs } from "../../lib/nodeLookback";
import { emptyAdvForm, logsFilterParams, type LogsAdvForm, type LogsStatusFilter } from "../../lib/logsQuery";
import { fmtBytes, fmtLogTs } from "../../lib/format";
import { LogsAdvancedFilters } from "./LogsAdvancedFilters";

// §85: повторная отправка запросов из логов за период.
//
// Три шага в одном диалоге: период и фильтр → предпросмотр (что уйдёт и что нет
// с причинами) → прогон батчами по курсору с прогрессом и остановкой.
//
// Цикл батчей крутит КЛИЕНТ (§85.5): окно может содержать десятки тысяч
// записей, и один HTTP-запрос на такую работу недопустим. Курсор приходит с
// сервера и уходит обратно, поэтому продолжение не переотправляет уже ушедшее —
// «нажать ещё раз», как в §79.2, здесь не работает: там множество сужается
// самим успехом, а тут набор от отправки не меняется.

type ReplayCandidateView = {
  id: string;
  date_request: string;
  http_method: string;
  method?: string;
  url: string;
  status: number;
  done: boolean;
  request_size: number;
  skip_reason?: string;
};

type ReplayPeriodPlan = {
  total: number;
  scanned: number;
  exact: boolean;
  eligible: number;
  skipped_by: Record<string, number>;
  eligibles: ReplayCandidateView[];
  ineligibles: ReplayCandidateView[];
  from: string;
  to: string;
};

type ReplayCursor = { after_ms: number; after_id: string };

type ReplayPeriodBatch = {
  scanned: number;
  replayed: number;
  failed: number;
  skipped_by: Record<string, number>;
  next_cursor: ReplayCursor | null;
};

// runState — накопленный итог прогона (батчи складываются).
type RunState = {
  scanned: number;
  replayed: number;
  failed: number;
  skipped: number;
  /** Отметка последней обработанной записи — точка продолжения после остановки. */
  atMs: number;
  /** stopped=true — оператор нажал «Остановить»; done=true — окно пройдено. */
  stopped: boolean;
  done: boolean;
};

const emptyRun: RunState = {
  scanned: 0,
  replayed: 0,
  failed: 0,
  skipped: 0,
  atMs: 0,
  stopped: false,
  done: false,
};

// BATCH_LIMIT — сколько записей просим за один вызов. Меньше серверного потолка
// намеренно: чем короче батч, тем отзывчивее прогресс и точнее остановка.
const BATCH_LIMIT = 100;

// MAX_BATCHES — предохранитель от бесконечного цикла, если сервер почему-то
// перестанет двигать курсор. Молчаливая вечная прокрутка хуже явной остановки.
const MAX_BATCHES = 5000;

function errText(e: unknown, fallback: string): string {
  const err = e as { response?: { data?: { error?: string } } };
  return err?.response?.data?.error ?? fallback;
}

export function ReplayPeriodDialog({ node, onClose, onFinished }: {
  node: Node;
  onClose: () => void;
  /** Вызывается после прогона: вкладка обновляет KPI и списки. */
  onFinished?: () => void;
}) {
  const { t } = useTranslation();
  const [period, setPeriod] = useState<Period>(defaultPeriod);
  const [adv, setAdv] = useState<LogsAdvForm>(emptyAdvForm);
  const [status, setStatus] = useState<LogsStatusFilter>("all");
  const [skipCopies, setSkipCopies] = useState(true);

  const [plan, setPlan] = useState<ReplayPeriodPlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [run, setRun] = useState<RunState | null>(null);
  const stopRef = useRef(false);

  // filterParams — те же параметры, что шлёт журнал (§72.2): один источник на
  // список, счётчик и обе операции §85. Окно берётся из пикера, а не из полей
  // даты панели: они в диалоге скрыты (showDates=false).
  const filterParams = useCallback(
    (from: string, to: string) => ({
      ...logsFilterParams({ ...adv, from: "", to: "", status, done: "all" }),
      from,
      to,
    }),
    [adv, status],
  );

  const windowIso = useMemo(() => {
    const { since, until } = periodWindow(period);
    return { from: new Date(since).toISOString(), to: new Date(until).toISOString() };
  }, [period]);

  async function doPlan() {
    setBusy(true);
    setError(null);
    try {
      const w = windowIso;
      const res = await api.post<ReplayPeriodPlan>(
        `/api/nodes/${node.id}/logs/replay-period/plan`,
        { skip_replay_copies: skipCopies },
        { params: filterParams(w.from, w.to) },
      );
      setPlan(res);
    } catch (e) {
      setError(errText(e, t("common.error")));
    } finally {
      setBusy(false);
    }
  }

  // doRun — цикл батчей. Границы окна берутся ИЗ ПЛАНА (§85.6): пересчитывать
  // «по сейчас» на каждом батче значило бы затягивать в набор собственные
  // повторы — прогон начал бы кормить сам себя.
  async function doRun() {
    if (!plan) return;
    stopRef.current = false;
    setBusy(true);
    setError(null);
    setRun({ ...emptyRun });

    const params = filterParams(plan.from, plan.to);
    let cursor: ReplayCursor | null = null;
    const acc: RunState = { ...emptyRun };

    try {
      for (let i = 0; i < MAX_BATCHES; i++) {
        const batch: ReplayPeriodBatch = await api.post<ReplayPeriodBatch>(
          `/api/nodes/${node.id}/logs/replay-period/run`,
          { cursor, limit: BATCH_LIMIT, skip_replay_copies: skipCopies },
          { params },
        );
        acc.scanned += batch.scanned;
        acc.replayed += batch.replayed;
        acc.failed += batch.failed;
        acc.skipped += Object.values(batch.skipped_by ?? {}).reduce((a, b) => a + b, 0);
        if (batch.next_cursor) acc.atMs = batch.next_cursor.after_ms;
        setRun({ ...acc });

        if (!batch.next_cursor) {
          acc.done = true;
          break;
        }
        if (stopRef.current) {
          acc.stopped = true;
          break;
        }
        cursor = batch.next_cursor;
      }
      setRun({ ...acc });
    } catch (e) {
      setError(errText(e, t("common.error")));
      setRun({ ...acc, stopped: true });
    } finally {
      setBusy(false);
      onFinished?.();
    }
  }

  const skipped = plan ? plan.scanned - plan.eligible : 0;
  const finished = run !== null && !busy;

  return (
    <Modal
      title={t("replay_period.title")}
      subtitle={
        <span className="font-mono">
          {node.path} · {node.root_method}
        </span>
      }
      onClose={onClose}
      className="max-w-3xl"
      footer={
        <ReplayPeriodFooter
          busy={busy}
          plan={plan}
          run={run}
          onClose={onClose}
          onPlan={doPlan}
          onBack={() => {
            setPlan(null);
            setRun(null);
          }}
          onRun={doRun}
          onStop={() => {
            stopRef.current = true;
          }}
        />
      }
    >
      {run === null && plan === null && (
        <div className="space-y-4">
          <section className="space-y-2">
            <h4 className="text-[11px] uppercase tracking-wider text-fg-muted">
              {t("replay_period.step_period")}
            </h4>
            <PeriodPicker value={period} onChange={setPeriod} maxLookbackMs={nodeLookbackMs(node)} />
          </section>

          <section className="space-y-2">
            <h4 className="text-[11px] uppercase tracking-wider text-fg-muted">
              {t("replay_period.step_filter")}
            </h4>
            {/* showDates=false — окно задаёт пикер выше; два контрола, пишущих
                в одни границы, затирали бы друг друга (приём §79.4). */}
            <div className="-mx-1 rounded-md border border-line">
              <LogsAdvancedFilters
                nodeId={node.id}
                draft={adv}
                applied={adv}
                onDraft={setAdv}
                onCommit={setAdv}
                showDates={false}
              />
              <div className="flex flex-wrap items-center gap-3 px-4 py-3">
                <span className="text-[10px] uppercase tracking-wider text-fg-muted">
                  {t("replay_period.state")}
                </span>
                <Seg
                  value={status}
                  onChange={setStatus}
                  options={[
                    { value: "all", label: t("logs.filter.all") },
                    { value: "ok", label: t("logs.filter.ok") },
                    { value: "err", label: t("logs.filter.err") },
                  ]}
                />
              </div>
            </div>
            <label className="flex items-center gap-2 text-[13px]">
              <input
                type="checkbox"
                checked={skipCopies}
                onChange={(e) => setSkipCopies(e.target.checked)}
              />
              {t("replay_period.skip_copies")}
            </label>
          </section>

          <Hint tone="muted" icon={<AlertTriangle className="h-3.5 w-3.5" />}>
            {t("replay_period.frozen_bounds")}
          </Hint>
          {error && <ErrorAlert>{error}</ErrorAlert>}
        </div>
      )}

      {run === null && plan !== null && (
        <PlanView plan={plan} skipped={skipped} error={error} />
      )}

      {run !== null && <RunView run={run} plan={plan} busy={busy} error={error} finished={finished} />}
    </Modal>
  );
}

// PlanView — предпросмотр: два множества, разбивка по причинам и примеры.
function PlanView({ plan, skipped, error }: {
  plan: ReplayPeriodPlan;
  skipped: number;
  error: string | null;
}) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"ok" | "skip">("ok");
  // ?? [] — Go отдаёт пустой срез как null, а не []: без страховки диалог
  // падал бы ровно на «отправлять нечего», то есть в самом безобидном случае.
  const rows = (tab === "ok" ? plan.eligibles : plan.ineligibles) ?? [];

  return (
    <div className="space-y-3">
      <div className="text-xs text-fg-muted">
        {t("replay_period.window")}: {fmtLogTs(plan.from)} — {fmtLogTs(plan.to)}
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="rounded-md border border-line px-3 py-2">
          <div className="text-[11px] uppercase tracking-wider text-fg-muted">
            {t("replay_period.will_send")}
          </div>
          <div className="text-xl font-semibold text-ok">{plan.eligible}</div>
        </div>
        <div className="rounded-md border border-line px-3 py-2">
          <div className="text-[11px] uppercase tracking-wider text-fg-muted">
            {t("replay_period.will_skip")}
          </div>
          <div className="text-xl font-semibold text-warn">{skipped}</div>
          <ul className="mt-1 space-y-0.5 text-xs text-fg-muted">
            {Object.entries(plan.skipped_by ?? {}).map(([reason, n]) => (
              <li key={reason}>
                • {t(`replay_period.reason.${reason}`)} — {n}
              </li>
            ))}
          </ul>
        </div>
      </div>

      <Hint tone={plan.exact ? "muted" : "warn"}>
        {plan.exact
          ? t("replay_period.exact", { total: plan.total })
          : t("replay_period.not_exact", { scanned: plan.scanned, total: plan.total })}
      </Hint>

      <Seg
        value={tab}
        onChange={setTab}
        options={[
          { value: "ok", label: t("replay_period.will_send") },
          { value: "skip", label: t("replay_period.will_skip") },
        ]}
      />
      <div className="max-h-64 overflow-auto rounded-md border border-line">
        {rows.length === 0 ? (
          <div className="px-3 py-4 text-center text-xs text-fg-muted">{t("replay_period.empty")}</div>
        ) : (
          <table className="w-full text-left text-xs">
            <thead className="sticky top-0 bg-app text-[10px] uppercase tracking-wider text-fg-muted">
              <tr>
                <th className="px-2 py-2">{t("queue.col.received")}</th>
                <th className="px-2 py-2">{t("queue.col.method")}</th>
                <th className="px-2 py-2">{t("queue.failed.col.status")}</th>
                <th className="px-2 py-2 text-right">{t("replay_period.col.size")}</th>
                <th className="px-2 py-2">{t("replay_period.col.reason")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} className="border-t border-line/60">
                  <td className="whitespace-nowrap px-2 py-1.5 font-mono">{fmtLogTs(r.date_request)}</td>
                  <td className="px-2 py-1.5 font-mono">
                    {r.http_method}
                    {r.method ? ` ${r.method}` : ""}
                  </td>
                  <td className="px-2 py-1.5">{r.status}</td>
                  <td className="px-2 py-1.5 text-right font-mono">{fmtBytes(r.request_size)}</td>
                  <td className="px-2 py-1.5 text-warn">
                    {r.skip_reason ? t(`replay_period.reason.${r.skip_reason}`) : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <Hint tone="warn" icon={<AlertTriangle className="h-3.5 w-3.5" />}>
        <div className="space-y-1">
          <p>{t("replay_period.warn_duplicates")}</p>
          <p>{t("replay_period.warn_throughput")}</p>
        </div>
      </Hint>
      {error && <ErrorAlert>{error}</ErrorAlert>}
    </div>
  );
}

// RunView — прогресс прогона и его итог.
function RunView({ run, plan, busy, error, finished }: {
  run: RunState;
  plan: ReplayPeriodPlan | null;
  busy: boolean;
  error: string | null;
  finished: boolean;
}) {
  const { t } = useTranslation();
  const total = plan?.eligible ?? 0;
  const pct = total > 0 ? Math.min(100, Math.round((run.scanned / Math.max(total, 1)) * 100)) : 0;

  return (
    <div className="space-y-3">
      {busy && <p className="text-sm">{t("replay_period.running")}</p>}
      <div className="h-2 w-full overflow-hidden rounded-full bg-bg-muted">
        <div className="h-full bg-accent transition-all" style={{ width: `${pct}%` }} />
      </div>
      <div className="text-sm">
        {t("replay_period.progress", {
          replayed: run.replayed,
          failed: run.failed,
          skipped: run.skipped,
        })}
      </div>
      {run.atMs > 0 && (
        <div className="text-xs text-fg-muted">
          {t("replay_period.processed_until")}: {fmtLogTs(new Date(run.atMs).toISOString())}
        </div>
      )}
      {finished && (
        <Hint tone={run.done ? "muted" : "warn"}>
          {run.done
            ? t("replay_period.done", { replayed: run.replayed, failed: run.failed })
            : t("replay_period.stopped", { replayed: run.replayed })}
        </Hint>
      )}
      {error && <ErrorAlert>{error}</ErrorAlert>}
    </div>
  );
}

// ReplayPeriodFooter — кнопки шага. Вынесено, чтобы не городить тернарники в
// разметке диалога.
function ReplayPeriodFooter({ busy, plan, run, onClose, onPlan, onBack, onRun, onStop }: {
  busy: boolean;
  plan: ReplayPeriodPlan | null;
  run: RunState | null;
  onClose: () => void;
  onPlan: () => void;
  onBack: () => void;
  onRun: () => void;
  onStop: () => void;
}) {
  const { t } = useTranslation();

  if (run !== null) {
    return busy ? (
      <Button variant="ghost" onClick={onStop}>
        {t("replay_period.stop")}
      </Button>
    ) : (
      <Button variant="primary" onClick={onClose}>
        {t("common.close")}
      </Button>
    );
  }
  if (plan === null) {
    return (
      <>
        <Button variant="ghost" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button variant="primary" disabled={busy} onClick={onPlan}>
          {busy ? "…" : t("replay_period.check")}
        </Button>
      </>
    );
  }
  return (
    <>
      <Button variant="ghost" onClick={onBack}>
        {t("replay_period.back")}
      </Button>
      <Button variant="primary" disabled={busy || plan.eligible === 0} onClick={onRun}>
        <RotateCcw className="h-4 w-4" /> {t("replay_period.send", { n: plan.eligible })}
      </Button>
    </>
  );
}
