import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";

export default function Login() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [login, setLogin] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/api/auth/login", { login, password });
      navigate("/", { replace: true });
    } catch (err: any) {
      setError(err?.response?.data?.error ?? t("auth.login_failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-bg">
      <form
        onSubmit={onSubmit}
        className="bg-bg-elev p-8 rounded-2xl shadow-xl w-[360px] space-y-4 border border-bg-muted"
      >
        <h1 className="text-2xl font-semibold mb-2">{t("app.title")}</h1>
        <h2 className="text-sm text-fg-muted">{t("auth.login")}</h2>

        <input
          className="w-full px-3 py-2 bg-bg-muted rounded-md border border-bg-muted focus:border-accent outline-none"
          type="text"
          autoComplete="username"
          value={login}
          onChange={(e) => setLogin(e.target.value)}
          placeholder="login"
          required
        />
        <input
          className="w-full px-3 py-2 bg-bg-muted rounded-md border border-bg-muted focus:border-accent outline-none"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="password"
          required
        />

        {error && <div className="text-err text-sm">{error}</div>}

        <button
          type="submit"
          disabled={busy}
          className="w-full bg-accent hover:bg-accent-hover transition-colors py-2 rounded-md font-medium disabled:opacity-50"
        >
          {busy ? "…" : t("auth.login_button")}
        </button>
      </form>
    </div>
  );
}
