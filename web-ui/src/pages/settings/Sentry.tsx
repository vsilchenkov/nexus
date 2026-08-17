import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";

type SentrySettings = {
  use?: boolean;
  dsn?: string;
  environment?: string;
  level?: number;
  attach_stacktrace?: boolean;
  enable_tracing?: boolean;
  traces_sample_rate?: number;
};

type ClickHouseSettings = Record<string, unknown>;

type AppSettings = {
  sentry: SentrySettings;
  clickhouse: ClickHouseSettings;
  updated_at?: string;
  updated_by?: string;
};

const levelOptions = [
  { value: 0, label: "debug" },
  { value: 1, label: "info" },
  { value: 2, label: "warn" },
  { value: 3, label: "error" },
];

export function SentryPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });

  const [form, setForm] = useState<SentrySettings>({});

  useEffect(() => {
    if (data) setForm({ ...data.sentry });
  }, [data]);

  const save = useMutation({
    mutationFn: () => api.put("/api/settings/app", { sentry: form }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["app-settings"] });
    },
  });

  // Phase 6.3.2.6: test connection с patch'ем без сохранения.
  // Backend создаёт изолированный sentry.Client, отправляет CaptureMessage
  // и ждёт Flush — глобальный hub не затрагивается.
  const test = useMutation({
    mutationFn: () =>
      api.post<{ ok: boolean; latency_ms?: number; error?: string }>(
        "/api/settings/sentry/test",
        form,
      ),
  });

  if (isLoading) {
    return <div className="text-fg-muted">{t("common.loading")}</div>;
  }

  const dsnIsMasked =
    !!form.dsn && (form.dsn === "***" || /^.{0,8}\*{3}/.test(form.dsn));

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("settings.sentry.title")}</h2>
        {data?.updated_at && (
          <span className="text-xs text-fg-muted">
            {t("settings.common.updated_at")}:{" "}
            {new Date(data.updated_at).toLocaleString()}
          </span>
        )}
      </header>

      <p className="text-sm text-fg-muted">{t("settings.sentry.hint")}</p>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 max-w-3xl">
        <label className="flex items-center gap-2 text-sm md:col-span-2">
          <input
            type="checkbox"
            checked={!!form.use}
            onChange={(e) => setForm({ ...form, use: e.target.checked })}
          />
          {t("settings.sentry.use")}
        </label>

        <div className="space-y-1 md:col-span-2">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.sentry.dsn")}
          </label>
          <input
            type="text"
            autoComplete="off"
            value={form.dsn ?? ""}
            onChange={(e) => setForm({ ...form, dsn: e.target.value })}
            placeholder="https://<key>@o0.ingest.sentry.io/0"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
          />
          {dsnIsMasked && (
            <p className="text-xs text-fg-muted">
              {t("settings.common.masked_hint")}
            </p>
          )}
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.sentry.environment")}
          </label>
          <input
            type="text"
            value={form.environment ?? ""}
            onChange={(e) =>
              setForm({ ...form, environment: e.target.value })
            }
            placeholder="production"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.sentry.level")}
          </label>
          <select
            value={form.level ?? 2}
            onChange={(e) =>
              setForm({ ...form, level: Number(e.target.value) })
            }
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          >
            {levelOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </div>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={!!form.attach_stacktrace}
            onChange={(e) =>
              setForm({ ...form, attach_stacktrace: e.target.checked })
            }
          />
          {t("settings.sentry.attach_stacktrace")}
        </label>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={!!form.enable_tracing}
            onChange={(e) =>
              setForm({ ...form, enable_tracing: e.target.checked })
            }
          />
          {t("settings.sentry.enable_tracing")}
        </label>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.sentry.traces_sample_rate")}
          </label>
          <input
            type="number"
            step={0.05}
            min={0}
            max={1}
            value={form.traces_sample_rate ?? 0}
            onChange={(e) =>
              setForm({
                ...form,
                traces_sample_rate: Number(e.target.value),
              })
            }
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>
      </div>

      <div className="flex items-center gap-3 flex-wrap">
        <button
          onClick={() => save.mutate()}
          disabled={save.isPending}
          className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
        >
          {t("common.save")}
        </button>
        <button
          onClick={() => test.mutate()}
          disabled={test.isPending || !form.use}
          className="bg-bg-muted hover:bg-bg-muted/70 px-4 py-2 rounded-md text-sm disabled:opacity-50"
        >
          {test.isPending
            ? t("settings.common.testing")
            : t("settings.common.test")}
        </button>
        {save.isSuccess && (
          <span className="text-ok text-sm">
            {t("settings.common.saved")}
          </span>
        )}
        {save.isError && (
          <span className="text-err text-sm">{t("common.error")}</span>
        )}
        {test.isSuccess && test.data?.ok && (
          <span className="text-ok text-sm">
            {t("settings.common.test_ok", { ms: test.data.latency_ms ?? 0 })}
          </span>
        )}
        {test.isSuccess && !test.data?.ok && (
          <span className="text-err text-sm">
            {t("settings.common.test_failed", {
              error: test.data?.error ?? "?",
            })}
          </span>
        )}
        {test.isError && (
          <span className="text-err text-sm">
            {t("common.error")}
          </span>
        )}
      </div>

      <p className="text-xs text-fg-muted">
        {t("settings.common.hot_reload_note")}
      </p>
    </div>
  );
}
