# Nexus

[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](go.mod)

Шина данных — три Go-сервиса (Receiver, Sender, Web), которые принимают входящие HTTP-запросы, маршрутизируют их на сконфигурированные внешние узлы и логируют все вызовы. Конфигурация маршрутов хранится в PostgreSQL, редактируется через REST API и SPA (React 18 + Vite + Tailwind). Полное ТЗ — [specs/nexus_spec.md](./specs/nexus_spec.md); разделено по разделам в [specs/sections/](./specs/sections/).

## Зависимости

| Компонент    | Версия |
|--------------|--------|
| Go           | 1.26   |
| PostgreSQL   | 16     |
| Redis        | 7      |
| ClickHouse   | 24     |
| Kafka        | 3.9 (KRaft) |
| Prometheus   | 2.55   |

## Установка, запуск и отладка

- **Развёртывание в продакшене** — [DEPLOYMENT.md](./DEPLOYMENT.md): установка на чистом
  Linux-сервере, запуск полностью в Docker и с внешними сервисами
  (PostgreSQL/ClickHouse/Kafka/Redis), где задавать адреса/логины/пароли, обновление,
  смена версии и откат, запуск под Docker на Windows.
- **Локальная разработка и отладка в VS Code (Windows)** — [DEVELOPMENT.md](./DEVELOPMENT.md):
  порядок первого запуска, миграции, варианты запуска под отладчиком, запуск всех трёх
  сервисов одной кнопкой.

Самый короткий путь (полностью в Docker):

```bash
cp .env.example .env       # отредактируйте пароли + ENCRYPTION_KEY (см. DEPLOYMENT.md §2)
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml run --rm web --set-admin-password 'mySecretPass'
curl http://localhost:8000/health
```

UI на `http://localhost:8000/`. Swagger UI — `http://localhost:8000/swagger/index.html`.

## Создание первого узла и тестовый запрос

```bash
# из под admin-сессии (cookie nexus_session):
curl -X POST http://localhost:8000/api/nodes -b cookies.txt \
  -H "Content-Type: application/json" \
  -d '{
    "path": "test/echo",
    "root_method": "request",
    "target_url": "https://httpbin.org/anything",
    "auth_type": "none",
    "incoming_auth_type": "none",
    "clickhouse_table": "nexus_default.test_echo"
  }'

# создать таблицу в ClickHouse (схема из §4.3 ТЗ — TODO Phase 3 UI помощник)

# отправить запрос через шину (единый вход через Web :8000; напрямую в Receiver :8080 тоже работает):
curl -X POST http://localhost:8000/api/v1/request/test/echo \
  -H "Content-Type: application/json" \
  -d '{"hello":"world"}'
```

## Запуск для разработки

- Локальный запуск под отладчиком VS Code (Windows) с зависимостями в Docker Desktop —
  [DEVELOPMENT.md](./DEVELOPMENT.md).
- Запуск без VS Code: `make docker-up-dev` (поднять зависимости) → `make migrate-up` →
  `make set-admin-password PASSWORD=...` → `make run-receiver` / `run-sender` / `run-web`
  (каждый в своём терминале). Полный список целей — `make help`.

## Конфигурация

- `config/config.yml` — production, в git не коммитится. Шаблон — `config/config.example.yml`.
- `config/config_debug.yml` — localhost-адреса для `make run-*`.
- `.env` — секреты, в git не коммитится.

Выбор конфига по приоритету: `--config` → `$NEXUS_CONFIG` → `--debug` → `config/config.yml`.
Полная карта переменных `.env` (адреса/логины/пароли хранилищ, `ENCRYPTION_KEY`) — в
[DEPLOYMENT.md](./DEPLOYMENT.md) §2.

## API

- Receiver `:8080` — `/api/v1/request/*`, `/api/v1/requestAsync/*`, `/api/v1/callback/*`, `/health`, `/ready`, `/metrics`.
  Боевой трафик идёт через единый вход Web (`:8000`, те же `/api/v1/*` проксируются в Receiver).
- Sender `:9190` (gRPC SenderService) + admin `:9091` (`/health`, `/ready`, `/metrics`).
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
