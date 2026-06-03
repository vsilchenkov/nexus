## 13. Сборка и запуск

### 13.1 Makefile

Корневой `Makefile` поддерживает запуск **и в Windows (с GNU Make для Windows или WSL), и в Linux/macOS**. Команды используют переменные `$(GOOS)` и `$(GOEXE)`, чтобы корректно собирать `.exe` под Windows.

Минимальный набор целей:

| Цель | Действие |
|---|---|
| `make build` | Сборка всех трёх бинарей под текущую ОС в `./bin/` |
| `make build-windows` | Кросс-сборка `.exe` под Windows из любой ОС |
| `make build-linux` | Кросс-сборка под Linux |
| `make run-receiver` | Локальный запуск Receiver с `config/config_debug.yml` |
| `make run-sender` | Локальный запуск Sender с `config/config_debug.yml` |
| `make run-web` | Локальный запуск Web с `config/config_debug.yml` |
| `make test` | Unit-тесты (см. §10.1) |
| `make test-coverage` | Unit-тесты + отчёт покрытия в `./coverage.html` |
| `make test-integration` | Integration-тесты через testcontainers, требует Docker (см. §10.1) |
| `make loadtest` | Сценарный тест производительности (см. §10.2). Принимает флаги: `make loadtest TARGET_RPS=500 DURATION=10m NODES=50` |
| `make lint` | `golangci-lint run ./...` |
| `make swagger` | Генерация swagger-документации (см. §11) |
| `make proto` | Генерация Go-кода из `.proto` (`protoc` + `protoc-gen-go-grpc`) |
| `make migrate-up` / `make migrate-down` | Применение миграций к локальной БД |
| `make docker-build` | Сборка всех Docker-образов |
| `make docker-up` | `docker compose up -d` со всеми зависимостями |
| `make docker-down` | Остановка стека |
| `make docker-logs` | `docker compose logs -f` |
| `make clean` | Удалить `./bin`, `./dist`, тестовые артефакты |

В Windows для `make` рекомендуется `chocolatey install make` или `scoop install make`. Альтернатива — поставить `mingw32-make`. README документирует оба варианта.

### 13.2 Docker Compose

`/deploy/docker-compose.yml` поднимает полный стек одной командой:

| Сервис | Назначение | Порты |
|---|---|---|
| `receiver` | Receiver Service | `8080` |
| `sender` | Sender Service | `9190` (gRPC) |
| `web` | Web UI + API | `8000` |
| `postgres` | Хранилище конфига (источник правды) | `5432` |
| `redis` | Кеш конфига, сессии, rate-limit, circuit-breaker | `6379` |
| `clickhouse` | Хранилище логов | `8123` HTTP, `9000` native |
| `kafka` | Брокер очередей (KRaft, без Zookeeper); single-broker для dev | `9092` |
| `prometheus` | Метрики | `9091` |
| `grafana` (опционально) | Дашборды | `3000` |

**Особенности compose-файла:**
- `depends_on` с `condition: service_healthy` — Receiver/Sender/Web стартуют только после готовности Postgres, Redis, ClickHouse и Kafka.
- Healthcheck на каждой зависимости (`pg_isready`, `redis-cli ping`, ClickHouse `SELECT 1`, Kafka `kafka-topics --list`).
- Restart policy `unless-stopped` для всех сервисов.
- Volume для Postgres, Redis (AOF persistence), ClickHouse и Kafka — данные переживают рестарт контейнеров.
- Сеть `nexus_net` для изоляции.
- Все секреты — через `.env` файл (`.env.example` в git, реальный `.env` в `.gitignore`). Compose автоматически подхватывает `.env` рядом с собой.

**Override для разработки** — `docker-compose.dev.yml`:
- Монтирует `./config/config_debug.yml` внутрь контейнеров.
- Открывает дополнительные порты для отладки (`6060` pprof).
- Запускает только инфраструктуру (Postgres, ClickHouse, Kafka), сами сервисы предполагаются запускаемыми локально через `make run-*`.

