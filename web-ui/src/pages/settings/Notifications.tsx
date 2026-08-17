import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";

type TelegramSettings = {
  enabled?: boolean;
  chat_id?: string;
  bot_token?: string;
  cron?: string;
};

type AppSettings = {
  notifications?: { telegram?: TelegramSettings };
  updated_at?: string;
};

export function NotificationsPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const { data } = useQuery({
    queryKey: ["app-settings"],
    queryFn: () => api.get<AppSettings>("/api/settings/app"),
  });

  const [form, setForm] = useState<TelegramSettings>({});
  useEffect(() => {
    if (data?.notifications?.telegram) {
      setForm({ ...data.notifications.telegram });
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () => api.put("/api/settings/app", { notifications: { telegram: form } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["app-settings"] }),
  });

  const check = useMutation({
    mutationFn: () =>
      api.post<{ ok: boolean; error?: string }>("/api/settings/notifications/test", form),
  });

  const inp =
    "w-full px-3 py-2 bg-bg-muted rounded-md outline-none border border-bg-muted focus:border-accent";

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("settings.notifications.title")}</h2>
        {data?.updated_at && (
          <span className="text-xs text-fg-muted">
            {t("settings.common.updated_at")}: {new Date(data.updated_at).toLocaleString()}
          </span>
        )}
      </header>
      <p className="text-sm text-fg-muted">{t("settings.notifications.hint")}</p>

      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={form.enabled ?? false}
          onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
        />
        <span className="text-sm">{t("settings.notifications.enabled")}</span>
      </label>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 max-w-3xl">
        <div className="space-y-1">
          <label className="text-sm text-fg-muted">{t("settings.notifications.chat_id")}</label>
          {/* autoComplete="off": текстовое поле перед password-полем — Chrome
              иначе вписывает сюда сохранённый логин браузера. */}
          <input
            className={inp}
            autoComplete="off"
            value={form.chat_id ?? ""}
            onChange={(e) => setForm({ ...form, chat_id: e.target.value })}
            placeholder="-1001234567890"
          />
        </div>
        <div className="space-y-1">
          <label className="text-sm text-fg-muted">{t("settings.notifications.bot_token")}</label>
          <input
            type="password"
            autoComplete="new-password"
            className={inp}
            value={form.bot_token ?? ""}
            onChange={(e) => setForm({ ...form, bot_token: e.target.value })}
            placeholder="***"
          />
        </div>
        <div className="space-y-1 md:col-span-2">
          <label className="text-sm text-fg-muted">{t("settings.notifications.cron")}</label>
          <input
            className={`${inp} font-mono`}
            value={form.cron ?? ""}
            onChange={(e) => setForm({ ...form, cron: e.target.value })}
            autoComplete="off"
            placeholder="*/15 * * * *"
          />
          <p className="text-xs text-fg-muted">{t("settings.notifications.cron_hint")}</p>
        </div>
      </div>

      <div className="flex items-center gap-3 flex-wrap">
        <button
          onClick={() => save.mutate()}
          disabled={save.isPending}
          className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm font-medium disabled:opacity-50"
        >
          {t("common.save")}
        </button>
        <button
          onClick={() => check.mutate()}
          disabled={check.isPending}
          className="bg-bg-muted hover:bg-bg-elev px-4 py-2 rounded-md text-sm"
        >
          {check.isPending ? t("settings.notifications.checking") : t("settings.notifications.check")}
        </button>
        {save.isSuccess && <span className="text-ok text-sm">{t("settings.common.saved")}</span>}
        {save.isError && (
          <span className="text-err text-sm">{(save.error as any)?.response?.data?.error ?? "error"}</span>
        )}
        {check.isSuccess && check.data?.ok && (
          <span className="text-ok text-sm">{t("settings.notifications.check_ok")}</span>
        )}
        {check.isSuccess && !check.data?.ok && (
          <span className="text-err text-sm">
            {t("settings.notifications.check_failed", { error: check.data?.error })}
          </span>
        )}
      </div>
    </div>
  );
}
