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
- Backfill (миграция 0009): `nodes.clickhouse_table` приводится к формату
  `<db>.<table>` (`vika_logs.<x>` и unprefixed `<x>` → `nexus_default.<x>`).

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
  - **api_tokens** — токен наследует `current_team_id` создателя.
  - **users** — список и создание ограничены участниками команды
    (`user_teams`); создание добавляет membership.
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

### 18.8 Что осталось вне раздела (возможные расширения)

- Роль `team_admin` с ограниченными правами (сейчас admin/viewer —
  глобальные роли UI; роли в `user_teams` хранятся, но RBAC по ним не
  разведён).
- Квоты/биллинг per team, self-service onboarding (SaaS-режим).
- Перенос узла между РАЗНЫМИ ClickHouse-серверами (сейчас только один
  сервер, RENAME в пределах него).