### 13.3 Локальный запуск без Docker

1. `make docker-up postgres redis clickhouse kafka` — поднять только зависимости.
2. `make migrate-up` — накатить миграции.
3. В трёх терминалах: `make run-receiver`, `make run-sender`, `make run-web`.

Все три бинаря возьмут `config/config_debug.yml`, который указывает на `localhost`.

### 13.4 Документация

В корне репозитория обязательно лежат два markdown-файла:

**`README.md`** — главный документ репозитория. Содержит:
- Краткое описание сервиса: что делает шина данных, какие задачи решает, схему верхнего уровня (ссылка на §2 ТЗ).
- Список зависимостей (PostgreSQL, Redis, ClickHouse, Kafka, Prometheus) с минимальными требованиями к версиям.
- Раздел **«Быстрый старт через Docker»** — `docker compose up`, открыть UI по `http://localhost:8000`, креды по умолчанию `admin/admin`.
- Раздел **«Запуск через Makefile»** — пошагово: `make docker-up postgres redis clickhouse kafka` → `make migrate-up` → `make run-receiver` / `make run-sender` / `make run-web` в трёх терминалах. Под Windows — отдельный подраздел с установкой GNU Make (chocolatey / scoop / mingw32-make).
- Раздел **«Ручной запуск без Makefile»** — для случаев, когда нет ни Docker, ни Make: установить зависимости локально (`brew install postgresql redis clickhouse kafka` и аналоги для других ОС), накатить миграции вручную через `migrate -path migrations -database "..." up`, собрать бинари через `go build -o bin/receiver ./cmd/receiver`, запустить с флагом `--config ./config/config_debug.yml`.
- Раздел **«Конфигурация»** — куда смотреть (`config/config.example.yml`), как переопределять (env, флаг `--config`), список основных env-переменных из `.env.example`.
- Раздел **«Веб-интерфейс»** — основные экраны, ссылки на скриншоты (если есть), как создать первый узел.
- Раздел **«API»** — ссылка на Swagger UI (`http://localhost:8080/swagger/`, `http://localhost:8000/swagger/`).
- Раздел **«Тестирование»** — краткое описание уровней тестов + **ссылка на TESTING.md** для подробностей.
- Раздел **«Метрики и логи»** — где смотреть Prometheus, как найти запрос в ClickHouse-логах, как настроить Sentry.
- Раздел **«Структура проекта»** — краткое описание дерева каталогов (ссылка на §12 ТЗ).
- Контакты / лицензия / контрибьютинг.

**`TESTING.md`** — отдельный документ про тесты, чтобы не раздувать README. Содержит:
- Раздел **«Unit-тесты»** — как запускать (`make test`), какие пакеты покрыты, как добавить тест, требования к покрытию (≥70%), как смотреть отчёт (`make test-coverage` → `coverage.html`).
- Раздел **«Integration-тесты»** — что они покрывают, какие зависимости поднимают через testcontainers, как запускать (`make test-integration`), как отладить упавший тест (вытащить логи контейнера, оставить контейнеры запущенными через флаг).
- Раздел **«Сценарный тест производительности»** — детальное описание `make loadtest`:
  - Что тестируется (см. §10.2).
  - Все доступные флаги/env с примерами.
  - Примеры команд для типичных сценариев: «быстрый smoke на 100 rps × 1 минута», «полный 500 rps × 10 минут перед релизом», «стресс-тест на 1000 rps для определения потолка».
  - Как читать итоговый отчёт `report.json`.
  - Критерии «прошёл/не прошёл».
  - Как воспроизвести проблему локально, если тест упал в CI.
- Раздел **«Тестирование в CI/CD»** — какие тесты гоняются на каждом PR, какие nightly, какие перед релизом.
- Раздел **«Отладка тестов»** — частые проблемы (порт занят, Docker не отвечает, testcontainers timeout) и решения.

В README.md в разделе «Тестирование» — явная ссылка `См. [TESTING.md](./TESTING.md) для подробной инструкции по запуску тестов и нагрузочного сценария`.

