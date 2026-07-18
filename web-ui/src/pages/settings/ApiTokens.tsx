import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { useConfirm } from "../../lib/confirm";
import { useMyTeams } from "../../lib/teams";

type Token = {
  id: string;
  name: string;
  // team_id (§18.3): токен действует только в своей команде. Показываем его
  // явно — раньше скоуп был невидим, и токен молча наследовал команду,
  // выбранную в шапке в момент создания.
  team_id: string;
  prefix: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked_at?: string;
};
type ListResp = { items: Token[] };
// info, а не api_token: так поле называется в ответе бэкенда (раньше здесь
// было api_token → created.api_token всегда undefined).
type CreateResp = { token: string; info: Token };

const allScopes = ["logs:read", "nodes:read", "metrics:read", "audit:read"];

export function ApiTokensPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["tokens"],
    queryFn: () => api.get<ListResp>("/api/tokens"),
  });
  // Членства: источник селекта команды в форме и имён в колонке «Команда».
  // Список токенов НЕ скоупится командой — «Настройки» не реагируют на
  // переключатель в шапке (ключ ["tokens"] в TEAM_INDEPENDENT_KEYS).
  const teams = useMyTeams();
  const teamName = (id: string) =>
    teams.data?.items.find((x) => x.id === id)?.name ?? id.slice(0, 8);

  const [showNew, setShowNew] = useState(false);
  // Значение токена показывается ровно один раз — и при создании, и при
  // перевыпуске (rotate). Храним только строку: баннеру больше ничего не нужно.
  const [createdToken, setCreatedToken] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["logs:read"]);
  const [days, setDays] = useState<number | "">(365);
  // Команда токена (§18.3) выбирается явно. Отправная точка — текущая команда
  // на момент ОТКРЫТИЯ формы (см. openNew): это удобный дефолт, а не реакция на
  // шапку — сама страница переключением команд не управляется.
  const [teamID, setTeamID] = useState("");
  useEffect(() => {
    if (!teamID && teams.data) setTeamID(teams.data.current_team_id);
  }, [teams.data, teamID]);

  const openNew = () => {
    if (teams.data) setTeamID(teams.data.current_team_id);
    setShowNew(true);
  };

  const create = useMutation({
    mutationFn: () =>
      api.post<CreateResp>("/api/tokens", {
        name,
        scopes,
        team_id: teamID,
        expires_in_days: days === "" ? null : days,
      }),
    onSuccess: (r) => {
      setCreatedToken(r.token);
      qc.invalidateQueries({ queryKey: ["tokens"] });
      setShowNew(false);
      setName("");
    },
  });

  const revoke = useMutation({
    mutationFn: (id: string) => api.post(`/api/tokens/${id}/revoke`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });

  // Rotate — перевыпуск значения токена: старое сразу теряет силу, новое
  // показывается один раз в том же баннере, что и при создании (copy_now).
  const rotate = useMutation({
    mutationFn: (id: string) => api.post<{ token: string }>(`/api/tokens/${id}/rotate`),
    onSuccess: (r) => {
      setCreatedToken(r.token);
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
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
          onClick={openNew}
          className="bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm"
        >
          {t("settings.tokens.new_token")}
        </button>
      </header>

      {createdToken && (
        <div className="bg-warn/10 border border-warn/40 text-warn p-3 rounded-md text-sm space-y-2">
          <div className="font-medium">{t("settings.tokens.copy_now")}</div>
          <code className="block bg-bg-muted px-2 py-1 rounded font-mono select-all">
            {createdToken}
          </code>
          <button onClick={() => setCreatedToken(null)} className="underline text-xs">
            {t("settings.tokens.close")}
          </button>
        </div>
      )}

      {showNew && (
        <div className="bg-bg-muted/40 p-4 rounded-md space-y-3">
          {/* §18.3: токен действует только в одной команде — выбираем её явно
              здесь, а не наследуем молча из переключателя в шапке. Список —
              только свои команды: сервер проверяет членство (403). */}
          <label className="flex items-center gap-2 text-sm">
            <span className="text-fg-muted">{t("settings.tokens.team_label")}</span>
            <select
              value={teamID}
              onChange={(e) => setTeamID(e.target.value)}
              className="bg-bg-muted rounded-md px-2 py-1 outline-none"
            >
              {(teams.data?.items ?? []).map((tm) => (
                <option key={tm.id} value={tm.id}>
                  {tm.name}
                </option>
              ))}
            </select>
          </label>
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
              <th className="text-left px-3 py-2">{t("settings.tokens.col_team")}</th>
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
                <td className="px-3 py-2 text-fg-muted">{teamName(tk.team_id)}</td>
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
                  {/* Rotate — только для активного токена (не отозван и не
                      просрочен): перевыпуск мёртвого токена бессмыслен, для него
                      создают новый. Значение показывается один раз (баннер выше). */}
                  {!tk.revoked_at &&
                    !(tk.expires_at && new Date(tk.expires_at) < new Date()) && (
                      <button
                        onClick={async () => {
                          if (
                            await confirm({
                              title: t("settings.tokens.rotate"),
                              message: t("settings.tokens.confirm_rotate"),
                              confirmLabel: t("settings.tokens.rotate"),
                              danger: true,
                            })
                          )
                            rotate.mutate(tk.id);
                        }}
                        className="text-accent hover:underline text-xs"
                      >
                        {t("settings.tokens.rotate")}
                      </button>
                    )}
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
