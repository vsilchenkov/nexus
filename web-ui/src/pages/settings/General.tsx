import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { useBodyLimitCaps } from "../../lib/nodeDefaults";

type GeneralSettings = {
  public_base_url?: string;
  version_override?: string;
  metrics_refetch_ms?: number;
  metrics_approx_counts?: boolean;
  // §94.5: срок хранения журнала отказов, дней. 0 = сбор выключен и
  // накопленное удаляется — отдельного тумблера нет.
  rejected_retention_days?: number;
  // §97: рабочие лимиты размера тела, БАЙТЫ (в форме вводятся в МиБ).
  max_body_bytes?: number;
  max_async_body_bytes?: number;
  // §98.5: глобальная политика защиты узла. Отсутствие/null = «не задано»,
  // действует конфигурация сервиса; на узле значение можно переопределить.
  circuit_breaker_threshold?: number | null;
  circuit_breaker_cooldown_sec?: number | null;
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

// §98.5: границы глобальной политики защиты узла. Зеркало домена
// (BreakerThreshold*/BreakerCooldown*) и полей узла: расхождение дало бы
// значение, которое форма пропускает, а сервер отвергает.
const BREAKER_THRESHOLD_MIN = 1;
const BREAKER_THRESHOLD_MAX = 100;
const BREAKER_COOLDOWN_MIN_SEC = 1;
const BREAKER_COOLDOWN_MAX_SEC = 3600;

// §94.5: границы срока хранения журнала отказов. Зеркало домена
// (RejectedRetention*Days): расхождение дало бы поле, которое сервер отвергает.
const REJECTED_RETENTION_MAX_DAYS = 365;

// §97: лимиты тела задаются в МиБ в UI, хранятся в байтах — та же схема, что у
// длительности сессии (минуты) и интервала метрик (секунды).
const MIB = 1024 * 1024;
const bytesToMib = (v: number) => String(Math.round(v / MIB));
const mibToBytes = (v: string) => Math.round(Number(v) * MIB);

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
  // §97: рабочие лимиты размера тела, в МиБ.
  const [bodyMib, setBodyMib] = useState("");
  const [asyncBodyMib, setAsyncBodyMib] = useState("");
  // §98.5: политика защиты узла. Пусто = не трогаем (действует конфигурация).
  const [breakerThreshold, setBreakerThreshold] = useState("");
  const [breakerCooldown, setBreakerCooldown] = useState("");
  const [error, setError] = useState<string | null>(null);

  // §97: потолки из конфига — выше них сервер значение не примет, поэтому
  // форма показывает их в подсказке и блокирует сохранение заранее.
  const caps = useBodyLimitCaps();
  const syncCapMib = caps.syncMaxBytes > 0 ? Math.floor(caps.syncMaxBytes / MIB) : 0;
  const asyncCapMib = caps.asyncMaxBytes > 0 ? Math.floor(caps.asyncMaxBytes / MIB) : 0;

  const bodyOverCap = syncCapMib > 0 && bodyMib.trim() !== "" && Number(bodyMib) > syncCapMib;
  const asyncOverCap =
    asyncCapMib > 0 && asyncBodyMib.trim() !== "" && Number(asyncBodyMib) > asyncCapMib;
  // async не может быть больше sync: запрос к узлу на паузе (§3.6) уходит в
  // очередь и режется async-лимитом.
  const asyncOverSync =
    bodyMib.trim() !== "" &&
    asyncBodyMib.trim() !== "" &&
    Number(asyncBodyMib) > Number(bodyMib);
  const limitsInvalid = bodyOverCap || asyncOverCap || asyncOverSync;

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
      // §97: байты → МиБ.
      const mb = data.general?.max_body_bytes;
      setBodyMib(mb === undefined ? "" : bytesToMib(mb));
      const amb = data.general?.max_async_body_bytes;
      setAsyncBodyMib(amb === undefined ? "" : bytesToMib(amb));
      // §98.5: null (не задано) и число различаются — пустое поле означает
      // «действует конфигурация сервиса», а не ноль.
      const bt = data.general?.circuit_breaker_threshold;
      setBreakerThreshold(bt === undefined || bt === null ? "" : String(bt));
      const bc = data.general?.circuit_breaker_cooldown_sec;
      setBreakerCooldown(bc === undefined || bc === null ? "" : String(bc));
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
      // §97: МиБ → байты; пусто — не трогаем текущее значение.
      const bm = bodyMib.trim();
      if (bm !== "") general.max_body_bytes = mibToBytes(bm);
      const abm = asyncBodyMib.trim();
      if (abm !== "") general.max_async_body_bytes = mibToBytes(abm);
      // §98.5: пусто — поле не отправляется вовсе, и сохранённое значение
      // остаётся прежним (общий контракт панели).
      const bt = breakerThreshold.trim();
      if (bt !== "") general.circuit_breaker_threshold = Math.round(Number(bt));
      const bc = breakerCooldown.trim();
      if (bc !== "") general.circuit_breaker_cooldown_sec = Math.round(Number(bc));
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
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.max_body_sync")}
        </label>
        <input
          type="number"
          min={1}
          max={syncCapMib > 0 ? syncCapMib : undefined}
          value={bodyMib}
          onChange={(e) => setBodyMib(e.target.value)}
          placeholder="100"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">
          {t("settings.general.max_body_sync_hint", { max: syncCapMib })}
        </p>
        {bodyOverCap && (
          <p className="text-xs text-err">
            {t("settings.general.max_body_over_cap", { max: syncCapMib })}
          </p>
        )}
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.max_body_async")}
        </label>
        <input
          type="number"
          min={1}
          max={asyncCapMib > 0 ? asyncCapMib : undefined}
          value={asyncBodyMib}
          onChange={(e) => setAsyncBodyMib(e.target.value)}
          placeholder="100"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">
          {t("settings.general.max_body_async_hint", { max: asyncCapMib })}
        </p>
        {asyncOverCap && (
          <p className="text-xs text-err">
            {t("settings.general.max_body_over_cap", { max: asyncCapMib })}
          </p>
        )}
        {asyncOverSync && (
          <p className="text-xs text-err">{t("settings.general.max_body_async_over_sync")}</p>
        )}
        <p className="text-xs text-fg-subtle">{t("settings.common.hot_reload_note")}</p>
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.breaker_threshold")}
        </label>
        <input
          type="number"
          min={BREAKER_THRESHOLD_MIN}
          max={BREAKER_THRESHOLD_MAX}
          value={breakerThreshold}
          onChange={(e) => setBreakerThreshold(e.target.value)}
          placeholder="5"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.breaker_threshold_hint")}</p>
      </div>

      <div className="max-w-3xl space-y-1">
        <label className="text-xs uppercase tracking-wider text-fg-muted">
          {t("settings.general.breaker_cooldown")}
        </label>
        <input
          type="number"
          min={BREAKER_COOLDOWN_MIN_SEC}
          max={BREAKER_COOLDOWN_MAX_SEC}
          value={breakerCooldown}
          onChange={(e) => setBreakerCooldown(e.target.value)}
          placeholder="30"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.breaker_cooldown_hint")}</p>
        <p className="text-xs text-fg-subtle">{t("settings.common.hot_reload_note")}</p>
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
          disabled={save.isPending || limitsInvalid}
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
