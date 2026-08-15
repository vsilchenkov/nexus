import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { CheckCircle2, KeyRound, ShieldAlert } from "lucide-react";

import { api } from "../api/client";
import { Button, Card, ErrorAlert, Field, SecretInput } from "../components/ui";
import { MIN_PASSWORD_LENGTH, passwordStrength } from "../lib/password";

// ResetPassword — публичная страница задания нового пароля по ссылке из письма
// (§88.8.3). Маршрут объявлен ЯВНО рядом с /login: catch-all в App.tsx иначе
// увёл бы прямой переход из письма на «/».
//
// Четыре состояния: проверка ссылки → недействительна | форма → успех.
export default function ResetPassword() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";

  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  // Проверка НЕ расходует ссылку (§88.4.6): почтовые шлюзы с защитой от
  // вредоносных ссылок сами открывают адреса из писем.
  const check = useQuery({
    queryKey: ["password-reset-validate", token],
    queryFn: () =>
      api.get<{ valid: boolean }>(
        `/api/auth/password-reset/validate?token=${encodeURIComponent(token)}`,
      ),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
  });

  const save = useMutation({
    mutationFn: () =>
      api.post("/api/auth/password-reset/confirm", { token, new_password: next }),
    onSuccess: () => {
      setError(null);
      setDone(true);
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const mismatch = confirm.length > 0 && next !== confirm;
  const canSubmit = next.length >= MIN_PASSWORD_LENGTH && next === confirm && !save.isPending;
  const strength = passwordStrength(next);

  // Ссылки нет вовсе или сервер сказал «недействительна» — один экран на все
  // причины (нет такой / истекла / уже использована), различать их нельзя.
  const invalid = token === "" || check.isError || check.data?.valid === false;

  return (
    <div className="grid min-h-screen place-items-center bg-app p-4 text-fg">
      <div className="w-[360px]">
        <div className="mb-6 text-center">
          <div
            className={
              "mx-auto mb-3.5 grid h-12 w-12 place-items-center rounded-lg text-white " +
              (invalid ? "bg-err" : "bg-gradient-to-br from-accent to-ok")
            }
          >
            {invalid ? <ShieldAlert className="h-6 w-6" /> : <KeyRound className="h-6 w-6" />}
          </div>
          <div className="text-[19px] font-semibold">{t("app.title")}</div>
          <div className="mt-1 text-[13px] text-fg-muted">
            {invalid ? t("auth.reset_invalid_title") : t("auth.reset_title")}
          </div>
        </div>

        <Card className="p-6">
          {token !== "" && check.isLoading ? (
            <div className="text-center text-[13px] text-fg-muted">{t("auth.reset_checking")}</div>
          ) : invalid ? (
            <div className="space-y-4">
              <p className="text-[13px] leading-relaxed text-fg-muted">
                {t("auth.reset_invalid_hint")}
              </p>
              <Button variant="primary" className="w-full" onClick={goToLogin}>
                {t("auth.reset_to_login")}
              </Button>
            </div>
          ) : done ? (
            <div className="space-y-4">
              <div className="flex gap-3">
                <CheckCircle2 className="mt-0.5 h-5 w-5 shrink-0 text-ok" />
                <p className="text-[13px] leading-relaxed">{t("auth.reset_done")}</p>
              </div>
              <Button variant="primary" className="w-full" onClick={goToLogin}>
                {t("auth.reset_to_login")}
              </Button>
            </div>
          ) : (
            <form
              className="space-y-3.5"
              onSubmit={(e) => {
                e.preventDefault();
                if (canSubmit) save.mutate();
              }}
            >
              <Field label={t("auth.reset_new")} hint={t("auth.reset_min", { n: MIN_PASSWORD_LENGTH })}>
                <SecretInput
                  value={next}
                  onChange={(e) => setNext(e.target.value)}
                  autoComplete="new-password"
                  autoFocus
                  required
                />
              </Field>

              {next !== "" && (
                <div className="flex items-center gap-2">
                  <div className="flex h-1 flex-1 gap-1">
                    {[0, 1, 2, 3].map((i) => (
                      <span
                        key={i}
                        className={
                          "h-1 flex-1 rounded-full " +
                          (i < strength.score
                            ? strength.score <= 1
                              ? "bg-err"
                              : strength.score === 2
                                ? "bg-warn"
                                : "bg-ok"
                            : "bg-bg-muted")
                        }
                      />
                    ))}
                  </div>
                  <span className="text-[11px] text-fg-muted">{t(strength.key)}</span>
                </div>
              )}

              <Field label={t("auth.reset_confirm")}>
                <SecretInput
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  autoComplete="new-password"
                  required
                />
              </Field>

              {mismatch && <div className="text-xs text-err">{t("auth.reset_mismatch")}</div>}
              {error && <ErrorAlert>{error}</ErrorAlert>}

              <Button type="submit" variant="primary" disabled={!canSubmit} className="w-full">
                {save.isPending ? "…" : t("auth.reset_submit")}
              </Button>
            </form>
          )}
        </Card>
      </div>
    </div>
  );
}

// goToLogin — полная перезагрузка, а не navigate: после смены пароля все
// сессии завершены, и клиентский кеш react-query обязан уйти вместе с ними.
function goToLogin() {
  window.location.href = "/login";
}
