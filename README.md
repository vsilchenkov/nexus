# DataBus

Шина данных — три Go-сервиса (Receiver, Sender, Web), которые принимают входящие HTTP-запросы, маршрутизируют их на сконфигурированные внешние узлы и логируют все вызовы. Конфигурация маршрутов хранится в PostgreSQL, редактируется через веб-UI. Полное ТЗ — [data_bus_spec.md](./data_bus_spec.md).

> **Статус: Phase 0 (фундамент).** Поднимается весь docker-стек, три сервиса отвечают на `/health` и `/ready`, миграции применяются автоматически. Бизнес-логика (приём запросов, gRPC, ClickHouse-логи, UI) — Phase 1+.

## Зависимости (минимальные версии)

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
cp .env.example .env       # отредактируйте пароли
cp config/config.example.yml config/config.yml
docker compose -f deploy/docker-compose.yml up -d
curl http://localhost:8080/health   # receiver
curl http://localhost:8000/health   # web
```

Веб-UI (Phase 3) — `http://localhost:8000`, креды по умолчанию `admin/admin`.

## Запуск через Makefile (локальная разработка)

Поднимаем только инфраструктуру в Docker; сервисы запускаем локально через `go run`:

```bash
make docker-up-dev          # postgres + redis + clickhouse + kafka + prometheus
make migrate-up             # накатить миграции
make run-receiver           # в одном терминале
make run-sender             # во втором
make run-web                # в третьем
```

### Windows

`make` ставится одним из:
- `choco install make` (Chocolatey)
- `scoop install make`
- `mingw32-make` (входит в MSYS2 / TDM-GCC)

Каждый бинарь дополнительно можно установить как Windows-сервис:

```cmd
bin\receiver.exe install
sc start DataBusReceiverService
```

## Конфигурация

- `config/config.yml` — основной конфиг, **не коммитится в git**. Шаблон — `config/config.example.yml`.
- `config/config_debug.yml` — конфиг для локальной разработки (адреса `localhost`, Sentry off).
- `.env` — секреты (пароли, токены), **не коммитится**. Шаблон — `.env.example`.
- Подстановка `${VAR}` / `${VAR:default}` поддерживается в YAML.

Выбор конфига (по приоритету):
1. Флаг `--config /path/to/file.yml` (или `-c`)
2. Переменная окружения `DATABUS_CONFIG`
3. Флаг `--debug` → `./config/config_debug.yml`
4. Дефолт: `./config/config.yml`

## API

- Receiver — HTTP `:8080`. В Phase 0 доступен только `/health`, `/ready`, `/metrics`.
- Sender — gRPC `:9090` (healthcheck сервис), admin HTTP `:9091` для `/health`, `/ready`, `/metrics`.
- Web — HTTP `:8000`. В Phase 0 доступен только `/health`, `/ready`, `/metrics`.

Swagger UI (Phase 1+) — `http://localhost:8080/swagger/`, `http://localhost:8000/swagger/`.

## Тестирование

См. [TESTING.md](./TESTING.md) для подробной инструкции.

## Структура проекта

```
/cmd               # точки входа (receiver, sender, web)
/internal
  /platform        # общие пакеты (config, logging, sentry, runner, healthcheck, pg, redis, clickhouse, kafka, bootstrap)
  /receiver        # бизнес-логика Receiver
  /sender          # бизнес-логика Sender
  /web             # бизнес-логика Web
/migrations        # SQL-миграции (golang-migrate)
/config            # YAML-конфиги
/deploy            # docker-compose, Dockerfile, prometheus.yml
/web-ui            # SPA (Phase 3)
```

## Лицензия

Внутренний проект Vozovoz.
