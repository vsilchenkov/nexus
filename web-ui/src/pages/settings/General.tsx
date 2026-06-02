import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";

type GeneralSettings = { public_base_url?: string };
type AppSettings = {
  general?: GeneralSettings;
  updated_at?: string;
};

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

  const [url, setUrl] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (data) setUrl(data.general?.public_base_url ?? "");
  }, [data]);

  const save = useMutation({
    mutationFn: () =>
      api.put("/api/settings/app", { general: { public_base_url: url.trim() } }),
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["app-settings"] });
      qc.invalidateQueries({ queryKey: ["public-settings"] });
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
          placeholder="https://nexus.example.com"
          className="w-full rounded-md bg-bg-muted px-3 py-2 font-mono text-xs outline-none"
        />
        <p className="text-xs text-fg-subtle">{t("settings.general.public_base_url_example")}</p>
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
