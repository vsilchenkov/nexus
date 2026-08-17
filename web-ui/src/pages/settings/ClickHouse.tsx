import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { OrphanTablesPanel } from "../../components/OrphanTablesPanel";
import { CHTemplatesPanel } from "../../components/CHTemplatesPanel";

type ClickHouseSettings = {
  host?: string;
  port?: number;
  database?: string;
  user?: string;
  password?: string;
  batch_size?: number;
  flush_interval_sec?: number;
  buffer_max_size?: number;
  workers?: number;
};

type SentrySettings = Record<string, unknown>;

type AppSettings = {
  sentry: SentrySettings;
  clickhouse: ClickHouseSettings;
  updated_at?: string;
  updated_by?: string;
};

export function ClickHousePanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });

  const [form, setForm] = useState<ClickHouseSettings>({});

  useEffect(() => {
    if (data) setForm({ ...data.clickhouse });
  }, [data]);

  const save = useMutation({
    mutationFn: () => api.put("/api/settings/app", { clickhouse: form }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["app-settings"] });
    },
  });

  // Phase 6.3.2.6: test connection с patch'ем без сохранения.
  // Backend merge'ит form с current settings, открывает временный conn,
  // делает Ping и возвращает {ok, latency_ms} или {error}.
  const test = useMutation({
    mutationFn: () =>
      api.post<{ ok: boolean; latency_ms?: number; error?: string }>(
        "/api/settings/clickhouse/test",
        form,
      ),
  });

  if (isLoading) {
    return <div className="text-fg-muted">{t("common.loading")}</div>;
  }

  const passwordIsMasked = form.password === "***";

  const num = (v: number | undefined) => (v === undefined ? "" : v);
  const setNum = (key: keyof ClickHouseSettings, val: string) =>
    setForm({
      ...form,
      [key]: val === "" ? undefined : Number(val),
    });

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          {t("settings.clickhouse.title")}
        </h2>
        {data?.updated_at && (
          <span className="text-xs text-fg-muted">
            {t("settings.common.updated_at")}:{" "}
            {new Date(data.updated_at).toLocaleString()}
          </span>
        )}
      </header>

      <p className="text-sm text-fg-muted">{t("settings.clickhouse.hint")}</p>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 max-w-3xl">
        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.host")}
          </label>
          <input
            type="text"
            autoComplete="off"
            value={form.host ?? ""}
            onChange={(e) => setForm({ ...form, host: e.target.value })}
            placeholder="clickhouse"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.port")}
          </label>
          <input
            type="number"
            value={num(form.port)}
            onChange={(e) => setNum("port", e.target.value)}
            placeholder="9000"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.database")}
          </label>
          <input
            type="text"
            autoComplete="off"
            value={form.database ?? ""}
            onChange={(e) => setForm({ ...form, database: e.target.value })}
            placeholder="nexus"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.user")}
          </label>
          {/* autoComplete="off" + new-password на пароле ниже: без них Chrome
              считает эту пару «формой логина» и на открытии страницы вписывает
              сюда сохранённый логин браузера (тот же приём — SecretInput). */}
          <input
            type="text"
            autoComplete="off"
            value={form.user ?? ""}
            onChange={(e) => setForm({ ...form, user: e.target.value })}
            placeholder="default"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1 md:col-span-2">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.password")}
          </label>
          <input
            type="password"
            autoComplete="new-password"
            value={form.password ?? ""}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
          />
          {passwordIsMasked && (
            <p className="text-xs text-fg-muted">
              {t("settings.common.masked_hint")}
            </p>
          )}
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.batch_size")}
          </label>
          <input
            type="number"
            value={num(form.batch_size)}
            onChange={(e) => setNum("batch_size", e.target.value)}
            placeholder="1000"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.flush_interval_sec")}
          </label>
          <input
            type="number"
            value={num(form.flush_interval_sec)}
            onChange={(e) => setNum("flush_interval_sec", e.target.value)}
            placeholder="5"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.buffer_max_size")}
          </label>
          <input
            type="number"
            value={num(form.buffer_max_size)}
            onChange={(e) => setNum("buffer_max_size", e.target.value)}
            placeholder="50000"
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.clickhouse.workers")}
          </label>
          <input
            type="number"
            value={num(form.workers)}
            onChange={(e) => setNum("workers", e.target.value)}
            placeholder="2"
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
          disabled={test.isPending}
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
        {t("settings.clickhouse.partial_hot_reload_note")}
      </p>

      <CHTemplatesPanel />

      <OrphanTablesPanel />
    </div>
  );
}
