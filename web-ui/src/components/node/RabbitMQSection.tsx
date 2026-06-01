import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Rabbit, PlugZap, RefreshCw, CircleCheck, CircleX } from "lucide-react";

import { api, type RMQTestResult } from "../../api/client";
import { Button, Card, Field, Hint, Input, SectionHead } from "../ui";

// §27: поля формы, относящиеся к RabbitMQAsync. Подмножество Form в NodeSettings.
export type RMQFormFields = {
  rmq_host: string;
  rmq_port: number;
  rmq_vhost: string;
  rmq_user: string;
  rmq_password: string;
  rmq_queue: string;
  rmq_use_tls: boolean;
  pull_interval_sec: number;
  pull_batch_size: number;
  pull_prefetch: number;
};

// RMQSetter — дженерик-сеттер по полям RMQFormFields. NodeSettings передаёт
// свой Form-сеттер с кастом (RMQFormFields ⊂ Form).
export type RMQSetter = <K extends keyof RMQFormFields>(k: K, v: RMQFormFields[K]) => void;

type Props = {
  form: RMQFormFields;
  set: RMQSetter;
  isNew: boolean;
};

const INTERVAL_PRESETS: { label: string; sec: number }[] = [
  { label: "1с", sec: 1 },
  { label: "5с", sec: 5 },
  { label: "30с", sec: 30 },
  { label: "1м", sec: 60 },
  { label: "5м", sec: 300 },
];

// RabbitMQSection — секция «RabbitMQ — источник» + «Параметры забора» (§27.11).
// Кнопка «Проверить подключение» дёргает POST /api/nodes/test-rmq (реальный
// AMQP-handshake без сохранения узла).
export function RabbitMQSection({ form, set, isNew }: Props) {
  const { t } = useTranslation();

  const test = useMutation({
    mutationFn: () =>
      api.post<RMQTestResult>("/api/nodes/test-rmq", {
        host: form.rmq_host,
        port: form.rmq_port || (form.rmq_use_tls ? 5671 : 5672),
        vhost: form.rmq_vhost || "/",
        user: form.rmq_user,
        password: form.rmq_password,
        queue: form.rmq_queue,
        use_tls: form.rmq_use_tls,
      }),
  });
  const res = test.data;

  return (
    <>
      <Card>
        <SectionHead icon={<Rabbit className="h-4 w-4 text-warn" />}>
          {t("node.rmq.source")}
        </SectionHead>

        <Field label={t("node.rmq.host_port")}>
          <div className="grid grid-cols-[2fr_100px_auto] gap-2">
            <Input
              mono
              value={form.rmq_host}
              onChange={(e) => set("rmq_host", e.target.value)}
              placeholder="rmq.internal.company.ru"
            />
            <Input
              mono
              type="number"
              value={form.rmq_port}
              onChange={(e) => set("rmq_port", Number(e.target.value))}
              placeholder={form.rmq_use_tls ? "5671" : "5672"}
            />
            <label className="flex items-center gap-1.5 rounded-md border border-line px-2 text-xs">
              <input
                type="checkbox"
                checked={form.rmq_use_tls}
                onChange={(e) => set("rmq_use_tls", e.target.checked)}
              />
              TLS
            </label>
          </div>
          <span className="text-[11px] text-fg-subtle">{t("node.rmq.host_hint")}</span>
        </Field>

        <Field label={t("node.rmq.vhost")} className="mt-3">
          <Input
            mono
            value={form.rmq_vhost}
            onChange={(e) => set("rmq_vhost", e.target.value)}
            placeholder="/"
          />
        </Field>

        <Field label={t("node.rmq.auth")} className="mt-3">
          <div className="grid grid-cols-2 gap-2">
            <Input
              value={form.rmq_user}
              onChange={(e) => set("rmq_user", e.target.value)}
              placeholder="user"
            />
            <Input
              type="password"
              value={form.rmq_password}
              onChange={(e) => set("rmq_password", e.target.value)}
              placeholder={isNew ? "password" : t("node.form.keep_secret")}
            />
          </div>
        </Field>

        <Field label={t("node.rmq.queue")} className="mt-3">
          <Input
            mono
            value={form.rmq_queue}
            onChange={(e) => set("rmq_queue", e.target.value)}
            placeholder="billing.events.outbound"
          />
          <span className="text-[11px] text-fg-subtle">{t("node.rmq.queue_hint")}</span>
        </Field>

        <div className="mt-3 flex justify-end">
          <Button
            onClick={() => test.mutate()}
            disabled={test.isPending || !form.rmq_host || !form.rmq_queue}
          >
            <PlugZap className="h-4 w-4" /> {t("node.rmq.test")}
          </Button>
        </div>

        {res && <TestResultBox res={res} />}
        {test.isError && (
          <Hint tone="danger" className="mt-3">
            {t("common.error")}
          </Hint>
        )}
      </Card>

      <Card>
        <SectionHead icon={<RefreshCw className="h-4 w-4" />}>{t("node.rmq.pull_params")}</SectionHead>

        <Field label={t("node.rmq.interval")} hint={t("node.rmq.interval_hint")}>
          <div className="flex items-center gap-2">
            <Input
              mono
              type="number"
              className="w-24 text-right"
              value={form.pull_interval_sec}
              onChange={(e) => set("pull_interval_sec", Number(e.target.value))}
            />
            <span className="text-xs text-fg-muted">{t("node.rmq.seconds")}</span>
            <div className="ml-auto flex gap-1">
              {INTERVAL_PRESETS.map((p) => (
                <Button
                  key={p.sec}
                  variant={form.pull_interval_sec === p.sec ? "primary" : "ghost"}
                  className="px-2 py-1 text-xs"
                  onClick={() => set("pull_interval_sec", p.sec)}
                >
                  {p.label}
                </Button>
              ))}
            </div>
          </div>
        </Field>

        <div className="mt-3 grid grid-cols-2 gap-3">
          <Field label={t("node.rmq.batch_size")} hint={t("node.rmq.batch_hint")}>
            <Input
              mono
              type="number"
              value={form.pull_batch_size}
              onChange={(e) => set("pull_batch_size", Number(e.target.value))}
            />
          </Field>
          <Field label={t("node.rmq.prefetch")} hint={t("node.rmq.prefetch_hint")}>
            <Input
              mono
              type="number"
              value={form.pull_prefetch}
              onChange={(e) => set("pull_prefetch", Number(e.target.value))}
            />
          </Field>
        </div>
      </Card>
    </>
  );
}

