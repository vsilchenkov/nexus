import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "../../components/ui";
import { useConfirm } from "../../lib/confirm";
import { MY_TEAMS_KEY } from "../../lib/teams";

// Multi-tenancy v2 (§16 ТЗ, Phase 10.F.2): admin создаёт команды, добавляет
// в них пользователей. Каждой команде соответствует своя CH-БД nexus_<slug>.

type Team = {
  id: string;
  slug: string;
  name: string;
  ch_database: string;
  created_at: string;
  updated_at: string;
};

type TeamMember = {
  user_id: string;
  team_id: string;
  login: string;
  // §66: отображаемое имя — показывается вместо логина.
  name?: string;
  email?: string;
  role: "owner" | "admin" | "member";
  created_at: string;
};

type User = {
  id: string;
  login: string;
  name?: string;
  email: string;
};

type ListResp<T> = { items: T[] };

export function TeamsPanel() {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const qc = useQueryClient();

  const list = useQuery({
    queryKey: ["teams"],
    queryFn: () => api.get<ListResp<Team>>("/api/teams"),
  });

  const [editing, setEditing] = useState<Team | "new" | null>(null);
  const [membersOf, setMembersOf] = useState<Team | null>(null);
  const [delError, setDelError] = useState<string | null>(null);

  // Client-side поиск по командам (slug/название/БД) — команд немного,
  // серверный ?search= не нужен (в отличие от /api/users).
  const [search, setSearch] = useState("");
  const q = search.trim().toLowerCase();
  const visibleTeams = (list.data?.items ?? []).filter(
    (tm) => !q || [tm.slug, tm.name, tm.ch_database].some((f) => f.toLowerCase().includes(q)),
  );

  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/teams/${id}`),
    onSuccess: () => {
      setDelError(null);
      qc.invalidateQueries({ queryKey: ["teams"] });
      // Дропдаун в шапке читает ["me-teams"] (/api/me/teams) — без этой
      // инвалидации удалённая команда «висит» в переключателе до F5.
      qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
    },
    // П17: команду с узлами удалить нельзя (409) — показываем понятную ошибку.
    onError: (err: { response?: { data?: { error?: string } } }) =>
      setDelError(err?.response?.data?.error ?? t("common.error")),
  });

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between gap-4 flex-wrap">
        <div>
          <h2 className="text-lg font-semibold">{t("settings.teams.title")}</h2>
          <p className="text-xs text-fg-muted mt-1">
            {t("settings.teams.subtitle")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("settings.teams.search_placeholder")}
            className="px-3 py-2 bg-bg-muted rounded-md outline-none text-sm w-72"
          />
          <button
            onClick={() => setEditing("new")}
            className="bg-accent hover:bg-accent-hover px-3 py-2 rounded-md text-sm"
          >
            {t("settings.teams.add")}
          </button>
        </div>
      </header>

      {list.isLoading && (
        <div className="text-fg-muted text-sm">{t("common.loading")}</div>
      )}
      {list.error && <div className="text-err text-sm">{t("common.error")}</div>}
      {delError && (
        <div className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-sm text-err">
          {delError}
        </div>
      )}

      {list.data && list.data.items.length === 0 && (
        <div className="text-fg-muted text-sm">{t("settings.teams.empty")}</div>
      )}

      {list.data && list.data.items.length > 0 && visibleTeams.length === 0 && (
        <div className="text-fg-muted text-sm">{t("settings.teams.search_empty")}</div>
      )}

      {visibleTeams.length > 0 && (
        <div className="max-h-[65vh] overflow-y-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 bg-bg text-fg-muted">
            <tr>
              <th className="text-left px-3 py-2">{t("settings.teams.col.slug")}</th>
              <th className="text-left px-3 py-2">{t("settings.teams.col.name")}</th>
              <th className="text-left px-3 py-2">{t("settings.teams.col.ch_database")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {visibleTeams.map((team) => {
              const isDefault = team.slug === "default";
              return (
                <tr key={team.id} className="border-t border-bg-muted">
                  <td className="px-3 py-2 font-mono text-xs">{team.slug}</td>
                  <td className="px-3 py-2">{team.name}</td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                    {team.ch_database}
                  </td>
                  <td className="px-3 py-2 text-right space-x-1 whitespace-nowrap">
                    <button
                      title={t("settings.teams.action.members")}
                      onClick={() => setMembersOf(team)}
                      className="px-1.5 py-1 hover:bg-bg-muted rounded text-fg-muted hover:text-fg text-sm"
                    >
                      👥
                    </button>
                    <button
                      title={t("settings.teams.action.edit")}
                      onClick={() => setEditing(team)}
                      className="px-1.5 py-1 hover:bg-bg-muted rounded text-fg-muted hover:text-fg text-sm"
                    >
                      ✏️
                    </button>
                    {!isDefault && (
                      <button
                        title={t("settings.teams.action.delete")}
                        onClick={async () => {
                          if (
                            await confirm({
                              title: t("settings.teams.confirm_delete_title"),
                              message: t("settings.teams.confirm_delete", { slug: team.slug }),
                              confirmLabel: t("settings.teams.action.delete"),
                              danger: true,
                            })
                          ) {
                            del.mutate(team.id);
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
        <TeamDialog
          mode={editing === "new" ? "create" : "edit"}
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["teams"] });
            // Создатель команды добавляется в неё owner'ом на бэкенде — новая
            // команда обязана сразу появиться в переключателе шапки, который
            // читает ["me-teams"]; без инвалидации она видна только после F5.
            qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
          }}
        />
      )}

      {membersOf && (
        <MembersDialog team={membersOf} onClose={() => setMembersOf(null)} />
      )}
    </div>
  );
}

type TeamDialogProps = {
  mode: "create" | "edit";
  initial: Team | null;
  onClose: () => void;
  onSaved: () => void;
};

function TeamDialog({ mode, initial, onClose, onSaved }: TeamDialogProps) {
  const { t } = useTranslation();
  const [slug, setSlug] = useState(initial?.slug ?? "");
  const [name, setName] = useState(initial?.name ?? "");
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: async () => {
      if (mode === "create") {
        return api.post("/api/teams", { slug, name });
      }
      return api.put(`/api/teams/${initial!.id}`, { name });
    },
    onSuccess: () => onSaved(),
    onError: (err: { response?: { data?: { error?: string } } }) => {
      setError(err?.response?.data?.error ?? t("common.error"));
    },
  });

  const canSubmit =
    !save.isPending &&
    name.length >= 1 &&
    (mode === "edit" || /^[a-z][a-z0-9_]{0,31}$/.test(slug));

  return (
    <Modal onClose={onClose}>
      <div className="space-y-4 w-[420px] max-w-full">
        <header>
          <h3 className="text-lg font-semibold">
            {mode === "create"
              ? t("settings.teams.dialog.new_title")
              : t("settings.teams.dialog.edit_title", { slug: initial?.slug })}
          </h3>
          <p className="text-xs text-fg-muted mt-1">
            {mode === "create"
              ? t("settings.teams.dialog.new_subtitle")
              : t("settings.teams.dialog.edit_subtitle")}
          </p>
        </header>

        {error && (
          <div className="bg-err/10 border border-err/40 text-err px-3 py-2 rounded text-sm">
            {error}
          </div>
        )}

        <div className="space-y-3">
          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.teams.field.slug")}
            </label>
            <input
              value={slug}
              onChange={(e) => setSlug(e.target.value.toLowerCase())}
              disabled={mode === "edit"}
              placeholder="acme"
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none disabled:opacity-60 font-mono text-xs"
            />
            <p className="text-xs text-fg-muted">
              {mode === "edit"
                ? t("settings.teams.field.slug_immutable")
                : t("settings.teams.field.slug_hint")}
            </p>
          </div>

          <div className="space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.teams.field.name")}
            </label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={mode === "create" ? "Acme Corp" : ""}
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
            />
          </div>

          {mode === "create" && slug && (
            <div className="text-xs text-fg-muted">
              {t("settings.teams.field.ch_database_preview", {
                db: "nexus_" + slug,
              })}
            </div>
          )}
        </div>

        <footer className="flex items-center justify-end gap-2 pt-2 border-t border-bg-muted">
          <button onClick={onClose} className="px-3 py-2 text-sm text-fg-muted hover:text-fg">
            {t("common.cancel")}
          </button>
          <button
            onClick={() => save.mutate()}
            disabled={!canSubmit}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
          >
            {mode === "create"
              ? t("settings.teams.dialog.create")
              : t("common.save")}
          </button>
        </footer>
      </div>
    </Modal>
  );
}

type MembersDialogProps = {
  team: Team;
  onClose: () => void;
};

function MembersDialog({ team, onClose }: MembersDialogProps) {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const members = useQuery({
    queryKey: ["team-members", team.id],
    queryFn: () => api.get<ListResp<TeamMember>>(`/api/teams/${team.id}/members`),
  });

  const users = useQuery({
    queryKey: ["users-all"],
    queryFn: () => api.get<ListResp<User>>("/api/users"),
  });

  const [newUserID, setNewUserID] = useState<string>("");
  const [newRole, setNewRole] = useState<"owner" | "admin" | "member">("member");

  // После правки состава/ролей перечитываем и собственные членства ["me-teams"]:
  // админ мог добавить/убрать/переролить СЕБЯ — переключатель шапки обязан
  // отразить это без F5 (тот же класс бага, что и создание команды).
  const invalidateMembers = () => {
    qc.invalidateQueries({ queryKey: ["team-members", team.id] });
    qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });
  };

  const add = useMutation({
    mutationFn: () =>
      api.post(`/api/teams/${team.id}/members`, { user_id: newUserID, role: newRole }),
    onSuccess: () => {
      setNewUserID("");
      invalidateMembers();
    },
  });

  const remove = useMutation({
    mutationFn: (userID: string) =>
      api.del(`/api/teams/${team.id}/members/${userID}`),
    onSuccess: invalidateMembers,
  });

  const updateRole = useMutation({
    mutationFn: ({ userID, role }: { userID: string; role: TeamMember["role"] }) =>
      api.put(`/api/teams/${team.id}/members/${userID}`, { role }),
    onSuccess: invalidateMembers,
  });

  // Список пользователей, которые ЕЩЁ не члены команды (для combobox добавления).
  const memberIDs = new Set((members.data?.items ?? []).map((m) => m.user_id));
  const candidates = (users.data?.items ?? []).filter((u) => !memberIDs.has(u.id));

  // Фильтр по УЖЕ добавленным участникам — отвечает на «есть ли он в команде».
  const [memberQuery, setMemberQuery] = useState("");
  const q = memberQuery.trim().toLowerCase();
  const visibleMembers = (members.data?.items ?? []).filter(
    (m) => !q || [m.name, m.login, m.email].some((f) => f?.toLowerCase().includes(q)),
  );

  return (
    <Modal onClose={onClose}>
      <div className="space-y-4 w-[520px] max-w-full">
        <header>
          <h3 className="text-lg font-semibold">
            {t("settings.teams.members.title", { name: team.name })}
          </h3>
          <p className="text-xs text-fg-muted mt-1 font-mono">
            slug={team.slug} · ch_database={team.ch_database}
          </p>
        </header>

        <div className="flex items-end gap-2">
          <div className="flex-1 space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.teams.members.add_user")}
            </label>
            <UserCombobox candidates={candidates} value={newUserID} onChange={setNewUserID} />
          </div>
          <div className="w-32 space-y-1">
            <label className="text-xs uppercase tracking-wider text-fg-muted">
              {t("settings.teams.members.role")}
            </label>
            <select
              value={newRole}
              onChange={(e) => setNewRole(e.target.value as typeof newRole)}
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none"
            >
              <option value="owner">{t("settings.teams.role.owner")}</option>
              <option value="admin">{t("settings.teams.role.admin")}</option>
              <option value="member">{t("settings.teams.role.member")}</option>
            </select>
          </div>
          <button
            onClick={() => add.mutate()}
            disabled={!newUserID || add.isPending}
            className="bg-accent hover:bg-accent-hover px-4 py-2 rounded-md text-sm disabled:opacity-50"
          >
            {t("settings.teams.members.add")}
          </button>
        </div>

        {members.isLoading && (
          <div className="text-fg-muted text-sm">{t("common.loading")}</div>
        )}

        {members.data && members.data.items.length === 0 && (
          <div className="text-fg-muted text-sm">{t("settings.teams.members.empty")}</div>
        )}

        {members.data && members.data.items.length > 0 && (
          <div className="space-y-2">
            <input
              value={memberQuery}
              onChange={(e) => setMemberQuery(e.target.value)}
              placeholder={t("settings.teams.members.filter_placeholder")}
              className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none text-sm"
            />
            {visibleMembers.length === 0 ? (
              <div className="text-fg-muted text-sm px-1">
                {t("settings.teams.members.filter_empty")}
              </div>
            ) : (
              <div className="max-h-[50vh] overflow-y-auto">
                <table className="w-full text-sm">
                  <thead className="text-fg-muted sticky top-0 bg-bg-elev">
                    <tr>
                      <th className="text-left px-3 py-2">
                        {t("settings.teams.members.col.user")}
                      </th>
                      <th className="text-left px-3 py-2">
                        {t("settings.teams.members.col.role")}
                      </th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleMembers.map((m) => (
                      <tr key={m.user_id} className="border-t border-bg-muted">
                        <td className="px-3 py-2">
                          {/* §66: имя — основное, логин — вторичной строкой. */}
                          <div className="text-sm">{m.name || m.login || m.user_id}</div>
                          <div className="text-fg-muted text-[11px] font-mono">
                            {m.login}
                            {m.email && <span className="font-sans"> · {m.email}</span>}
                          </div>
                        </td>
                        <td className="px-3 py-2">
                          <select
                            value={m.role}
                            onChange={(e) =>
                              updateRole.mutate({
                                userID: m.user_id,
                                role: e.target.value as TeamMember["role"],
                              })
                            }
                            className="px-2 py-1 bg-bg-muted rounded text-xs"
                          >
                            <option value="owner">{t("settings.teams.role.owner")}</option>
                            <option value="admin">{t("settings.teams.role.admin")}</option>
                            <option value="member">{t("settings.teams.role.member")}</option>
                          </select>
                        </td>
                        <td className="px-3 py-2 text-right">
                          <button
                            onClick={() => remove.mutate(m.user_id)}
                            className="px-1.5 py-1 hover:bg-bg-muted rounded text-err text-sm"
                            title={t("settings.teams.members.remove")}
                          >
                            🗑
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
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

// Combobox выбора пользователя (Popover + cmdk): поиск по имени/логину/email
// вместо нативного select — на боевых списках из десятков пользователей
// прокрутка option'ов неюзабельна. Показывает только НЕ-участников команды.
function UserCombobox({
  candidates,
  value,
  onChange,
}: {
  candidates: User[];
  value: string;
  onChange: (id: string) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const selected = candidates.find((u) => u.id === value);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="w-full px-3 py-2 bg-bg-muted rounded-md outline-none text-left text-sm"
        >
          {/* §66: имя — основное; логин в скобках, чтобы различать тёзок. */}
          {selected ? (
            selected.name ? (
              `${selected.name} (${selected.login})`
            ) : (
              selected.login
            )
          ) : (
            <span className="text-fg-muted">{t("settings.teams.members.pick_user")}</span>
          )}
        </button>
      </PopoverTrigger>
      {/* z-[60]: локальный Modal — fixed z-50, контент попапа обязан быть выше.
          Esc гасим до window — иначе слушатель Modal закроет весь диалог. */}
      <PopoverContent
        align="start"
        className="w-[360px] p-0 z-[60]"
        onEscapeKeyDown={(e) => e.stopPropagation()}
      >
        <Command>
          <CommandInput placeholder={t("settings.teams.members.search_user")} />
          <CommandList>
            <CommandEmpty>{t("settings.teams.members.not_found")}</CommandEmpty>
            {candidates.map((u) => (
              <CommandItem
                key={u.id}
                // value — предмет встроенного фильтра cmdk; login уникален,
                // поэтому тёзки не схлопываются в один айтем.
                value={`${u.name ?? ""} ${u.login} ${u.email ?? ""}`}
                onSelect={() => {
                  onChange(u.id);
                  setOpen(false);
                }}
              >
                <div>
                  <div>{u.name || u.login}</div>
                  <div className="text-fg-muted text-[11px] font-mono">
                    {u.login}
                    {u.email && <span className="font-sans"> · {u.email}</span>}
                  </div>
                </div>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
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
      <div className="bg-bg-elev rounded-xl border border-bg-muted p-5 shadow-xl max-h-[calc(100vh-4rem)] overflow-y-auto">
        {children}
      </div>
    </div>
  );
}
