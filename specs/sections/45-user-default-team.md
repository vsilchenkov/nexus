# 45. Смена команды по умолчанию пользователя прямо в списке

Раздел вводит inline-смену **команды по умолчанию** (`users.default_team_id`) администратором — кликом
по чипу команды в колонке «Команды» списка `Settings → Users` (§44.G). Связан с §18.9 (резолв
`current_team` по фактическому членству при входе) и §44.G/§44.H (колонка «Команды», warn-чип
рассинхрона).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 45.1 Зачем

`default_team_id` определяет, в какую команду пользователь приземляется при входе (резолв `current_team`,
§18.9). До §45 поменять её из UI было нельзя: при создании она ставилась репозиторием (COALESCE на
`'default'`-команду), а полный `PUT /api/users/:id` её не трогал (DTO `updateUserRequest` не содержит
поля). Колонка «Команды» (§44.G) уже показывает членства с пометкой дефолтной (★) и warn-чип «⚠ default»
при рассинхроне (дефолт вне членств). §45 даёт админу сделать дефолтной любую команду, **в которой
пользователь состоит** — одним кликом по её чипу.

## 45.2 Модель и правило валидации

- Меняется только колонка `users.default_team_id`. Членства (`user_teams`) не трогаются.
- **Назначить дефолтной можно лишь команду из членств пользователя.** Иначе — отказ
  (`ErrUserNotTeamMember` → HTTP 400). Причина: §18.9 при входе всё равно перекинет на команду по
  фактическому членству, поэтому дефолт вне членств бессмыслен (это и есть рассинхрон, который §45
  позволяет починить, выбрав валидную команду; сам warn-чип «⚠ default» как цель клика не кликабелен).
- Активные сессии не трогаются: `default_team_id` влияет на резолв `current_team` при **следующем**
  входе; текущая сессия уже несёт свой `current_team` (его меняет переключатель команд, §18).

## 45.3 API

`PUT /api/users/:id/default-team` (admin-only, рядом с остальными `/users/:id/*`). Тело:

```json
{ "team_id": "<uuid>" }
```

- `204 No Content` — успех.
- `400` — `team_id` не среди членств пользователя (`ErrUserNotTeamMember`) или пустое тело.
- `404` — пользователь не найден (в т.ч. невалидный UUID в `:id` → §44.I, `22P02` трактуется как
  not-found).

Отдельный узкий эндпоинт (а не расширение полного `PUT /api/users/:id`): для inline-клика по чипу не
нужно слать все поля пользователя, и менять состав `updateUserRequest` ради одного поля нежелательно.

## 45.4 Слои (Clean Architecture)

- **handler** [user_handler.go](../../internal/web/adapter/in/http/user_handler.go) `SetDefaultTeam`:
  bind `setDefaultTeamRequest{team_id}`, маппинг ошибок (`ErrUserNotFound`→404, `ErrUserNotTeamMember`→400).
- **usecase** [user.go](../../internal/web/usecase/user.go) `SetDefaultTeam(ctx, actor, userID, teamID)`:
  проверяет существование пользователя (`users.Get`), затем что `teamID` ∈ `teams.ListUserTeams(userID)`
  (§18.9) — иначе `ErrUserNotTeamMember`; затем `users.UpdateDefaultTeam`; пишет audit
  (`ActionUserUpdate`, details `default_team_id`).
- **port/adapter** [user_repo.go](../../internal/web/usecase/port/user_repo.go) `UpdateDefaultTeam`,
  реализация [postgres/user_repo.go](../../internal/web/adapter/out/postgres/user_repo.go):
  `UPDATE users SET default_team_id = $2::uuid WHERE id = $1::uuid` (невалидный UUID → `ErrUserNotFound`).

## 45.5 UI

`Settings → Users`, колонка «Команды» ([Users.tsx](../../web-ui/src/pages/settings/Users.tsx)): чипы
команд-членств — кликабельные кнопки. Клик по НЕ-дефолтной команде → `setDefaultTeam.mutate({id, team_id})`
(`useMutation` → `PUT /api/users/:id/default-team`, `onSuccess → invalidateQueries(["users"])`); чип
становится дефолтным (★), warn-чип «⚠ default» (если был) исчезает. Текущая дефолтная (★) и warn-чип
рассинхрона — не кликабельны как цель (нельзя сделать дефолтной команду без членства). i18n
`settings.users.teams.set_as_default`.

## 45.x Scope (что не входит)

- Смена набора членств (`user_teams`) из этого UI — отдельная задача (управление командами, §18).
- Серверная смена `current_team` активной сессии — это переключатель команд (§18), не §45.
