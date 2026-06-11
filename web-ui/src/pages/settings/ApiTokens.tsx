import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { useConfirm } from "../../lib/confirm";

type Token = {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked_at?: string;
};
type ListResp = { items: Token[] };
type CreateResp = { token: string; api_token: Token };

const allScopes = ["logs:read", "nodes:read", "metrics:read", "audit:read"];

export function ApiTokensPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["tokens"],
    queryFn: () => api.get<ListResp>("/api/tokens"),
  });

  const [showNew, setShowNew] = useState(false);
  const [created, setCreated] = useState<CreateResp | null>(null);

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["logs:read"]);
  const [days, setDays] = useState<number | "">(365);

  const create = useMutation({
    mutationFn: () =>
      api.post<CreateResp>("/api/tokens", {
        name,
        scopes,
        expires_in_days: days === "" ? null : days,
      }),
    onSuccess: (r) => {
      setCreated(r);
      qc.invalidateQueries({ queryKey: ["tokens"] });
      setShowNew(false);
      setName("");
    },
  });

  const revoke = useMutation({
    mutationFn: (id: string) => api.post(`/api/tokens/${id}/revoke`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("settings.tokens.title")}</h2>
        <button
          onClick={() => setShowNew(true)}
          className="bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm"
        >
          {t("settings.tokens.new_token")}
        </button>
      </header>

      {created && (
        <div className="bg-warn/10 border border-warn/40 text-warn p-3 rounded-md text-sm space-y-2">
          <div className="font-medium">{t("settings.tokens.copy_now")}</div>
          <code className="block bg-bg-muted px-2 py-1 rounded font-mono select-all">
            {created.token}
          </code>
          <button onClick={() => setCreated(null)} className="underline text-xs">
            {t("settings.tokens.close")}
          </button>
        </div>
      )}

      {showNew && (
        <div className="bg-bg-muted/40 p-4 rounded-md space-y-3">
          <input
            placeholder={t("settings.tokens.name_placeholder")}
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
          />
          <div className="flex flex-wrap gap-3">
            {allScopes.map((s) => (
              <label key={s} className="text-sm flex items-center gap-2 font-mono">
                <input
                  type="checkbox"
                  checked={scopes.includes(s)}
                  onChange={(e) =>
                    setScopes((p) =>
                      e.target.checked ? [...p, s] : p.filter((x) => x !== s),
                    )
                  }
                />
                {s}
              </label>
            ))}
          </div>
          <div className="text-sm flex items-center gap-2">
            {t("settings.tokens.expires_days")} · {t("settings.tokens.expires_lifetime")}
            <input
              type="number"
              value={days}
              onChange={(e) =>
                setDays(e.target.value === "" ? "" : Number(e.target.value))
              }
              className="w-20 px-2 py-1 bg-bg-muted rounded-md outline-none"
            />
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => create.mutate()}
              disabled={create.isPending || !name}
              className="bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm disabled:opacity-50"
            >
              {t("settings.tokens.create")}
            </button>
            <button
              onClick={() => setShowNew(false)}
              className="px-3 py-2 text-sm text-fg-muted hover:text-fg"
            >
              {t("settings.tokens.cancel")}
            </button>
          </div>
        </div>
      )}

      {list.data && list.data.items.length === 0 && (
        <div className="text-fg-muted text-sm">{t("settings.tokens.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && (
        <div className="max-h-[65vh] overflow-y-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 bg-bg text-fg-muted">
            <tr>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_name")}</th>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_prefix")}</th>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_scopes")}</th>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_created")}</th>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_last_used")}</th>
              <th className="text-left px-3 py-2">{t("settings.tokens.col_status")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.items.map((tk) => (
              <tr key={tk.id} className="border-t border-bg-muted">
                <td className="px-3 py-2">{tk.name}</td>
                <td className="px-3 py-2 font-mono text-xs">{tk.prefix}</td>
                <td className="px-3 py-2 font-mono text-xs">{tk.scopes.join(", ")}</td>
                <td className="px-3 py-2 font-mono text-xs">
                  {new Date(tk.created_at).toLocaleDateString()}
                </td>
                <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                  {tk.last_used_at ? new Date(tk.last_used_at).toLocaleString() : "—"}
                </td>
                <td className="px-3 py-2">
                  {tk.revoked_at ? (
                    <span className="text-err">{t("settings.tokens.status_revoked")}</span>
                  ) : tk.expires_at && new Date(tk.expires_at) < new Date() ? (
                    <span className="text-warn">{t("settings.tokens.status_expired")}</span>
                  ) : (
                    <span className="text-ok">{t("settings.tokens.status_active")}</span>
                  )}
                </td>
                <td className="px-3 py-2 text-right space-x-2">
                  {!tk.revoked_at && (
                    <button
                      onClick={() => revoke.mutate(tk.id)}
                      className="text-warn hover:underline text-xs"
                    >
                      {t("settings.tokens.revoke")}
                    </button>
                  )}
                  <button
                    onClick={async () => {
                      if (
                        await confirm({
                          title: t("settings.tokens.delete"),
                          message: t("settings.tokens.confirm_delete"),
                          confirmLabel: t("settings.tokens.delete"),
                          danger: true,
                        })
                      )
                        del.mutate(tk.id);
                    }}
                    className="text-err hover:underline text-xs"
                  >
                    {t("settings.tokens.delete")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}
    </div>
  );
}
