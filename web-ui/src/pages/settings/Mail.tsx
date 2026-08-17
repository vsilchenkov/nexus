import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { SecretInput } from "../../components/ui";

// MailSettings — секция mail из app_settings (§88.2). Поля необязательные:
// сервер отдаёт только заданные, остальные разворачиваются в дефолты у него же.
type MailSettings = {
  enabled?: boolean;
  host?: string;
  port?: number;
  encryption?: string;
  auth_type?: string;
  username?: string;
  password?: string;
  from_address?: string;
  from_name?: string;
  helo_host?: string;
  timeout_sec?: number;
  skip_tls_verify?: boolean;
  password_reset_enabled?: boolean;
  password_reset_ttl_min?: number;
};

type AppSettings = {
  mail?: MailSettings;
  general?: { public_base_url?: string };
  updated_at?: string;
};

type TestResult = { ok: boolean; latency_ms?: number; error?: string };

const inp =
  "w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent";

// numOrUndef — пустое поле означает «не задано», а не 0: иначе очищенный
// таймаут уехал бы на сервер нулём и не прошёл валидацию.
function numOrUndef(v: string): number | undefined {
  const s = v.trim();
  return s === "" ? undefined : Number(s);
}

export function MailPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });
  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<{ user: { email?: string } }>("/api/auth/me"),
  });

  const [form, setForm] = useState<MailSettings>({});
  // Числа держим строками: пустое поле должно оставаться пустым, а не
  // превращаться в 0 при каждом нажатии.
  const [port, setPort] = useState("");
  const [timeout, setTimeoutSec] = useState("");
  const [ttl, setTtl] = useState("");
  const [to, setTo] = useState("");

  useEffect(() => {
    const m = data?.mail;
    if (!m) return;
    setForm({ ...m });
    setPort(m.port != null ? String(m.port) : "");
    setTimeoutSec(m.timeout_sec != null ? String(m.timeout_sec) : "");
    setTtl(m.password_reset_ttl_min != null ? String(m.password_reset_ttl_min) : "");
  }, [data]);

  useEffect(() => {
    const email = me.data?.user.email;
    if (email) setTo((prev) => (prev === "" ? email : prev));
  }, [me.data]);

  // payload собирается в одном месте: «Сохранить» и «Проверить» обязаны
  // отправлять одну и ту же секцию, иначе тест проверяет не то, что сохранится.
  const section = (): MailSettings => ({
    ...form,
    port: numOrUndef(port),
    timeout_sec: numOrUndef(timeout),
    password_reset_ttl_min: numOrUndef(ttl),
  });

  const save = useMutation({
    mutationFn: () => api.put("/api/settings/app", { mail: section() }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["app-settings"] });
      // §88.4.5: доступность восстановления отдаётся в /api/version и меняется
      // этим же сохранением — форма входа обязана узнать об этом.
      qc.invalidateQueries({ queryKey: ["version"] });
    },
  });

  const check = useMutation({
    mutationFn: () => api.post<TestResult>("/api/settings/mail/test", { ...section(), to: to.trim() }),
  });

  const publicBaseURL = data?.general?.public_base_url ?? "";
  const saveErr = (save.error as { response?: { data?: { error?: string } } } | null)?.response?.data
    ?.error;

  if (isLoading) return <div className="text-fg-muted">{t("common.loading")}</div>;

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("settings.mail.title")}</h2>
        {data?.updated_at && (
          <span className="text-xs text-fg-muted">
            {t("settings.common.updated_at")}: {new Date(data.updated_at).toLocaleString()}
          </span>
        )}
      </header>
      <p className="text-sm text-fg-muted">{t("settings.mail.hint")}</p>

      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          className="h-4 w-4 accent-accent"
          checked={form.enabled ?? false}
          onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
        />
        <span className="text-sm">{t("settings.mail.enabled")}</span>
      </label>

      {/* ── Сервер ─────────────────────────────────────────────── */}
      <section className="max-w-3xl space-y-4">
        <h3 className="text-sm font-medium text-fg-muted">{t("settings.mail.server")}</h3>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.host")}</label>
            <input
              className={inp}
              value={form.host ?? ""}
              onChange={(e) => setForm({ ...form, host: e.target.value })}
              autoComplete="off"
              placeholder="smtp.example.com"
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.port")}</label>
            <input
              className={inp}
              type="number"
              value={port}
              onChange={(e) => setPort(e.target.value)}
              placeholder="587"
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.encryption")}</label>
            <select
              className={inp}
              value={form.encryption ?? "starttls"}
              onChange={(e) => setForm({ ...form, encryption: e.target.value })}
            >
              <option value="none">{t("settings.mail.encryption_none")}</option>
              <option value="starttls">{t("settings.mail.encryption_starttls")}</option>
              <option value="tls">{t("settings.mail.encryption_tls")}</option>
            </select>
          </div>
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.timeout")}</label>
            <input
              className={inp}
              type="number"
              value={timeout}
              onChange={(e) => setTimeoutSec(e.target.value)}
              placeholder="10"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent"
                checked={form.skip_tls_verify ?? false}
                onChange={(e) => setForm({ ...form, skip_tls_verify: e.target.checked })}
              />
              <span className="text-sm">{t("settings.mail.skip_tls_verify")}</span>
            </label>
            {form.skip_tls_verify && (
              <p className="text-xs text-warn">{t("settings.mail.skip_tls_verify_hint")}</p>
            )}
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-sm text-fg-muted">{t("settings.mail.helo")}</label>
            <input
              className={inp}
              value={form.helo_host ?? ""}
              onChange={(e) => setForm({ ...form, helo_host: e.target.value })}
              autoComplete="off"
              placeholder="nexus.example.com"
            />
            <p className="text-xs text-fg-subtle">{t("settings.mail.helo_hint")}</p>
          </div>
        </div>
      </section>

      {/* ── Аутентификация ─────────────────────────────────────── */}
      <section className="max-w-3xl space-y-4">
        <h3 className="text-sm font-medium text-fg-muted">{t("settings.mail.auth")}</h3>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.auth_type")}</label>
            <select
              className={inp}
              value={form.auth_type ?? "plain"}
              onChange={(e) => setForm({ ...form, auth_type: e.target.value })}
            >
              <option value="none">{t("settings.mail.auth_none")}</option>
              <option value="plain">{t("settings.mail.auth_plain")}</option>
              <option value="login">{t("settings.mail.auth_login")}</option>
              <option value="cram-md5">{t("settings.mail.auth_cram_md5")}</option>
            </select>
          </div>
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.username")}</label>
            {/* autoComplete="off": текстовое поле перед password-полем — Chrome
                иначе вписывает сюда сохранённый логин браузера. */}
            <input
              className={inp}
              autoComplete="off"
              value={form.username ?? ""}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
              placeholder="noreply@example.com"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-sm text-fg-muted">{t("settings.mail.password")}</label>
            {/* SecretInput, а не голый type="password": SMTP-пароль длинный, и
                вводить его вслепую — гарантированная опечатка. */}
            <SecretInput
              value={form.password ?? ""}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
              placeholder="***"
            />
            <p className="text-xs text-fg-subtle">{t("settings.common.masked_hint")}</p>
          </div>
        </div>
      </section>

      {/* ── Отправитель ────────────────────────────────────────── */}
      <section className="max-w-3xl space-y-4">
        <h3 className="text-sm font-medium text-fg-muted">{t("settings.mail.sender")}</h3>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.from_address")}</label>
            <input
              className={inp}
              value={form.from_address ?? ""}
              onChange={(e) => setForm({ ...form, from_address: e.target.value })}
              autoComplete="off"
              placeholder="nexus@example.com"
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.from_name")}</label>
            <input
              className={inp}
              value={form.from_name ?? ""}
              onChange={(e) => setForm({ ...form, from_name: e.target.value })}
              autoComplete="off"
              placeholder="Nexus"
            />
          </div>
        </div>
      </section>

      {/* ── Восстановление пароля ──────────────────────────────── */}
      <section className="max-w-3xl space-y-4">
        <h3 className="text-sm font-medium text-fg-muted">{t("settings.mail.reset")}</h3>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            className="h-4 w-4 accent-accent"
            checked={form.password_reset_enabled ?? false}
            onChange={(e) => setForm({ ...form, password_reset_enabled: e.target.checked })}
          />
          <span className="text-sm">{t("settings.mail.reset_enabled")}</span>
        </label>
        <p className="text-xs text-fg-subtle">{t("settings.mail.reset_enabled_hint")}</p>

        <div className="max-w-xs space-y-1">
          <label className="text-sm text-fg-muted">{t("settings.mail.reset_ttl")}</label>
          <input
            className={inp}
            type="number"
            value={ttl}
            onChange={(e) => setTtl(e.target.value)}
            placeholder="60"
          />
          <p className="text-xs text-fg-subtle">{t("settings.mail.reset_ttl_hint")}</p>
        </div>

        {publicBaseURL ? (
          <p className="text-xs text-fg-subtle">
            {t("settings.mail.base_url_ok", { url: `${publicBaseURL}/reset-password` })}
          </p>
        ) : (
          <p className="text-xs text-warn">{t("settings.mail.base_url_missing")}</p>
        )}
      </section>

      {/* ── Тестовое письмо ────────────────────────────────────── */}
      <section className="max-w-3xl space-y-3">
        <h3 className="text-sm font-medium text-fg-muted">{t("settings.mail.test")}</h3>
        <div className="flex flex-wrap items-end gap-3">
          <div className="w-72 space-y-1">
            <label className="text-sm text-fg-muted">{t("settings.mail.test_to")}</label>
            <input
              className={inp}
              type="email"
              value={to}
              onChange={(e) => setTo(e.target.value)}
              autoComplete="off"
              placeholder="admin@example.com"
            />
          </div>
          <button
            type="button"
            onClick={() => check.mutate()}
            disabled={check.isPending || to.trim() === ""}
            className="rounded-md bg-bg-muted px-4 py-2 text-sm hover:bg-bg-elev disabled:opacity-50"
          >
            {check.isPending ? t("settings.mail.test_sending") : t("settings.mail.test_send")}
          </button>
        </div>
        <p className="text-xs text-fg-subtle">{t("settings.mail.test_to_hint")}</p>
        {check.isSuccess && check.data?.ok && (
          <span className="text-sm text-ok">
            {t("settings.common.test_ok", { ms: check.data.latency_ms ?? 0 })}
          </span>
        )}
        {check.isSuccess && !check.data?.ok && (
          <span className="text-sm text-err">
            {t("settings.common.test_failed", { error: check.data?.error })}
          </span>
        )}
      </section>

      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={() => save.mutate()}
          disabled={save.isPending}
          className="rounded-md bg-accent px-4 py-2 text-sm font-medium hover:bg-accent-hover disabled:opacity-50"
        >
          {t("common.save")}
        </button>
        {save.isSuccess && <span className="text-sm text-ok">{t("settings.common.saved")}</span>}
        {save.isError && <span className="text-sm text-err">{saveErr ?? t("common.error")}</span>}
      </div>

      <p className="text-xs text-fg-subtle">{t("settings.common.hot_reload_note")}</p>
    </div>
  );
}
