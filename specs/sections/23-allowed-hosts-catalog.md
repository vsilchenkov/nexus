## 23. Каталог разрешённых хостов (Allowed Hosts catalog)

Раздел вводит общий для инсталляции **каталог разрешённых хостов** для защиты от SSRF при
`url_mode = from_request`. До этого allowlist хранился только как per-node массив
`nodes.url_allowed_hosts TEXT[]` (§3.4, миграция 0002) без переиспользования между узлами и без UI.
Каталог делает паттерны общими, типизированными (exact/wildcard/regex) и управляемыми из админки.
Мокап — [../ui_allowed_hosts.html](../ui_allowed_hosts.html).

### 23.1. Модель данных

- **`host_allowlist`** — каталог паттернов (общий для инсталляции, не per-team):
  `id`, `pattern`, `kind` (`exact`|`wildcard`|`regex`), `description`, `usage_count`, `created_by`,
  `created_at`, `updated_at`. Уникальность `pattern` без учёта регистра (`UNIQUE (lower(pattern))`).
- **`node_allowed_hosts(node_id, host_id)`** — many-to-many привязка паттернов к узлам. У одного
  хоста много узлов, у узла — до 50 хостов.
- **`usage_count`** денормализован — обновляется PostgreSQL-trigger'ом при изменении
  `node_allowed_hosts` (чтение дешевле, чем `COUNT(*)` на каждый рендер).
- Миграция — `migrations/0011_host_allowlist.{up,down}.sql`.

**Денормализованный снимок (ключевое решение).** `nodes.url_allowed_hosts TEXT[]` **сохраняется** как
снимок паттернов привязанных хостов. Receiver читает его из JSON-кеша узла (Redis) и **не трогается**
этой фичей — горячий путь неизменен. Источник истины — `node_allowed_hosts`; при привязке/отвязке Web
пересобирает снимок и обновляет write-through кеш. `kind` кодируется в плоском массиве: `exact` →
`api.partner.com`, `wildcard` → `*.partner.com`, `regex` → `re:<pattern>` (префикс `re:` безопасен —
hostname не содержит `:`). Редактирование паттерна разрешено только при `usage_count = 0` → стейл-копий
в снимках не бывает.

### 23.2. Матчинг (единый для Receiver и preview)

`domain.HostAllowed(host, patterns)` — единственная реализация матчинга, вызывается и из
Receiver (`urlresolver.go`), и из Web preview (DRY). Правила: пустой список = разрешено всё; host
нормализуется (lower-case, срез порта); `exact` — точное совпадение; `*.x` — любой поддомен `x`
(но не сам `x`); `re:<re>` — `regexp.Compile` против hostname (regex **не** лоуэркейзится; невалидный
regexp = deny). Sentinel-блокировки приватных диапазонов (§3.4) остаются поверх каталога — даже
паттерн `.*` не пустит запрос в localhost/метаданные облака.

### 23.3. API

- `GET /api/allowed-hosts?q=&kind=&limit=` — поиск/листинг (любая сессия; combobox формы узла).
- `POST /api/allowed-hosts` `{pattern, kind, description?}` — создать (admin). Идемпотентно по
  case-insensitive паттерну: повтор возвращает существующую запись.
- `PATCH /api/allowed-hosts/:id` — описание всегда; паттерн/тип только при `usage_count = 0` (иначе 409).
- `DELETE /api/allowed-hosts/:id` — 409, если используется (FK RESTRICT — второй уровень защиты).
- `POST /api/allowed-hosts/preview` `{pattern, kind, test_urls}` — `{allowed, blocked}` через
  `domain.HostAllowed` (вся логика на сервере, не дублируется в JS).
- `GET /api/nodes/:id/allowed-hosts` — привязанные паттерны (chips формы узла).
- `POST /api/nodes/:id/allowed-hosts` `{host_id}` / `DELETE …/:host_id` — привязка/отвязка (admin):
  в одной UoW-транзакции link/unlink + пересборка снимка + audit, затем write-through `cache.Set`.

**Изменение контракта узла:** `POST/PUT /api/nodes` больше **не** задают allowlist из тела —
снимок управляется только каталогом. Create стартует с пустым allowlist, Update сохраняет
существующий снимок.

### 23.4. UI

- **Settings → Allowed Hosts** (`pages/settings/AllowedHosts.tsx`, admin): таблица с фильтром по типу
  и поиском, кнопка добавления, диалог создания/редактирования с **живым превью «Разрешит /
  Заблокирует»** (через preview-эндпоинт). Удаление заблокировано при `usage_count > 0`.
- **Форма узла** (`components/node/AllowedHostsField.tsx`): в режиме `from_request` — chips
  привязанных паттернов (с бейджем типа) + cmdk-combobox выбора из каталога. Для существующего узла —
  attach/detach сразу; для нового — локальный список, привязка после создания. Создание новых паттернов —
  на странице Settings (там выбор типа и превью). Пустой allowlist в `from_request` → красное
  SSRF-предупреждение.

### 23.5. Audit

`host.create` / `host.update` / `host.delete` — изменения каталога; `host.attach` / `host.detach` —
привязки к узлам (с `details.node_id`, `details.host_pattern`).
