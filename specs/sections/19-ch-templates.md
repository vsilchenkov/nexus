## 19. Шаблоны запросов ClickHouse

Реализовано в Phase F1 (фазы F1.1–F1.5). Карта реализации с привязкой к коду —
в [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

Назначение: оператор-админ заранее готовит **каталог шаблонов DDL** для таблиц
логов. При настройке узла выбирается шаблон и задаётся имя таблицы — сырой SQL
пользователь больше не редактирует. Таблица в ClickHouse создаётся
**автоматически** при сохранении узла, в БД его команды (`nexus_<slug>.<table>`).
Шаблоны позволяют настроить сжатие (CODEC) колонок, дополнительные индексы и
срок хранения (TTL), не нарушая обязательную схему логов из 20 колонок.

До Phase F1 таблица логов создавалась вручную; единственный `CREATE TABLE` был
захардкожен в интеграционных тестах и в §4.3.

### 19.1 Модель данных

- **Обязательные 20 колонок** (§4.3) — единый источник `domain.RequiredLogColumns`
  (`[]CHLogColumn{Name,Type}`) в порядке INSERT/SELECT. Совпадает с `insertSQL`
  Sender'а и `selectCols` LogReader'а. Колонки — инвариант кода: шаблон задаёт
  CODEC/индексы/TTL поверх, но не меняет состав/имена/типы (иначе ломается batch
  INSERT Sender'а).
- **Таблица `ch_templates`** (PostgreSQL, **глобальный** каталог, не per-team;
  миграция 0010):
  - `id` (UUID, PK), `name` (varchar(64), UNIQUE, CHECK
    `^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`), `description` (text),
    `spec` (JSONB), `is_default` (bool), `created_at`, `updated_at`.
  - Частичный уникальный индекс `WHERE is_default` — ровно один шаблон по
    умолчанию.
  - Сид `Standard logs` (`is_default=true`) — рендерится ровно в схему §4.3.
- **`spec` (JSONB)** — структурное описание (не сырой DDL):
  `engine` (только `MergeTree`), `partition_by`, `order_by []string`,
  `column_overrides []{name,codec}` (CODEC поверх обязательной колонки),
  `indexes []{name,expr,type,granularity}`, `ttl_mode` (`none` | `ttl_days`).
- **`nodes.clickhouse_template_id`** (UUID, NULL, FK на `ch_templates`,
  `ON DELETE SET NULL`). `NULL` = ручная таблица (обратная совместимость с
  узлами до Phase F1).

Выбор «структура → генерация DDL» вместо сырого DDL-плейсхолдера даёт
детерминированную верификацию 20 колонок и предметный UI (CODEC/индекс/TTL
вместо textarea).

### 19.2 Рендеринг и параметризация

`CHTemplate.RenderCreateTable(table, ttlDays)` собирает
`CREATE TABLE IF NOT EXISTS <table> (...)`: 20 колонок из `RequiredLogColumns`
(+`CODEC(...)` по `column_overrides`), `INDEX ...`,
`ENGINE = MergeTree PARTITION BY <...> ORDER BY (<...>)`, при `ttl_mode=ttl_days`
— `TTL date_create + INTERVAL <ttlDays> DAY DELETE`. Имя таблицы и `ttlDays` —
параметры функции (не текстовые плейсхолдеры), валидируются перед подстановкой
(анти-инъекция).

### 19.3 Верификация (гибрид)

1. **Статическая** (`CHTemplate.Validate()`, без сети): формат имени; каждый
   `column_overrides.name` ∈ `RequiredLogColumns`; CODEC / `index.type` /
   `partition_by` — по белым спискам + regex; `order_by` непустой и ссылается на
   обязательные колонки. Гарантирует наличие 20 колонок по построению.
2. **Live** (`TeamProvisioner.VerifyTemplate`, как «Test connection»): пробное
   `CREATE TABLE IF NOT EXISTS <db>.__nexus_tmpl_check_<rand>` + `DROP` в defer.
   Ловит несовместимость CODEC/типов для конкретной версии ClickHouse.

Сохранение шаблона без live-проверки допускается (CH может быть недоступен).

### 19.4 Применение к узлу

- `clickhouse_template_id != ""` + provisioner доступен + задано имя таблицы →
  при `Create` рендер шаблона и `CreateTable` (`IF NOT EXISTS`) **до** PG-commit;
  при ошибке узел не создаётся. При `Update` — только если изменились
  `clickhouse_table` или `clickhouse_template_id`. ALTER существующей таблицы —
  вне scope.
- Имя нормализуется до `<team.ch_database>.<table>` (как и прежде, §18.2).
- Атомарность PG+CH: честный 2PC невозможен (CH вне PG-tx). Компромисс —
  «`CreateTable` (идемпотентно) до commit + orphan-cleanup (§7.10)». При
  `provisioner == nil` и заданном `template_id` — явная ошибка `ErrCHUnavailable`,
  не тихий пропуск.

### 19.5 Retention/TTL

Владелец retention по умолчанию — `ch_housekeeping` (partition-drop по
`nodes.clickhouse_retention_days`). Дефолт шаблона — `ttl_mode=none` (без native
TTL), чтобы не было двух механизмов. Native `ttl_days` реализован в генераторе
как advanced-опция (при включении предполагает `retention_days=0`).

### 19.6 API

- `GET /api/ch-templates`, `GET /api/ch-templates/:id` — любая сессия (нужно для
  селектора при настройке узла).
- `POST/PUT/DELETE /api/ch-templates[/:id]` — admin-only. DELETE запрещён для
  default и для используемого узлами шаблона (409, проверка `CountNodesUsing`).
- `POST /api/ch-templates/verify` — admin-only; `{ok}` / `{ok:false,error}`,
  503 при недоступном ClickHouse.

### 19.7 UI

- В настройке узла (NodeSettings) — селектор шаблона (`(none / manual)` +
  список) вместо ручного DDL; имя таблицы — отдельное поле.
- Settings → ClickHouse → «Table templates» (admin-only): список, создание/
  редактирование (имя, описание, PARTITION/ORDER, CODEC по колонкам, индексы,
  TTL-режим, default), кнопка «Verify», удаление.

### 19.8 Scope / неочевидности

- Шаблоны **глобальные** (один каталог на систему); имя БД подставляется в
  команду конкретного узла при применении.
- Изменение шаблонов — только admin; чтение (для селектора) — любая сессия.
- 20 колонок — инвариант; пользователь управляет только сжатием/индексами/
  партиционированием/TTL поверх них.
