import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ShieldAlert } from "lucide-react";

import { api } from "../api/client";
import { Button, Field, Input } from "../components/ui";

// ForcePasswordChange — обязательный экран смены пароля (§7.1, П18). Показывается
// поверх всего приложения, пока у пользователя стоит must_change_password=true
// (бэкенд блокирует все эндпоинты кроме /api/me/password). После успешной смены
// все сессии завершаются → уходим на /login.
export default function ForcePasswordChange() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);

  const mismatch = confirm.length > 0 && next !== confirm;
  const canSubmit = current.length > 0 && next.length >= 8 && next === confirm;

  const save = useMutation({
    mutationFn: () =>
      api.post("/api/me/password", { current_password: current, new_password: next }),
    onSuccess: () => {
      window.location.href = "/login";
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <div className="grid min-h-screen place-items-center bg-app p-4">
      <div className="w-full max-w-md rounded-lg border border-line bg-bg p-6 shadow-lg">
        <div className="mb-4 flex items-center gap-2.5">
          <span className="grid h-9 w-9 place-items-center rounded-md bg-warn/15 text-warn">
            <ShieldAlert className="h-5 w-5" />
          </span>
          <div>
            <div className="text-base font-semibold">{t("force_password.title")}</div>
            <div className="text-xs text-fg-muted">{t("force_password.subtitle")}</div>
          </div>
        </div>

        {error && (
          <div className="mb-3 rounded border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">
            {error}
          </div>
        )}

        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (canSubmit) save.mutate();
          }}
        >
          <Field label={t("settings.password.current")}>
            <Input
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </Field>
          <Field label={t("settings.password.new")} hint={t("settings.password.new_hint")}>
            <Input
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          </Field>
          <Field label={t("settings.password.confirm")}>
            <Input
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </Field>
          {mismatch && <p className="text-xs text-err">{t("settings.password.mismatch")}</p>}
          <Button variant="primary" type="submit" disabled={!canSubmit || save.isPending} className="w-full">
            {save.isPending ? t("settings.password.saving") : t("force_password.submit")}
          </Button>
        </form>
      </div>
    </div>
  );
}
