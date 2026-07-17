## 18. Multi-tenancy v2

Multi-tenancy реализована в v2 (фазы Phase 10 + Phase 11). В v1 раздел был
закладкой в §16 «Out of scope» — теперь это полноценный раздел ТЗ.
Карта реализации с привязкой к коду — в [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

Назначение: с системой работают независимые команды (tenants). Узлы,
логи, API-токены и аудит изолированы по команде; пользователь команды A
не видит ресурсы команды B. Каждой команде соответствует своя БД
ClickHouse для логов.

### 18.1 Модель данных

- `teams` — справочник команд:
  - `id` (UUID, PK), `slug` (varchar, UNIQUE, формат `^[a-z][a-z0-9_]{0,31}$`),
    `name`, `ch_database` (varchar, UNIQUE, формат `^nexus_[a-z][a-z0-9_]{0,31}$`),
    `created_at`, `updated_at`.
  - `slug` и `ch_database` **неизменяемы** после создания (переименование
    БД ClickHouse в полёте сломало бы Sender). Меняется только `name`.
- `user_teams` — членство many-to-many: `user_id` × `team_id` × `role`
  (`owner` / `admin` / `member`), `created_at`. PK `(user_id, team_id)`,
  `ON DELETE CASCADE`.
- Изменения существующих таблиц (миграция 0008):
  - `nodes.team_id` — UUID FK на `teams` (`ON DELETE RESTRICT`). Снят
    глобальный `UNIQUE(path)`, поставлен **`UNIQUE(team_id, path)`** — две
    команды могут иметь узлы с одинаковым path.
  - `users.team_id` → `users.default_team_id` (UUID FK) — команда по
    умолчанию при логине; реальная видимость — через `user_teams`.
  - `api_tokens.team_id` (UUID FK) — токен ограничен одной командой.
  - `user_audit.team_id` (UUID FK, NULL = глобальное действие).
- Сидинг: команда `default` (`ch_database = nexus_default`), стартовый
  `admin` — её owner.
- Нормализация: `nodes.clickhouse_table` приводится к формату `<db>.<table>`
  на write-time в Web (`NodeUsecase.normalizeCHTable` при создании/обновлении
  узла) — unprefixed `<x>` → `nexus_default.<x>`. Backfill-миграция для
  legacy-данных не нужна (стенд greenfield, узлов со старым форматом нет).

### 18.2 ClickHouse: БД на команду

- Каждая команда пишет логи в свою БД `nexus_<slug>` на одном
  ClickHouse-сервере (модель «1 сервер, много БД»).
- `nodes.clickhouse_table` хранит полное имя `<team.ch_database>.<table>`.
  Нормализация — на write-time в Web (при создании/обновлении узла), а не
  на read-time в Sender: Sender за каждое сообщение не ходит в PG за
  именем БД.
- Provisioning: при создании команды атомарно создаётся PG-запись +
  `CREATE DATABASE IF NOT EXISTS nexus_<slug>` (при ошибке CH — откат
  PG-записи). Создавать команду можно только при доступном ClickHouse.
- Таблицы логов узлов автоматически кодом **не создаются** — это
  ответственность оператора / внешнего инструмента (как и в v1). Перенос
  узла переносит таблицу через `RENAME TABLE`, если она существует.

### 18.3 Scope и сессия

- В серверной сессии (Redis) хранится `current_team_id`. При логине =
  `default_team_id` пользователя.
- `GET /api/me/teams` — список команд пользователя (+ текущая).
- `POST /api/me/switch-team {team_id}` — смена текущей команды
  (проверяется членство в `user_teams`). Cookie не меняется, сессия не
  инвалидируется. Только для session-cookie: API-токены ограничены своей
  командой и переключать её не могут.
- Scope-фильтрация по `current_team_id` во всех list/get/mutate:
  - **nodes** — Create/Get/Update/Delete/List; cross-team → 404.
  - **logs / replay / dry-run** — узел чужой команды → 404.
  - **api_tokens** — команда токена **выбирается в форме создания** (список — команды-членства
    владельца; сервер проверяет членство, чужая команда → 403). Отправная точка селекта — текущая
    команда, но молчаливого наследования `current_team_id` больше нет: скоуп виден при создании и
    показан колонкой «Команда» в списке. Сам список токенов **не скоупится** — «Настройки» вне
    скоупа команды (§7.14.1).
  - **users** — **список глобальный** (все пользователи, admin-only): в
    отличие от прочих ресурсов, пользователь — глобальная сущность, а членство
    в командах — отдельная ось (управляется в Teams → Members). **Создание**
    по-прежнему добавляет membership в текущую команду (новый юзер сразу
    функционален). Изменено относительно Phase 11.A, где **список** скоупился
    по `user_teams` — из-за чего в одной команде не было видно юзеров другой и
    приходилось искать людей по командам.
  - **audit** — `user_audit.team_id` заполняется из сессии; по умолчанию
    admin видит журнал своей команды (`?team_id=*` — глобально).

### 18.4 Receiver: URL с team_slug

- Входящий маршрут: `/v1/request/<team_slug>/<node_path>` (и
  `requestAsync`, `callback`). Резолв узла — `JOIN teams ... WHERE
  teams.slug = ? AND nodes.path = ?`.
- Legacy `/v1/request/<node_path>` (без слога) продолжает работать как
  `default`-team — для обратной совместимости существующих интеграций.
- Cross-team изоляция: чужой `team_slug` → 404 (не утечка существования
  узла). Redis-ключ кеша — `node:<team_slug>:<path>`, L2-кеш — по
  `<team_slug>/<path>`.

### 18.5 Перенос узла между командами

- `POST /api/nodes/<id>/move {target_team_slug}` (admin-only).
- PG-запись авторитетна: `team_id` и `clickhouse_table` (rebase на БД
  целевой команды) меняются в одной транзакции + audit `node.move`.
  Конфликт пути в целевой команде (`UNIQUE(team_id, path)`) → 409.
- Логи следуют за узлом: `RENAME TABLE old_db.tbl TO new_db.tbl`
  (best-effort; если исходной таблицы нет — операция пропускается, узел
  переносится).

### 18.6 Web UI

- `Settings → Teams` (admin-only): CRUD команд (slug/name, preview
  `ch_database`), управление участниками (add / change role / remove).
  `default`-команду удалить нельзя.
- Topbar team-switcher: `<select>` со списком команд; при смене —
  `POST /api/me/switch-team` + перезагрузка данных под новый scope.
- Overview: действие «Move» — перенос узла в другую команду.

### 18.7 Конфигурация

- Redis ACL (Phase 11.C): поле `redis.username` (`${REDIS_USER:}`) — для
  серверов с включённым ACL и выключенным default-пользователем.

### 18.9 Резолв current_team при входе (по членству, не по `default_team_id`)

`current_team_id` сессии **резолвится по фактическому членству** в `user_teams`,
а не берётся слепо из `users.default_team_id` (фикс §44.H). Причина: колонка
`default_team_id` и членство — разные сущности; пользователя можно убрать из
команды, бывшей его дефолтом, не переписав колонку — тогда `default_team_id`
указывает на команду, в которой он не состоит.

- **Логин** (`AuthUsecase.resolveLoginTeam`): если `default_team_id` среди
  членств — он; иначе первая команда из членств (`ORDER BY slug`); если членств
  нет — оставляем `default_team_id`. Иначе пользователь попадал бы на ноды чужой
  команды, а переключатель был бы заблокирован (одна доступная команда ≠ текущая).
- **Самолечение** (`AuthUsecase.MyTeamsAndCurrent`, `GET /api/me/teams`): если
  current в сессии не входит в членства — переключаем на первую доступную и
  персистим; ответ несёт `healed=true`, по которому UI инвалидирует team-scoped
  кеш. Псевдо-сессии API-токена (`token==""`) не трогаем — у них team фиксирована.
- **Согласованность** (`TeamRepoPg.RemoveMember`): удаление пользователя из
  команды, бывшей его `default_team_id`, переводит колонку на другую его команду
  (если есть). Рассинхрон виден и в `Settings → Users` (чип «default (!)»).

### 18.8 Что осталось вне раздела (возможные расширения)

- Роль `team_admin` с ограниченными правами (сейчас admin/viewer —
  глобальные роли UI; роли в `user_teams` хранятся, но RBAC по ним не
  разведён).
- Квоты/биллинг per team, self-service onboarding (SaaS-режим).
- Перенос узла между РАЗНЫМИ ClickHouse-серверами (сейчас только один
  сервер, RENAME в пределах него).
