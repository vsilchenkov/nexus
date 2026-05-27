# DataBus

[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](go.mod)

Шина данных — три Go-сервиса (Receiver, Sender, Web), которые принимают входящие HTTP-запросы, маршрутизируют их на сконфигурированные внешние узлы и логируют все вызовы. Конфигурация маршрутов хранится в PostgreSQL, редактируется через REST API и SPA (React 18 + Vite + Tailwind). Полное ТЗ — [specs/nexus_spec.md](./specs/nexus_spec.md); разделено по разделам в [specs/sections/](./specs/sections/).

## Статус

| Фаза     | Что готово                                                                       |
|----------|----------------------------------------------------------------------------------|
| Phase 0  | Скелет, docker-compose, healthcheck, миграции, kardianos/service runner          |
| Phase 1  | Sync end-to-end: /v1/request/* → gRPC к Sender → внешний URL; ClickHouse-логи    |
| Phase 1  | Все режимы auth (none/basic/token/token_from_request/basic_from_request)         |
| Phase 1  | URL-режимы static / from_request с allowlist                                     |
| Phase 1  | AES-256-GCM шифрование auth_credentials в БД                                     |
| Phase 2  | Async /v1/requestAsync/* → Kafka (databus.async/dlq); Sender-consumer; paused-pacing |
| Phase 2  | Circuit breaker + rate-limit (Redis); audit log таблица + запись для CRUD узлов  |
| Phase 3  | Web auth: users CRUD, sessions в Redis, login/logout/me, RBAC, --set-admin-password CLI |
| Phase 3  | API-токены: SHA-256 hash, scopes, rate-limit, audit                              |
| Phase 3  | SPA каркас через embed.FS (index.html-заглушка с REST-документацией)             |
| Phase 4  | loadtest бинарь с pass/fail-критериями (§10.2)                                   |
| Phase 4  | unit-тесты критических usecase'ов; housekeeping cron (audit retention)           |
| Phase 5  | paused→202, dry-run (§7.5.1), replay (§7.4.1), SSE live-tail (§7.4)              |
| Phase 5  | rotate-encryption-key utility (§5.5); CH partition-drop housekeeping (§4.3)      |
| Phase 5  | Swagger generation + drift-check; port.UnitOfWork; Sentry tracing-spans          |
| Phase 5  | i18n (Accept-Language en/ru); SPA на React 18 + Vite + Tailwind (Login, Overview, NodeDetail, NodeSettings, Audit, Settings/{API tokens,Language,Theme}) |
| Phase 5  | integration-тесты testcontainers: node-repo + receiver sync end-to-end           |
| Phase 5.1 | CH file-fallback (NDJSON); расширенный i18n на handlers; unit-тесты Replay/Logs/Sentry middleware |
| Phase 6  | Prometheus метрики; app_settings + hot-reload Sentry/ClickHouse + test connection; Users CRUD; live-tail UI (подсветка/баннер); CSV-экспорт audit; CH orphan-tables; live-tail фильтры; audit diff |
| Phase 7  | Swagger 100% endpoints + UI; L2 LRU кеш узлов; integration suite (Redis+CH через testcontainers); GitLab CI pipeline; Grafana dashboard + Prometheus alerts; **GoReleaser релизы + multi-arch docker в GitLab Container Registry; security scanning** |
| Out-of-scope (v2) | KMS-интеграция, multi-tenancy логика, webhook signature verification, OpenTelemetry |

## Зависимости

| Компонент    | Версия |
|--------------|--------|
| Go           | 1.25   |
| PostgreSQL   | 16     |
| Redis        | 7      |
| ClickHouse   | 24     |
| Kafka        | 3.7 (KRaft) |
| Prometheus   | 2.55   |

## Быстрый старт через Docker

```bash
cp .env.example .env       # отредактируйте пароли + ENCRYPTION_KEY
cp config/config.example.yml config/config.yml
docker compose -f deploy/docker-compose.yml up -d

# bootstrap пароля admin (миграция 0002 создаёт его с password_hash=NULL)
make set-admin-password PASSWORD=mySecretPass

# проверка
curl http://localhost:8000/health
curl -X POST http://localhost:8000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"login":"admin","password":"mySecretPass"}'
```

UI на `http://localhost:8000/`. Swagger UI — `http://localhost:8000/swagger/index.html` (см. Phase 7.1 в [IMPLEMENTATION.md](./specs/IMPLEMENTATION.md)).

## Создание первого узла и тестовый запрос

```bash
# из под admin-сессии (cookie databus_session):
curl -X POST http://localhost:8000/api/nodes -b cookies.txt \
  -H "Content-Type: application/json" \
  -d '{
    "path": "test/echo",
    "root_method": "request",
    "target_url": "https://httpbin.org/anything",
    "auth_type": "none",
    "incoming_auth_type": "none",
    "clickhouse_table": "vika_logs.test_echo"
  }'

# создать таблицу в ClickHouse (схема из §4.3 ТЗ — TODO Phase 3 UI помощник)

# отправить запрос через шину:
curl -X POST http://localhost:8080/v1/request/test/echo \
  -H "Content-Type: application/json" \
  -d '{"hello":"world"}'
```

## Запуск через Makefile (локальная разработка)

```bash
make docker-up-dev          # postgres + redis + clickhouse + kafka + prometheus
make migrate-up
make set-admin-password PASSWORD=...
make run-receiver           # в одном терминале
make run-sender             # во втором
make run-web                # в третьем
```

### Windows

`make` ставится одним из:
- `choco install make` (Chocolatey, требует admin)
- `scoop install make`
- `mingw32-make`

Каждый бинарь также устанавливается как Windows-сервис (kardianos/service):
```cmd
bin\receiver.exe install
sc start DataBusReceiverService
```

## Отладка в VS Code (Windows, Docker Desktop для зависимостей)

В каталоге [.vscode/](.vscode/) лежит готовый конфиг отладки: launch.json с конфигами для каждого сервиса и compound «DataBus: all», tasks.json с задачами `deps: up/down/logs`, миграциями и тестами, settings.json под `gopls`/`dlv-dap`.

Сценарий: зависимости (PostgreSQL, Redis, ClickHouse, Kafka) поднимаются в Docker Desktop отдельным compose-файлом с пробросом портов на хост; бинари `receiver`/`sender`/`web` стартуют локально под отладчиком и цепляются к `localhost:<port>`.

### Разовая подготовка

- Установить Docker Desktop, Go 1.25+, VS Code + расширение `golang.go` (через Command Palette → «Go: Install/Update Tools» поставить `dlv`, `gopls`, `golangci-lint`).
- В корне создать `.env` (см. [.env.example](.env.example)). Минимум обязателен `ENCRYPTION_KEY` — 32 байта в base64. Сгенерировать в PowerShell:

  ```powershell
  [Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))
  ```

### Запуск зависимостей

```powershell
docker compose -f deploy/docker-compose.deps.yml up -d
```

или VS Code Command Palette → **Tasks: Run Task → deps: up**. Проверка состояния — `deps: status`; полный сброс данных — `deps: down + reset volumes`.

Compose [deploy/docker-compose.deps.yml](deploy/docker-compose.deps.yml) — самодостаточный, поднимает только зависимости с портами `5432/6379/8123/9000/9092` на хосте. Prometheus спрятан за профилем `metrics` (по умолчанию выключен — scrape-таргеты в [prometheus.yml](deploy/prometheus.yml) ориентированы на контейнерные имена).

### Миграции и bootstrap admin (один раз)

- **Tasks: Run Task → migrate up** (или `go run ./cmd/web --debug --migrate-up`)
- **Tasks: Run Task → set admin password (admin)** (или `go run ./cmd/web --debug --set-admin-password admin`)

### Старт под отладчиком

В панели **Run and Debug** (`Ctrl+Shift+D`):

| Конфиг                             | Что делает                                                       |
|------------------------------------|------------------------------------------------------------------|
| `Web (debug)`                      | `go run ./cmd/web --debug` под dlv → :8000                       |
| `Receiver (debug)`                 | `go run ./cmd/receiver --debug` под dlv → :8080                  |
| `Sender (debug)`                   | `go run ./cmd/sender --debug` под dlv → :9090 gRPC + :9091       |
| `DataBus: all`                     | compound: все три сервиса одной кнопкой (`stopAll: true`)        |
| `Web: migrate-up`                  | разовая миграция через отладчик                                  |
| `Web: set-admin-password`          | задаёт пароль admin'у                                            |
| `Loadtest`                         | `cmd/loadtest` против локального стека (1m / 100 RPS / 10 узлов) |
| `Integration tests (current file)` | `go test -tags=integration -v` для открытого файла               |
| `Attach to process (pid)`          | подключение к уже запущенному процессу                           |

Все конфиги загружают переменные из `${workspaceFolder}/.env`, используют `--debug` → [config/config_debug.yml](config/config_debug.yml) (все хосты — `localhost`).

### После запуска

- Web UI / API → `http://localhost:8000`
- Swagger UI → `http://localhost:8000/swagger/index.html`
- Receiver → `http://localhost:8080/v1/request/*`, `http://localhost:8080/v1/requestAsync/*`
- Sender admin → `http://localhost:9091/health` (gRPC SenderService — на `:9090`)

### Типовые грабли

- `ENCRYPTION_KEY invalid` — декодированный base64 не равен 32 байтам, перегенерируй.
- `clickhouse connect failed` в Web — не критично: replay/live-tail отключатся, остальное работает ([bootstrap.go:161](internal/platform/bootstrap/bootstrap.go#L161)).
- Kafka не стартует за 10 сек — норма для KRaft на Windows; дай 30 сек. Логи: `docker compose -f deploy/docker-compose.deps.yml logs kafka`.
- Порт 5432/6379/9092 занят — выключи локальный postgres/redis/kafka-сервис или поменяй проброс в `docker-compose.deps.yml`.

## Конфигурация

- `config/config.yml` — production, в git не коммитится. Шаблон — `config/config.example.yml`.
- `config/config_debug.yml` — localhost-адреса для `make run-*`.
- `.env` — секреты, в git не коммитится.

Выбор конфига по приоритету: `--config` → `$DATABUS_CONFIG` → `--debug` → `config/config.yml`.

## API

- Receiver `:8080` — `/v1/request/*`, `/v1/requestAsync/*`, `/health`, `/ready`, `/metrics`.
- Sender `:9090` (gRPC SenderService) + admin `:9091` (`/health`, `/ready`, `/metrics`).
- Web `:8000` — `/api/*`, SPA fallback, `/health`, `/ready`, `/metrics`.

REST API задокументировано в [internal/web/static/index.html](./internal/web/static/index.html) (он же — UI-заглушка при открытии `http://localhost:8000/`).

OpenAPI / Swagger: `make swagger` генерирует [docs/web/](./docs/web/) из аннотаций в Go-handlers; `make swagger-drift-check` для CI. Интерактивный UI — `http://localhost:8000/swagger/index.html`.

## Docker images (release builds)

Релизные multi-arch образы (amd64+arm64) собираются [.goreleaser.yaml](./.goreleaser.yaml)
и публикуются в GitLab Container Registry по тегу `v*`:
`$CI_REGISTRY_IMAGE/{receiver,sender,web}:<version>`.

Локальный snapshot для проверки релизной сборки — `make release-snapshot`
(артефакты в `dist/`).

## CI/CD

Pipeline живёт в [.gitlab-ci.yml](./.gitlab-ci.yml), запускается на self-hosted
runner с тегом `srv-d-android-l-docker` (docker-executor). Stages:

| Stage         | Что делает                                                                  |
|---------------|-----------------------------------------------------------------------------|
| `test`        | `go vet ./...`, `go test -race -short ./...`                                |
| `lint`        | `golangci-lint run`, `swagger-drift` (проверка `docs/` против аннотаций)    |
| `build`       | `go build ./...`, `ui-build` (Vite + lint + build для `web-ui/`)            |
| `integration` | testcontainers PG/Redis/Kafka/CH; `loadtest` (manual или RUN_PROFILE)       |
| `security`    | `govulncheck`, `gosec`, `trivy-fs`, `renovate` (weekly schedule)            |
| `release`     | `goreleaser release` на теги `v*` → GitLab Container Registry               |

Селективный ручной запуск через **«Run pipeline»** в UI с переменной
`RUN_PROFILE = integration-only | loadtest-only` (см. шапку [.gitlab-ci.yml](./.gitlab-ci.yml)
для required CI/CD Variables). Авто-апдейты зависимостей — [renovate.json](./renovate.json)
по weekly schedule.

## Вклад в проект

См. [CONTRIBUTING.md](./CONTRIBUTING.md) — конвенции, процесс работы, CI/release pipeline.

## Changelog

История изменений по фазам — в [CHANGELOG.md](./CHANGELOG.md) (формат Keep a Changelog).

## Тестирование

См. [TESTING.md](./TESTING.md).

## Где смотреть, что реализовано

Подробная карта реализации с привязкой к разделам ТЗ, ссылками на ключевые файлы,
архитектурными решениями и неочевидностями — в [specs/IMPLEMENTATION.md](./specs/IMPLEMENTATION.md).
Этот документ создан специально для быстрого onboarding'а новых разработчиков
и агентов (включая Claude Code в будущих сессиях).

## Структура проекта

```
/cmd
  /receiver, /sender, /web, /loadtest
/internal
  /platform        # общая инфраструктура (config, logging, sentry, runner,
                   #   healthcheck, pg, redis, clickhouse, kafka, crypto,
                   #   ratelimit, circuitbreaker, bootstrap)
  /domain          # shared kernel (Node, User, Session, LogRecord, AuditEntry, APIToken)
  /receiver        # Receiver: handlers, usecase, gRPC client, NodeReader
  /sender          # Sender: gRPC server, Kafka consumer, HTTP-клиент, ClickHouse writer
  /web             # Web: handlers, usecase, repo, SPA static
/proto/sender/v1   # .proto + сгенерированные pb.go / pb_grpc.go
/migrations        # SQL-миграции (golang-migrate)
/config            # YAML
/deploy            # docker-compose, Dockerfile, prometheus.yml
/web-ui            # каркас фронта (см. README в каталоге)
```

## Лицензия

Внутренний проект Vozovoz.
