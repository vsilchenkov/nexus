import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight } from "lucide-react";

import { api } from "../api/client";
import { Button, Card, ErrorAlert, Field, Input } from "../components/ui";

// DEV_PREFILL_LOGIN — подсказка логина на СТЕНДЕ (§85.8). На боевой установке
// префилла нет вовсе: форма входа не должна называть имя существующего
// привилегированного аккаунта каждому, кто открыл страницу.
const DEV_PREFILL_LOGIN = "admin";

export default function Login() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // touched — пользователь уже трогал поле логина. Ответ /api/version приходит
  // асинхронно, и без этого флага он затирал бы уже набранное.
  const touched = useRef(false);

  // §85.8: единственный публичный /api — тот же запрос версии, что уже делает
  // футер SPA (queryKey "version"), поэтому лишнего вызова не появляется.
  // Недоступность/ошибка → префилла нет: fail-closed = боевое поведение.
  const version = useQuery({
    queryKey: ["version"],
    queryFn: () => api.get<{ dev_mode?: boolean }>("/api/version"),
    staleTime: Infinity,
    retry: false,
  });
  const devMode = version.data?.dev_mode === true;

  useEffect(() => {
    if (devMode && !touched.current && login === "") setLogin(DEV_PREFILL_LOGIN);
  }, [devMode, login]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/api/auth/login", { login, password });
      navigate("/", { replace: true });
    } catch (err: unknown) {
      const ex = err as { response?: { data?: { error?: string } } };
      setError(ex?.response?.data?.error ?? t("auth.login_failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center bg-app text-fg">
      <div className="w-[340px]">
        <div className="mb-6 text-center">
          <div className="mx-auto mb-3.5 grid h-12 w-12 place-items-center rounded-lg bg-gradient-to-br from-accent to-ok text-white">
            <ArrowLeftRight className="h-6 w-6" />
          </div>
          <div className="text-[19px] font-semibold">{t("app.title")}</div>
          <div className="mt-1 text-[13px] text-fg-muted">{t("auth.login")}</div>
        </div>
        <Card className="p-6">
          <form onSubmit={onSubmit} className="space-y-3.5">
            <Field label={t("auth.login_field")}>
              {/* autoComplete сохранён: на своей машине браузер подставит
                  СОБСТВЕННЫЙ логин пользователя — удобство возвращающегося
                  оператора от снятия префилла не страдает. */}
              <Input
                type="text"
                autoComplete="username"
                value={login}
                onChange={(e) => {
                  touched.current = true;
                  setLogin(e.target.value);
                }}
                required
              />
            </Field>
            <Field label={t("auth.password_field")}>
              <Input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </Field>
            {error && <ErrorAlert>{error}</ErrorAlert>}
            <Button type="submit" variant="primary" disabled={busy} className="w-full">
              {busy ? "…" : t("auth.login_button")}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  );
}
