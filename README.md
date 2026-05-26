# DataBus

[![CI](https://github.com/vsilchenkov/databus/actions/workflows/ci.yml/badge.svg)](.github/workflows/ci.yml)
[![Security](https://github.com/vsilchenkov/databus/actions/workflows/security.yml/badge.svg)](.github/workflows/security.yml)
[![Release](https://github.com/vsilchenkov/databus/actions/workflows/release.yml/badge.svg)](.github/workflows/release.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](go.mod)

Шина данных — три Go-сервиса (Receiver, Sender, Web), которые принимают входящие HTTP-запросы, маршрутизируют их на сконфигурированные внешние узлы и логируют все вызовы. Конфигурация маршрутов хранится в PostgreSQL, редактируется через REST API и SPA (React 18 + Vite + Tailwind). Полное ТЗ — [specs/data_bus_spec.md](./specs/data_bus_spec.md); разделено по разделам в [specs/sections/](./specs/sections/).

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
| Phase 7  | Swagger 100% endpoints + UI; L2 LRU кеш узлов; integration suite (Redis+CH через testcontainers); GitHub Actions CI; Grafana dashboard + Prometheus alerts; **GoReleaser релизы + multi-arch docker в GHCR; security scanning workflow** |
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

Релизные multi-arch образы (amd64+arm64) публикуются в registry по тегу `v*`.
Один [.goreleaser.yaml](./.goreleaser.yaml) работает в обоих CI:

- **GitHub Actions** ([release.yml](./.github/workflows/release.yml)) →
  `ghcr.io/<owner>/<repo>/{receiver,sender,web}:<version>`
- **GitLab CI** ([release job в .gitlab-ci.yml](./.gitlab-ci.yml)) →
  `$CI_REGISTRY_IMAGE/{receiver,sender,web}:<version>` (GitLab Container Registry)

Различия параметризованы через env-переменные `DOCKER_REGISTRY_BASE` и
`COMPARE_URL_BASE`, которые задаёт каждый CI. GoReleaser автодетектит платформу
по наличию `GITHUB_TOKEN` или `GITLAB_TOKEN`.

Локальный snapshot для проверки релизной сборки — `make release-snapshot`
(артефакты в `dist/`).

## CI/CD

Pipeline'ы покрывают unit-тесты, lint, build, security и release. Поддерживаются
обе платформы:

| Что                            | GitHub Actions                                                 | GitLab CI                                          |
|--------------------------------|----------------------------------------------------------------|----------------------------------------------------|
| Unit + lint + build + swagger  | [ci.yml](./.github/workflows/ci.yml)                           | [.gitlab-ci.yml](./.gitlab-ci.yml) stages test/lint/build |
| Integration (testcontainers)   | `ci.yml` (label `run-integration`)                             | `.gitlab-ci.yml` job `integration`                 |
| Security (govulncheck/gosec/trivy/nancy) | [security.yml](./.github/workflows/security.yml)     | `.gitlab-ci.yml` security stage                    |
| Release (GoReleaser)           | [release.yml](./.github/workflows/release.yml)                 | `.gitlab-ci.yml` release stage                     |
| Auto-updates deps              | [dependabot.yml](./.github/dependabot.yml)                     | [renovate.json](./renovate.json) + renovate job    |

GitLab CI запускается на self-hosted runner с тегом `srv-d-android-l` —
смотри шапку [.gitlab-ci.yml](./.gitlab-ci.yml) для required CI/CD Variables.

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
