import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";

type GeneralSettings = {
  public_base_url?: string;
  version_override?: string;
  metrics_refetch_ms?: number;
  metrics_approx_counts?: boolean;
  // §94.5: срок хранения журнала отказов, дней. 0 = сбор выключен и
  // накопленное удаляется — отдельного тумблера нет.
  rejected_retention_days?: number;
};
type SecuritySettings = { session_ttl_seconds?: number };
type AppSettings = {
  general?: GeneralSettings;
  security?: SecuritySettings;
  updated_at?: string;
};
type VersionInfo = { version: string; override_allowed?: boolean };

// Длительность сессии задаётся в минутах в UI, хранится в секундах (§34.2).
const SESSION_MIN_MINUTES = 5; // 300 c
const SESSION_MAX_MINUTES = 30 * 24 * 60; // 30 суток

// §94.5: границы срока хранения журнала отказов. Зеркало домена
// (RejectedRetention*Days): расхождение дало бы поле, которое сервер отвергает.
const REJECTED_RETENTION_MAX_DAYS = 365;

// GeneralPanel — общие настройки приложения (§28, Пункт 1): публичный адрес,
// под которым опубликован Web. Если задан, UI собирает полный адрес узла от
// него вместо адреса хоста в браузере.
export function GeneralPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });

  // §34.3: поле ручного override версии видно только если бэкенд разрешает
  // (web.allow_version_override — dev/staging). В проде гейт выключен.
  const version = useQuery({
    queryKey: ["version"],
    queryFn: () => api.get<VersionInfo>("/api/version"),
    staleTime: Infinity,
  });
  const overrideAllowed = version.data?.override_allowed ?? false;

  const [url, setUrl] = useState("");
  const [versionOverride, setVersionOverride] = useState("");
  const [sessionMinutes, setSessionMinutes] = useState("");
  // §44.C: интервал автообновления метрик — в UI задаётся в секундах, хранится в мс.
  const [refetchSec, setRefetchSec] = useState("");
  // §44-perf: режим подсчёта уникальных (точно/приблизительно).
  const [approxCounts, setApproxCounts] = useState(false);
  // §94.5: срок хранения журнала отказов (пусто = не трогаем текущее значение).
  const [rejectedDays, setRejectedDays] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (data) {
      setUrl(data.general?.public_base_url ?? "");
      setVersionOverride(data.general?.version_override ?? "");
      const secs = data.security?.session_ttl_seconds;
      setSessionMinutes(secs ? String(Math.round(secs / 60)) : "");
      const ms = data.general?.metrics_refetch_ms;
      setRefetchSec(ms ? String(Math.round(ms / 1000)) : "");
      setApproxCounts(data.general?.metrics_approx_counts ?? false);
      const rr = data.general?.rejected_retention_days;
      // Ноль — осмысленное значение («журнал выключен»), поэтому проверка
      // именно на undefined: `rr ? …` показал бы пустое поле вместо нуля.
      setRejectedDays(rr === undefined ? "" : String(rr));
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () => {
      const general: GeneralSettings = { public_base_url: url.trim() };
      if (overrideAllowed) general.version_override = versionOverride.trim();
      // §44.C: интервал в секундах → мс; пусто — не трогаем (дефолт/текущее).
      const sec = refetchSec.trim();
      if (sec !== "") general.metrics_refetch_ms = Math.round(Number(sec) * 1000);
      // §44-perf: режим подсчёта уникальных (всегда шлём текущее значение тоггла).
      general.metrics_approx_counts = approxCounts;
      // §94.5: срок хранения отказов. Пусто — не трогаем; ноль отправляем как
      // есть (это «выключить журнал», а не «нет значения»).
      const rr = rejectedDays.trim();
      if (rr !== "") general.rejected_retention_days = Math.round(Number(rr));
      const body: { general: GeneralSettings; security?: SecuritySettings } = { general };
      // Длительность сессии: пусто — не трогаем (остаётся из env/текущего).
      const mins = sessionMinutes.trim();
      if (mins !== "") {
        body.security = { session_ttl_seconds: Math.round(Number(mins) * 60) };
      }
      return api.put("/api/settings/app", body);
    },
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["app-settings"] });
      qc.invalidateQueries({ queryKey: ["public-settings"] });
      qc.invalidateQueries({ queryKey: ["version"] });
    },
    onError: (e: { response?: { data?: { error?: string } } }) =>
      setError(e?.response?.data?.error ?? t("common.error")),
  });

  if (isLoading) {
    return <div className="text-fg-muted">{t("common.loading")}</div>;
  }

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("settings.general.title")}</h2>
        {data?.updated_at && (
          <span className="text-xs text-fg-muted">
            {t("settings.common.updated_at")}: {new Date(data.updated_at).toLocaleString()}
          </span>
        )}
      </header>

      <p className="text-sm text-fg-muted">{t("settings.general.public_base_url_hint")}</p>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.public_base_url")}
        </label>
        <input
          type="text"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          autoComplete="off"
          placeholder="https://nexus.example.com"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.public_base_url_example")}</p>
      </div>

      {overrideAllowed && (
        <div className="max-w-3xl space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.general.version_override")}
          </label>
          <input
            type="text"
            value={versionOverride}
            onChange={(e) => setVersionOverride(e.target.value)}
            autoComplete="off"
            placeholder={version.data?.version ?? "dev-local"}
            className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
          />
          <p className="text-xs text-fg-subtle">{t("settings.general.version_override_hint")}</p>
        </div>
      )}

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.session_ttl")}
        </label>
        <input
          type="number"
          min={SESSION_MIN_MINUTES}
          max={SESSION_MAX_MINUTES}
          value={sessionMinutes}
          onChange={(e) => setSessionMinutes(e.target.value)}
          placeholder="1440"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.session_ttl_hint")}</p>
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.metrics_refetch")}
        </label>
        <input
          type="number"
          min={1}
          max={120}
          value={refetchSec}
          onChange={(e) => setRefetchSec(e.target.value)}
          placeholder="12"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.metrics_refetch_hint")}</p>
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.rejected_retention")}
        </label>
        <input
          type="number"
          min={0}
          max={REJECTED_RETENTION_MAX_DAYS}
          value={rejectedDays}
          onChange={(e) => setRejectedDays(e.target.value)}
          placeholder="30"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.rejected_retention_hint")}</p>
        {rejectedDays.trim() === "0" && (
          <p className="text-xs text-warn">{t("settings.general.rejected_retention_zero_warn")}</p>
        )}
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="flex cursor-pointer items-center gap-2">
          <input
            type="checkbox"
            checked={approxCounts}
            onChange={(e) => setApproxCounts(e.target.checked)}
            className="h-4 w-4 accent-accent"
          />
          <span className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.general.metrics_approx")}
          </span>
        </label>
        <p className="text-xs text-fg-subtle">{t("settings.general.metrics_approx_hint")}</p>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <button
          onClick={() => save.mutate()}
          disabled={save.isPending}
          className="rounded-md bg-accent px-4 py-2 text-sm hover:bg-accent-hover disabled:opacity-50"
        >
          {t("common.save")}
        </button>
        {save.isSuccess && <span className="text-sm text-ok">{t("settings.common.saved")}</span>}
        {error && <span className="text-sm text-err">{error}</span>}
      </div>
    </div>
  );
}
