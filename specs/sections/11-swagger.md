## 11. Swagger / OpenAPI

Все HTTP-эндпоинты документируются через OpenAPI 3.0. Используется `github.com/swaggo/swag` (генерация из аннотаций в коде).

### 11.1 Что описывается

Swagger покрывает **только HTTP-эндпоинты**. Это два сервиса:

- **Receiver Service** — `/request/{node_path}`, `/requestAsync/{node_path}` (документируется как `{nodePath}` со списком известных узлов в описании), `/health`, `/ready`, `/metrics`.
- **Web Service API** — все endpoint'ы админки: `/api/auth/login`, `/api/auth/logout`, `/api/nodes` (GET/POST/PUT/DELETE), `/api/nodes/{id}/test` (dry-run, см. §7.5.1), `/api/nodes/{id}/logs` (GET с фильтрами + пагинация), `/api/nodes/{id}/logs/stream` (SSE для live-tail, см. §7.4), `/api/logs/{log_id}/replay` (POST, см. §7.4.1), `/api/users` (CRUD), `/api/settings/clickhouse` (GET/PUT/test), `/api/settings/sentry`, `/api/audit` (GET с фильтрами, см. §7.13).

Sender Service общается только по gRPC и в Swagger не описывается — его контракт зафиксирован в `.proto` (см. §4.1) и этого достаточно.

### 11.2 Способ ведения

- Аннотации `@Summary`, `@Description`, `@Tags`, `@Param`, `@Success`, `@Failure` пишутся в комментариях над handler-функциями.
- Структуры request/response описываются с тегами `json:"..." example:"..."` и `swaggertype` где нужно.
- Команда `make swagger` запускает `swag init` для каждого сервиса и обновляет файлы `/docs/receiver/`, `/docs/web/`.
- В CI шаг «swagger drift check» — генерирует свежую документацию и фейлит сборку, если она отличается от закоммиченной (защита от забытого `make swagger`).

### 11.3 Публикация

- В dev/staging — Swagger UI доступен по `/swagger/index.html` для каждого сервиса (handler от `swaggo/gin-swagger`).
- В production — Swagger UI отключается флагом конфигурации (`receiver.swagger_enabled: false`), документация выгружается как статика на отдельный domain или в Confluence.

