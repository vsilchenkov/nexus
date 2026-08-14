import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";

import { api } from "../api/client";
import { Button, ErrorAlert, Field, Input, Modal } from "../components/ui";

type RequestResponse = { ok: boolean; ttl_minutes: number };

// ForgotPasswordDialog — запрос ссылки для смены пароля (§88.8.2).
//
// Диалог, а не отдельная страница: набранный логин не теряется, а результат
// виден без ухода с формы входа.
//
// Ответ сервера ОДИНАКОВ при любом исходе (§88.4.3) — здесь это выражается
// тем, что успешное состояние одно и не зависит от содержимого ответа: текст
// сообщает «если такой пользователь есть», а не «письмо отправлено».
export function ForgotPasswordDialog({
  initialLogin,
  onClose,
}: {
  initialLogin: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [login, setLogin] = useState(initialLogin);
  const [error, setError] = useState<string | null>(null);
  const [ttl, setTtl] = useState<number | null>(null);

  const send = useMutation({
    mutationFn: () =>
      api.post<RequestResponse>("/api/auth/password-reset/request", { login: login.trim() }),
    onSuccess: (data) => {
      setError(null);
      setTtl(data?.ttl_minutes ?? 60);
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const done = ttl !== null;

  return (
    <Modal
      title={t("auth.forgot_title")}
      subtitle={done ? undefined : t("auth.forgot_subtitle")}
      onClose={onClose}
      footer={
        done ? (
          <Button variant="primary" onClick={onClose}>
            {t("common.close")}
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button
              variant="primary"
              disabled={send.isPending || login.trim() === ""}
              onClick={() => send.mutate()}
            >
              {send.isPending ? "…" : t("auth.forgot_submit")}
            </Button>
          </>
        )
      }
    >
      {done ? (
        <div className="flex gap-3">
          <CheckCircle2 className="mt-0.5 h-5 w-5 shrink-0 text-ok" />
          <div className="space-y-2 text-[13px] leading-relaxed">
            <p>{t("auth.forgot_sent", { minutes: ttl })}</p>
            <p className="text-fg-muted">{t("auth.forgot_sent_hint")}</p>
          </div>
        </div>
      ) : (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (login.trim() !== "" && !send.isPending) send.mutate();
          }}
        >
          <Field label={t("auth.forgot_field")}>
            <Input
              type="text"
              autoComplete="username"
              value={login}
              onChange={(e) => setLogin(e.target.value)}
              autoFocus
            />
          </Field>
          {error && <ErrorAlert>{error}</ErrorAlert>}
        </form>
      )}
    </Modal>
  );
}
