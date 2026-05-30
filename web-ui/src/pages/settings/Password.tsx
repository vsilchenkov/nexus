import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Info } from "lucide-react";

import { api } from "../../api/client";
import { Button, Card, Field, Input, Hint } from "../../components/ui";

// PasswordPanel — self-service смена собственного пароля (§26). Доступна
// любой роли. Бэкенд требует подтверждения текущего пароля и инвалидирует
// все сессии пользователя, поэтому после успеха уводим на /login.
export function PasswordPanel() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);

  const mismatch = confirm.length > 0 && next !== confirm;
  const canSubmit =
    current.length > 0 && next.length >= 8 && next === confirm;

  const save = useMutation({
    mutationFn: () =>
      api.post("/api/me/password", {
        current_password: current,
        new_password: next,
      }),
    onSuccess: () => {
      // Все сессии завершены — повторный вход обязателен.
      window.location.href = "/login";
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  return (
    <Card className="max-w-xl">
      <div className="text-sm font-semibold">{t("settings.password.title")}</div>
      <div className="mb-3.5 mt-1 text-xs text-fg-muted">
        {t("settings.password.subtitle")}
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
        {mismatch && (
          <p className="text-xs text-err">{t("settings.password.mismatch")}</p>
        )}
        <div className="flex items-center gap-3 pt-1">
          <Button variant="primary" type="submit" disabled={!canSubmit || save.isPending}>
            {save.isPending ? t("settings.password.saving") : t("settings.password.submit")}
          </Button>
        </div>
      </form>

      <Hint tone="muted" icon={<Info className="h-3.5 w-3.5" />} className="mt-3">
        {t("settings.password.note")}
      </Hint>
    </Card>
  );
}
