import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { useConfirm } from "../../lib/confirm";
import type { Role } from "../../lib/roles";
import { MY_TEAMS_KEY } from "../../lib/teams";

// TeamBrief — команда пользователя для колонки «Команды» (§44.G).
type TeamBrief = { id: string; slug: string; name: string; role: string };

type User = {
  id: string;
  login: string;
  // §66: отображаемое имя — в интерфейсе показывается вместо логина.
  name: string;
  email: string;
  role: Role;
  active: boolean;
  lang: "en" | "ru";
  must_change_password: boolean;
  default_team_id: string;
  teams?: TeamBrief[];
  created_at: string;
  last_login_at?: string;
};

type ListResp = { items: User[] };

type Me = {
  user: { user_id: string; login: string; role: string };
};

function passwordStrength(p: string): { score: 0 | 1 | 2 | 3 | 4; key: string } {
  if (p.length === 0) return { score: 0, key: "settings.users.pw_empty" };
  let score = 0;
  if (p.length >= 8) score++;
  if (/[A-Z]/.test(p) && /[a-z]/.test(p)) score++;
  if (/\d/.test(p)) score++;
  if (/[^A-Za-z0-9]/.test(p)) score++;
  const key =
    score <= 1
      ? "settings.users.pw_weak"
      : score === 2
      ? "settings.users.pw_medium"
      : score === 3
      ? "settings.users.pw_strong"
      : "settings.users.pw_very_strong";
  return { score: score as 0 | 1 | 2 | 3 | 4, key };
}

function generatePassword(): string {
  const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ";
  const lower = "abcdefghijkmnopqrstuvwxyz";
  const digits = "23456789";
  const symbols = "!@#$%^&*-_+=?";
  const all = upper + lower + digits + symbols;
  const pick = (set: string) => set[Math.floor(Math.random() * set.length)];
  const must = [pick(upper), pick(lower), pick(digits), pick(symbols)];
  const rest = Array.from({ length: 8 }, () => pick(all));
  return [...must, ...rest]
    .sort(() => Math.random() - 0.5)
    .join("");
}

function relativeTime(iso?: string, lang = "en"): string {
  if (!iso) return "—";
  const date = new Date(iso);
  const diffSec = Math.floor((Date.now() - date.getTime()) / 1000);
  if (diffSec < 60) return lang === "ru" ? "сейчас" : "just now";
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: "auto" });
  if (diffSec < 3600) return rtf.format(-Math.floor(diffSec / 60), "minute");
  if (diffSec < 86_400) return rtf.format(-Math.floor(diffSec / 3600), "hour");
  if (diffSec < 30 * 86_400) return rtf.format(-Math.floor(diffSec / 86_400), "day");
  if (diffSec < 365 * 86_400) return rtf.format(-Math.floor(diffSec / (30 * 86_400)), "month");
  return rtf.format(-Math.floor(diffSec / (365 * 86_400)), "year");
}

function initials(login: string): string {
  const trimmed = login.trim();
  if (!trimmed) return "?";
  return trimmed.slice(0, 2).toUpperCase();
}

function avatarColor(login: string): string {
  let h = 0;
  for (let i = 0; i < login.length; i++) h = (h * 31 + login.charCodeAt(i)) >>> 0;
  const palette = [
    "bg-accent/20 text-accent",
    "bg-ok/20 text-ok",
    "bg-warn/20 text-warn",
    "bg-err/20 text-err",
    "bg-fg-muted/20 text-fg",
  ];
  return palette[h % palette.length];
}

