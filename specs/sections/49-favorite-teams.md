# 49. Избранные команды (favorite teams)

Раздел вводит **избранные команды** пользователя: отображаемое имя команды вместо `Name (slug)`
в шапке, звезда «в избранное» в переключателе команд и секцию «Избранное» в сайдбаре с
переключением по клику и drag-and-drop-переупорядочиванием. Развивает §18 (multi-tenancy,
переключатель команд) и §45 (валидация «команда ∈ членства»).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 49.1 Отображаемое имя команды

- Везде, где пользователь видит команду в шапке, показывается **только `Team.Name`** — без slug и
  скобок. До §49 переключатель в Topbar рендерил `Name (slug)`.
- Триггер переключателя команд — кнопка с именем текущей команды (это и есть «представление команды
  сверху главного окна»); в выпадающем списке — имена команд, текущая отмечена галкой.
- Нативный `<select>` заменён кастомным Radix Popover: `<option>` не умеет ни звезду, ни разметку.

## 49.2 Избранное

- **Звезда** возле каждой команды в списке переключателя добавляет/убирает её из избранного.
  Кнопка выбора команды и кнопка-звезда — соседние элементы (не вложенные): звезда не переключает
  команду.
- **Секция «Избранное»** в сайдбаре (между основной навигацией и подвалом): избранные команды в
  пользовательском порядке. Клик — переключение текущей команды (no-op на текущей; текущая
  подсвечена). 0 избранных → секция скрыта целиком. Секция занимает **весь остаток высоты** сайдбара до
подвала («Настройки») и скроллится внутри себя. Прежний фиксированный потолок `max-h 40vh`
снят в §86: на длинном списке он давал прокрутку при пустом месте снизу — оператор листал
там, где листать было незачем.
- **Drag-and-drop** внутри секции меняет порядок (библиотека `@dnd-kit`). Порог активации 6px —
  клик не превращается в 0px-драг; click, прилетающий после drop, гасится флагом.

## 49.3 Модель данных

Миграция `0023_user_team_favorites`:

```sql
CREATE TABLE user_team_favorites (
    user_id    UUID        NOT NULL,
    team_id    UUID        NOT NULL,
    position   INT         NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, team_id),
    FOREIGN KEY (user_id, team_id) REFERENCES user_teams (user_id, team_id) ON DELETE CASCADE,
    CHECK (position >= 0)
);
```

- **Составной FK на `user_teams(user_id, team_id)`**, а не два отдельных FK на users/teams: один
  каскад покрывает удаление пользователя, удаление команды и **исключение из членства**
  (`RemoveMember` удаляет строку `user_teams` напрямую), плюс БД-инвариант «избранное ⊆ членство».
- `position` — порядок в списке. Дырки после каскада терпимы: чтение сортирует по `position`,
  запись всегда перезаписывает список целиком компактными `0..n-1`.

## 49.4 API

- **Чтение** — в существующем `GET /api/me/teams` добавлено поле `favorites: ["<team_id>", ...]`
  (упорядоченный список). Один источник истины для переключателя и сайдбара — без второго запроса
  и рассинхрона кешей; логика `healed` (§44.H) не тронута.
- **Запись** — `PUT /api/me/favorite-teams`, тело `{"team_ids": ["<uuid>", ...]}` — **полная
  замена** списка (позиция = индекс). Добавление/удаление/reorder — один и тот же вызов; пустой
  массив очищает избранное. Guard `RequireSessionOnly()` (как switch-team: API-токен ограничен
  одной командой, избранное ему ни к чему).
  - `200 {"team_ids": [...]}` — успех.
  - `400` — дубликаты, больше 100 элементов (`ErrFavoriteTeamsInvalid`) или id вне членств
    пользователя (`ErrUserNotTeamMember`, прецедент §45).
  - `401` — нет сессии.

## 49.5 Слои (Clean Architecture)

- **port** [favorite_team_repo.go](../../internal/web/usecase/port/favorite_team_repo.go) —
  отдельный малый интерфейс `FavoriteTeamRepo` (`ListFavoriteTeamIDs`/`ReplaceFavoriteTeams`).
  Сознательно НЕ расширение `port.TeamRepo`: стабы TeamRepo в существующих тестах не ломаются (ISP).
- **adapter** [postgres/team_repo.go](../../internal/web/adapter/out/postgres/team_repo.go) —
  `TeamRepoPg` реализует оба порта. List: `JOIN user_teams` + `ORDER BY position` (страховка поверх
  FK). Replace: транзакция `DELETE`+`INSERT` (position = индекс), FK-нарушение 23503 (гонка с
  исключением из команды) → `ErrUserNotTeamMember`.
- **usecase** [auth.go](../../internal/web/usecase/auth.go) (владеет self-service team-поверхностью):
  builder `WithFavoriteTeams(repo)` (nil → фича выключена); `FavoriteTeamIDs` — **никогда не
  возвращает ошибку** (деградация в пустой список: избранное — декорация, не должно валить
  MyTeams); `SetFavoriteTeams` — валидация (лимит/дубликаты/членство) + Replace + аудит
  `user.favorite_teams.update`.
- **handler** [auth_handler.go](../../internal/web/adapter/in/http/auth_handler.go): `MyTeams`
  дополнен `favorites`; `SetFavoriteTeams` — bind `omitempty,dive,uuid` (**без `required`** — иначе
  пустой массив, т.е. последнее «убрать из избранного», отклонялся бы 400).

## 49.6 UI

- Общий слой [lib/teams.ts](../../web-ui/src/lib/teams.ts) (вынесен из Topbar): `useMyTeams`,
  `useSwitchTeam`, `useSetFavoriteTeams`, `invalidateTeamScoped`. Сайдбар переключает команду с той
  же семантикой инвалидации team-scoped кеша, что и Topbar.
  - `useSwitchTeam.onError`: 403 (членство сняли, UI ещё показывает команду) → invalidate
    `["me-teams"]` — протухший пункт исчезает сам (на бэке избранное уже удалено каскадом).
  - `useSetFavoriteTeams`: optimistic update кеша `["me-teams"]` (звезда/порядок без мигания),
    откат при ошибке, `invalidateQueries` по завершении.
- [TeamSwitcher.tsx](../../web-ui/src/components/TeamSwitcher.tsx) — Popover-переключатель (§49.1/49.2).
- [SidebarFavorites.tsx](../../web-ui/src/components/SidebarFavorites.tsx) — секция «Избранное»;
  `useSortable` — в отдельном компоненте `FavoriteItem` (хук нельзя звать в map).
- i18n: `teams.switcher_label`/`teams.favorite_add`/`teams.favorite_remove`/`teams.drag_hint`,
  `nav.favorites` — en/ru синхронно.

## 49.x Scope (что не входит)

- Избранное **пер-пользовательское и глобальное** (не пер-команда и не пер-браузер): хранится в PG,
  переживает смену устройства. localStorage сознательно не используется.
- Избранные узлы/страницы — не в этом разделе (только команды).
- Шаринг избранного между пользователями, «команды по умолчанию для новых пользователей» — вне scope.