function TestResultBox({ res }: { res: RMQTestResult }) {
  const { t } = useTranslation();
  const rows: { name: string; check: RMQTestResult["checks"]["connect"] }[] = [
    { name: "connect", check: res.checks.connect },
    { name: "auth", check: res.checks.auth },
    { name: "queue", check: res.checks.queue },
  ];
  return (
    <div
      className={`mt-3 rounded-md border px-3 py-3 text-xs ${
        res.ok ? "border-ok/40 bg-ok/10" : "border-err/40 bg-err/10"
      }`}
    >
      <div className={`mb-2 font-semibold ${res.ok ? "text-ok" : "text-err"}`}>
        {res.ok ? t("node.rmq.test_ok") : t("node.rmq.test_fail")}
      </div>
      {rows.map((r) => (
        <div key={r.name} className="flex items-center gap-2 py-0.5">
          {r.check.ok ? (
            <CircleCheck className="h-3.5 w-3.5 shrink-0 text-ok" />
          ) : (
            <CircleX className="h-3.5 w-3.5 shrink-0 text-err" />
          )}
          <span className="w-16 font-medium">{r.name}</span>
          <span className="flex-1 text-fg-muted">
            {r.check.error
              ? r.check.error
              : r.name === "queue" && r.check.ok
                ? t("node.rmq.queue_stats", {
                    messages: r.check.message_count ?? 0,
                    consumers: r.check.consumer_count ?? 0,
                  })
                : "OK"}
          </span>
          {r.check.ok && <span className="font-mono text-fg-subtle">{r.check.elapsed_ms} ms</span>}
        </div>
      ))}
    </div>
  );
}