export function UsersPanel() {
  const { t, i18n } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const lang = i18n.language.startsWith("ru") ? "ru" : "en";

  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<Me>("/api/auth/me"),
  });

  const [search, setSearch] = useState("");

  const list = useQuery({
    queryKey: ["users", search],
    queryFn: () =>
      api.get<ListResp>("/api/users", search ? { search } : undefined),
  });

  const [editing, setEditing] = useState<User | "new" | null>(null);
  const [pwTarget, setPwTarget] = useState<User | null>(null);
  // Диалог «Команды пользователя»: храним id, а не снапшот User — после
  // добавления в команду инвалидация ["users"] перечитывает список, и открытый
  // диалог получает СВЕЖИЙ user.teams (снапшот показывал бы протухший состав).
  const [teamsTargetId, setTeamsTargetId] = useState<string | null>(null);
  const teamsTarget =
    (teamsTargetId && list.data?.items.find((u) => u.id === teamsTargetId)) || null;

  const activeAdmins = useMemo(
    () => (list.data?.items ?? []).filter((u) => u.role === "admin" && u.active).length,
    [list.data],
  );

  const counters = useMemo(() => {
    const items = list.data?.items ?? [];
    return {
      total: items.length,
      active: items.filter((u) => u.active).length,
      disabled: items.filter((u) => !u.active).length,
    };
  }, [list.data]);

  const adminUsesDefault = useMemo(() => {
    // §7.9: баннер, если admin не менял пароль (must_change_password === true и login=admin).
    const adminUser = (list.data?.items ?? []).find((u) => u.login === "admin");
    return !!adminUser?.must_change_password;
  }, [list.data]);

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/users/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["users"] }),
  });

  const toggleActive = useMutation({
    mutationFn: (u: User) =>
      api.put(`/api/users/${u.id}`, {
        name: u.name || u.login, // §66: name обязателен в PUT
        email: u.email,
        role: u.role,
        active: !u.active,
        lang: u.lang,
        must_change_password: u.must_change_password,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["users"] }),
  });

  // §45: смена команды по умолчанию кликом по чипу членства в колонке «Команды».
  const setDefaultTeam = useMutation({
    mutationFn: ({ id, teamId }: { id: string; teamId: string }) =>
      api.put(`/api/users/${id}/default-team`, { team_id: teamId }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["users"] }),
  });

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between gap-4 flex-wrap">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.users.title")}</h2>
          <div className="text-xs text-fg-muted mt-1 flex items-center gap-3">
            <span>
              {t("settings.users.counter_total", { n: counters.total })}
            </span>
            <span className="text-ok">
              {t("settings.users.counter_active", { n: counters.active })}
            </span>
            {counters.disabled > 0 && (
              <span className="text-fg-muted">
                {t("settings.users.counter_disabled", { n: counters.disabled })}
              </span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("settings.users.search_placeholder")}
            className="px-3 py-2 bg-bg-muted rounded-md outline-none text-sm w-60"
          />
          <button
            onClick={() => setEditing("new")}
            className="bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm"
          >
            {t("settings.users.add")}
          </button>
        </div>
      </header>

      {adminUsesDefault && (
        <div className="bg-warn/10 border border-warn/40 text-warn px-3 py-2 rounded-md text-sm">
          {t("settings.users.default_admin_warning")}
        </div>
      )}

      {list.isLoading && (
        <div className="text-fg-muted text-sm">{t("common.loading")}</div>
      )}
      {list.error && (
        <div className="text-err text-sm">{t("common.error")}</div>
      )}

      {list.data && list.data.items.length === 0 && (
        <div className="text-fg-muted text-sm">{t("settings.users.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && (
        <div className="max-h-[65vh] overflow-y-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 bg-bg text-fg-muted">
            <tr>
              <th className="text-left px-3 py-2">{t("settings.users.col.user")}</th>
              <th className="text-left px-3 py-2">{t("settings.users.col.role")}</th>
              <th className="text-left px-3 py-2">{t("settings.users.col.teams")}</th>
              <th className="text-left px-3 py-2">{t("settings.users.col.status")}</th>
              <th className="text-left px-3 py-2">{t("settings.users.col.last_login")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {list.data.items.map((u) => {
              const isSelf = me.data?.user.user_id === u.id;
              const isLastAdmin =
                u.role === "admin" && u.active && activeAdmins <= 1;
              const rowMuted = !u.active ? "opacity-60" : "";
              return (
                <tr key={u.id} className={`border-t border-bg-muted ${rowMuted}`}>
                  <td className="px-3 py-2">
                    <div className="flex items-center gap-3">
                      <div
                        className={`w-8 h-8 rounded-full flex items-center justify-center text-xs font-medium ${avatarColor(u.name || u.login)}`}
                      >
                        {initials(u.name || u.login)}
                      </div>
                      <div>
                        <div className="flex items-center gap-2">
                          {/* §66: имя — основное; логин остаётся вторичной
                              строкой (это кредентиал, админ должен его видеть). */}
                          <span className="font-medium">{u.name || u.login}</span>
                          {isSelf && (
                            <span className="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-accent/15 text-accent">
                              {t("settings.users.you")}
                            </span>
                          )}
                        </div>
                        <div className="text-xs text-fg-muted">
                          <span className="font-mono">{u.login}</span>
                          {u.email && <span> · {u.email}</span>}
                        </div>
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    <span
                      className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs ${
                        u.role === "admin"
                          ? "bg-accent/15 text-accent"
                          : u.role === "manager"
                          ? "bg-ok/15 text-ok"
                          : u.role === "operator"
                          ? "bg-warn/15 text-warn"
                          : "bg-fg-muted/15 text-fg-muted"
                      }`}
                    >
                      {t(`settings.users.role.${u.role}`)}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-1">
                      {(u.teams ?? []).map((tm) => {
                        const isDefault = tm.id === u.default_team_id;
                        return (
                          <button
                            key={tm.id}
                            type="button"
                            disabled={isDefault || setDefaultTeam.isPending}
                            onClick={() =>
                              !isDefault && setDefaultTeam.mutate({ id: u.id, teamId: tm.id })
                            }
                            title={
                              isDefault
                                ? t("settings.users.teams.default")
                                : t("settings.users.teams.set_as_default", { name: tm.name })
                            }
                            className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs ${
                              isDefault
                                ? "bg-accent/15 text-accent cursor-default"
                                : "bg-fg-muted/15 text-fg-muted hover:bg-accent/15 hover:text-accent cursor-pointer disabled:opacity-50"
                            }`}
                          >
                            {isDefault && <span className="text-[10px]">★</span>}
                            {tm.slug}
                          </button>
                        );
                      })}
                      {/* §44.G/H: default_team_id вне членств — рассинхрон. */}
                      {u.default_team_id &&
                        !(u.teams ?? []).some((tm) => tm.id === u.default_team_id) && (
                          <span
                            title={t("settings.users.teams.default_not_member")}
                            className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs bg-warn/15 text-warn"
                          >
                            ⚠ default
                          </span>
                        )}
                      {(u.teams ?? []).length === 0 && !u.default_team_id && (
                        <span className="text-xs text-fg-subtle">{t("settings.users.teams.none")}</span>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    {u.active ? (
                      <span className="inline-flex items-center gap-1.5 text-ok text-xs">
                        <span className="w-1.5 h-1.5 rounded-full bg-ok" />
                        {t("settings.users.status.active")}
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1.5 text-fg-muted text-xs">
                        <span className="w-1.5 h-1.5 rounded-full bg-fg-muted" />
                        {t("settings.users.status.disabled")}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-xs text-fg-muted">
                    {relativeTime(u.last_login_at, lang)}
                  </td>
                  <td className="px-3 py-2 text-right space-x-1 whitespace-nowrap">
                    <button
                      title={t("settings.users.action.change_password")}
                      onClick={() => setPwTarget(u)}
                      className="px-1.5 py-1 hover:bg-bg-muted rounded text-fg-muted hover:text-fg text-sm"
                    >
                      🔑
                    </button>
                    <button
                      title={t("settings.users.action.edit")}
                      onClick={() => setEditing(u)}
                      className="px-1.5 py-1 hover:bg-bg-muted rounded text-fg-muted hover:text-fg text-sm"
                    >
                      ✏️
                    </button>
                    <button
                      title={t("settings.users.action.teams")}
                      onClick={() => setTeamsTargetId(u.id)}
                      className="px-1.5 py-1 hover:bg-bg-muted rounded text-fg-muted hover:text-fg text-sm"
                    >
                      👥
                    </button>
                    {!u.active && (
                      <button
                        title={t("settings.users.action.enable")}
                        onClick={() => toggleActive.mutate(u)}
                        className="px-1.5 py-1 hover:bg-bg-muted rounded text-ok text-sm"
                      >
                        ▶
                      </button>
                    )}
                    {!isSelf && !isLastAdmin && (
                      <button
                        title={t("settings.users.action.delete")}
                        onClick={async () => {
                          if (
                            await confirm({
                              title: t("settings.users.action.delete"),
                              message: t("settings.users.confirm_delete", { login: u.name || u.login }),
                              confirmLabel: t("settings.users.action.delete"),
                              danger: true,
                            })
                          ) {
                            del.mutate(u.id);
                          }
                        }}
                        className="px-1.5 py-1 hover:bg-bg-muted rounded text-err text-sm"
                      >
                        🗑
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        </div>
      )}

      {editing && (
        <UserDialog
          mode={editing === "new" ? "create" : "edit"}
          initial={editing === "new" ? null : editing}
          isSelf={editing !== "new" && me.data?.user.user_id === editing.id}
          activeAdmins={activeAdmins}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["users"] });
          }}
        />
      )}

      {pwTarget && (
        <PasswordDialog
          user={pwTarget}
          onClose={() => setPwTarget(null)}
          onSaved={() => {
            setPwTarget(null);
            qc.invalidateQueries({ queryKey: ["users"] });
          }}
        />
      )}

      {teamsTarget && (
        <UserTeamsDialog user={teamsTarget} onClose={() => setTeamsTargetId(null)} />
      )}
    </div>
  );
}

// Команда из GET /api/teams — для диалога «Команды пользователя» достаточно
// идентификатора и подписей (полный тип живёт в settings/Teams.tsx).
type TeamListItem = { id: string; slug: string; name: string };

// UserTeamsDialog — членства пользователя со стороны страницы пользователей:
// текущие команды read-only + добавление в команду. Использует командо-центричный
// POST /api/teams/{id}/members — отдельного user-центричного эндпоинта нет.
// Удаление из команды — намеренно вне scope (краевые случаи «последний owner»,
// default-команда) — оно остаётся в диалоге участников команды.
function UserTeamsDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const confirm = useConfirm();

  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => api.get<{ items: TeamListItem[] }>("/api/teams"),
  });

  const [teamId, setTeamId] = useState("");
  const [role, setRole] = useState<"owner" | "admin" | "member">("member");
  const [error, setError] = useState<string | null>(null);

  const memberOf = new Set((user.teams ?? []).map((tm) => tm.id));
  const candidates = (teams.data?.items ?? []).filter((tm) => !memberOf.has(tm.id));

  const add = useMutation({
    mutationFn: () => api.post(`/api/teams/${teamId}/members`, { user_id: user.id, role }),
    onSuccess: () => {
      setTeamId("");
      setError(null);
      // ["users"] — чипы в строке и свежий user.teams в этом же диалоге;
      // ["team-members", teamId] — кэш диалога участников этой команды;
      // MY_TEAMS_KEY — админ мог добавить СЕБЯ: переключатель шапки обязан
      // отразить это без F5 (тот же класс бага, что в Teams.tsx).
      qc.invalidateQueries({ queryKey: ["users"] });
      qc.invalidateQueries({ queryKey: ["team-members", teamId] });
      qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const remove = useMutation({
    mutationFn: (teamID: string) => api.del(`/api/teams/${teamID}/members/${user.id}`),
    // Тот же набор инвалидаций, что и у add: чипы строки + свежий user.teams
    // в диалоге, кэш участников команды, членства самого админа для шапки.
    onSuccess: (_data, teamID) => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["users"] });
      qc.invalidateQueries({ queryKey: ["team-members", teamID] });
      qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  // §45: смена команды по умолчанию — тот же PUT, что у чипов в колонке
  // «Команды»; после успеха ["users"] перечитывается, и ★ в диалоге и в
  // строке списка переезжает без F5.
  const setDefault = useMutation({
    mutationFn: (teamID: string) =>
      api.put(`/api/users/${user.id}/default-team`, { team_id: teamID }),
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const askRemove = async (tm: TeamBrief) => {
    if (
      await confirm({
        title: t("settings.users.teams_dialog.remove"),
        message: t("settings.users.teams_dialog.confirm_remove", {
          name: user.name || user.login,
          team: tm.slug,
        }),
        confirmLabel: t("settings.users.teams_dialog.remove"),
        danger: true,
      })
    ) {
      remove.mutate(tm.id);
    }
  };

  return (
    <Modal onClose={onClose}>
      <div className="space-y-4 w-[460px] max-w-full">
        <header>
          <h3 className="text-lg font-semibold">
            {t("settings.users.teams_dialog.title", { name: user.name || user.login })}
          </h3>
        </header>

        {error && (
          <div className="bg-err/10 border border-err/40 text-err px-3 py-2 rounded text-sm">
            {error}
          </div>
        )}

        <div className="space-y-1">
          <div className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.users.teams_dialog.current")}
          </div>
          {(user.teams ?? []).length === 0 ? (
            <div className="text-sm text-fg-muted">{t("settings.users.teams_dialog.none")}</div>
          ) : (
            <div className="flex flex-wrap gap-1 pt-1">
              {(user.teams ?? []).map((tm) => {
                const isDefault = tm.id === user.default_team_id;
                return (
                <span
                  key={tm.id}
                  className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs bg-fg-muted/15 text-fg"
                >
                  <button
                    type="button"
                    disabled={isDefault || setDefault.isPending}
                    onClick={() => setDefault.mutate(tm.id)}
                    title={
                      isDefault
                        ? t("settings.users.teams.default")
                        : t("settings.users.teams.set_as_default", { name: tm.name })
                    }
                    className={`text-[10px] ${
                      isDefault
                        ? "text-accent cursor-default"
                        : "text-fg-muted hover:text-accent disabled:opacity-50"
                    }`}
                  >
                    {isDefault ? "★" : "☆"}
                  </button>
                  {tm.slug}
                  <span className="text-fg-muted">· {t(`settings.teams.role.${tm.role}`)}</span>
                  <button
                    type="button"
                    disabled={remove.isPending}
                    onClick={() => askRemove(tm)}
                    title={t("settings.users.teams_dialog.remove")}
                    className="ml-0.5 text-fg-muted hover:text-err disabled:opacity-50"
                  >
                    ×
                  </button>
                </span>
                );
              })}
            </div>
          )}
        </div>

        {teams.isLoading && (
          <div className="text-fg-muted text-sm">{t("common.loading")}</div>
        )}

        {teams.data && candidates.length === 0 && (
          <div className="text-fg-muted text-sm">
            {t("settings.users.teams_dialog.all_teams")}
          </div>
        )}

        {teams.data && candidates.length > 0 && (
          <div className="flex items-end gap-2">
            <div className="flex-1 space-y-1">
              <label className="text-xs uppercase tracking-wider text-fg-muted">
                {t("settings.users.teams_dialog.add_team")}
              </label>
              <select
                value={teamId}
                onChange={(e) => setTeamId(e.target.value)}
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
              >
                <option value="">{t("settings.users.teams_dialog.pick_team")}</option>
                {candidates.map((tm) => (
                  <option key={tm.id} value={tm.id}>
                    {tm.name} ({tm.slug})
                  </option>
                ))}
              </select>
            </div>
            <div className="w-32 space-y-1">
              <label className="text-xs uppercase tracking-wider text-fg-muted">
                {t("settings.users.teams_dialog.role")}
              </label>
              <select
                value={role}
                onChange={(e) => setRole(e.target.value as typeof role)}
                className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
              >
                <option value="owner">{t("settings.teams.role.owner")}</option>
                <option value="admin">{t("settings.teams.role.admin")}</option>
                <option value="member">{t("settings.teams.role.member")}</option>
              </select>
            </div>
            <button
              onClick={() => add.mutate()}
              disabled={!teamId || add.isPending}
              className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
            >
              {t("settings.users.teams_dialog.add")}
            </button>
          </div>
        )}

        <footer className="flex items-center justify-end pt-2 border-t border-bg-muted">
          <button onClick={onClose} className="px-3 py-2 text-sm text-fg-muted hover:text-fg">
            {t("common.close")}
          </button>
        </footer>
      </div>
    </Modal>
  );
}

type UserDialogProps = {
  mode: "create" | "edit";
  initial: User | null;
  isSelf: boolean;
  activeAdmins: number;
  onClose: () => void;
  onSaved: () => void;
};

function UserDialog({ mode, initial, isSelf, activeAdmins, onClose, onSaved }: UserDialogProps) {
  const { t } = useTranslation();

  const [login, setLogin] = useState(initial?.login ?? "");
  // §66: отображаемое имя — обязательно при создании и изменении.
  const [name, setName] = useState(initial?.name ?? "");
  const [email, setEmail] = useState(initial?.email ?? "");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>(initial?.role ?? "viewer");
  const [active, setActive] = useState(initial?.active ?? true);
  const [mustChange, setMustChange] = useState(initial?.must_change_password ?? true);
  const [error, setError] = useState<string | null>(null);

  const isLastAdmin =
    mode === "edit" &&
    initial?.role === "admin" &&
    initial.active &&
    activeAdmins <= 1;

  const save = useMutation({
    mutationFn: async () => {
      if (mode === "create") {
        return api.post("/api/users", {
          login,
          name: name.trim(),
          email,
          password,
          role,
          active,
          lang: "en",
          must_change_password: mustChange,
        });
      }
      return api.put(`/api/users/${initial!.id}`, {
        name: name.trim(),
        email,
        role,
        active,
        lang: initial!.lang,
        must_change_password: mustChange,
      });
    },
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const pw = passwordStrength(password);

  const canSubmit =
    save.isPending || name.trim() === ""
      ? false
      : mode === "create"
      ? !!login && password.length >= 8
      : true;

  return (
    <Modal onClose={onClose}>
      {/* §87.5: четвёртая роль добавила ряд карточек — без ограничения высоты
          форма создания выталкивала бы кнопки за нижний край на низких экранах
          (у Modal своей прокрутки нет). */}
      <div className="space-y-4 w-[460px] max-w-full max-h-[80vh] overflow-y-auto pr-1">
        <header>
          <h3 className="text-lg font-semibold">
            {mode === "create"
              ? t("settings.users.dialog.new_title")
              : t("settings.users.dialog.edit_title", { login: initial?.name || initial?.login })}
          </h3>
          <p className="text-xs text-fg-muted mt-1">
            {mode === "create"
              ? t("settings.users.dialog.new_subtitle")
              : t("settings.users.dialog.edit_subtitle")}
          </p>
        </header>

        {error && (
          <div className="bg-err/10 border border-err/40 text-err px-3 py-2 rounded text-sm">
            {error}
          </div>
        )}

        <div className="space-y-3">
          {/* §66: имя — первым (основное представление пользователя в UI). */}
          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.users.field.name")}
            </label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("settings.users.field.name_placeholder")}
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
            />
            <p className="text-xs text-fg-muted">
              {t("settings.users.field.name_hint")}
            </p>
          </div>

          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.users.field.login")}
            </label>
            <input
              value={login}
              onChange={(e) => setLogin(e.target.value)}
              disabled={mode === "edit"}
              placeholder="login"
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none disabled:opacity-60"
            />
            <p className="text-xs text-fg-muted">
              {t("settings.users.field.login_hint")}
            </p>
          </div>

          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.users.field.email")}
            </label>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="user@example.com"
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
            />
          </div>

          {mode === "create" && (
            <div className="space-y-1">
              <label className="text-xs uppercase tracking-wider text-fg-muted">
                {t("settings.users.field.password")}
              </label>
              <div className="flex gap-2">
                <input
                  type="text"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="min 8 chars"
                  className="flex-1 px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
                />
                <button
                  type="button"
                  onClick={() => setPassword(generatePassword())}
                  className="px-3 py-2 bg-bg-muted hover:bg-bg-muted/70 rounded-md text-xs"
                >
                  {t("settings.users.field.generate")}
                </button>
              </div>
              <PasswordMeter score={pw.score} labelKey={pw.key} />
            </div>
          )}

          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.users.field.role")}
            </label>
            {/*
              §87.5: ролей стало четыре, и в один ряд они не помещаются — окно
              диалога 460px, на карточку осталось бы ~97px и подсказки рвались бы
              по слогам. Отсюда сетка 2×2; порядок — по убыванию прав.
            */}
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {(["admin", "manager", "operator", "viewer"] as const).map((r) => {
                const disabledLastAdmin = isLastAdmin && r !== "admin";
                const disabledSelf = isSelf && initial?.role === "admin" && r !== "admin";
                const disabled = disabledLastAdmin || disabledSelf;
                return (
                  <button
                    type="button"
                    key={r}
                    onClick={() => !disabled && setRole(r)}
                    disabled={disabled}
                    className={`text-left p-3 rounded-md border transition-colors ${
                      role === r
                        ? "border-accent bg-accent/10"
                        : "border-bg-muted hover:border-fg-muted"
                    } ${disabled ? "opacity-50 cursor-not-allowed" : ""}`}
                  >
                    <div className="text-sm font-medium">
                      {t(`settings.users.role.${r}`)}
                    </div>
                    <div className="text-xs text-fg-muted mt-1">
                      {t(`settings.users.role.${r}_hint`)}
                    </div>
                  </button>
                );
              })}
            </div>
            {isLastAdmin && (
              <p className="text-xs text-warn">
                {t("settings.users.last_admin_warning")}
              </p>
            )}
          </div>

          {mode === "edit" && (
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={active}
                onChange={(e) => setActive(e.target.checked)}
                disabled={isSelf || isLastAdmin}
              />
              {t("settings.users.field.active")}
              {(isSelf || isLastAdmin) && (
                <span className="text-xs text-fg-muted ml-2">
                  {t(
                    isSelf
                      ? "settings.users.cannot_disable_self"
                      : "settings.users.last_admin_warning",
                  )}
                </span>
              )}
            </label>
          )}

          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={mustChange}
              onChange={(e) => setMustChange(e.target.checked)}
            />
            {t("settings.users.field.must_change")}
          </label>
        </div>

        <footer className="flex items-center justify-end gap-2 pt-2 border-t border-bg-muted">
          <button
            onClick={onClose}
            className="px-3 py-2 text-sm text-fg-muted hover:text-fg"
          >
            {t("common.cancel")}
          </button>
          <button
            onClick={() => save.mutate()}
            disabled={!canSubmit}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
          >
            {mode === "create"
              ? t("settings.users.dialog.create")
              : t("common.save")}
          </button>
        </footer>
      </div>
    </Modal>
  );
}

type PasswordDialogProps = {
  user: User;
  onClose: () => void;
  onSaved: () => void;
};

function PasswordDialog({ user, onClose, onSaved }: PasswordDialogProps) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [mustChange, setMustChange] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useMutation({
    mutationFn: () =>
      api.post(`/api/users/${user.id}/password`, {
        new_password: password,
        must_change_password: mustChange,
      }),
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const pw = passwordStrength(password);
  const canSubmit = password.length >= 8 && !submit.isPending;

  return (
    <Modal onClose={onClose}>
      <div className="space-y-4 w-[400px] max-w-full">
        <header>
          <h3 className="text-lg font-semibold">
            {t("settings.users.password_dialog.title", { login: user.name || user.login })}
          </h3>
        </header>

        {error && (
          <div className="bg-err/10 border border-err/40 text-err px-3 py-2 rounded text-sm">
            {error}
          </div>
        )}

        <div className="space-y-1">
          <label className="text-xs uppercase tracking-wider text-fg-muted">
            {t("settings.users.password_dialog.new_password")}
          </label>
          <div className="flex gap-2">
            <input
              type="text"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="min 8 chars"
              className="flex-1 px-3 py-2 bg-bg-muted rounded-md outline-none font-mono text-xs"
            />
            <button
              type="button"
              onClick={() => setPassword(generatePassword())}
              className="px-3 py-2 bg-bg-muted hover:bg-bg-muted/70 rounded-md text-xs"
            >
              {t("settings.users.field.generate")}
            </button>
          </div>
          <PasswordMeter score={pw.score} labelKey={pw.key} />
        </div>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={mustChange}
            onChange={(e) => setMustChange(e.target.checked)}
          />
          {t("settings.users.password_dialog.must_change")}
        </label>

        <footer className="flex items-center justify-end gap-2 pt-2 border-t border-bg-muted">
          <button
            onClick={onClose}
            className="px-3 py-2 text-sm text-fg-muted hover:text-fg"
          >
            {t("common.cancel")}
          </button>
          <button
            onClick={() => submit.mutate()}
            disabled={!canSubmit}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
          >
            {t("settings.users.password_dialog.submit")}
          </button>
        </footer>
      </div>
    </Modal>
  );
}

function PasswordMeter({ score, labelKey }: { score: 0 | 1 | 2 | 3 | 4; labelKey: string }) {
  const { t } = useTranslation();
  const colors = ["bg-bg-muted", "bg-err", "bg-warn", "bg-warn", "bg-ok"];
  return (
    <div className="space-y-1">
      <div className="flex gap-1">
        {[1, 2, 3, 4].map((i) => (
          <div
            key={i}
            className={`h-1 flex-1 rounded ${score >= i ? colors[score] : "bg-bg-muted"}`}
          />
        ))}
      </div>
      <p className="text-xs text-fg-muted">{t(labelKey)}</p>
    </div>
  );
}

// Закрытие ТОЛЬКО явным действием (Esc / «Отмена», §21): клик по подложке не
// закрывает — случайный клик мимо окна терял введённые данные формы.
function Modal({ onClose, children }: { onClose: () => void; children: React.ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4">
      <div className="bg-bg-elev rounded-xl border border-bg-muted p-5 shadow-xl">
        {children}
      </div>
    </div>
  );
}
