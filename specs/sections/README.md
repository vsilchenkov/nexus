# Шина данных — техническое задание (разделено по разделам)

Каждый раздел `data_bus_spec.md` вынесен в отдельный файл. Исходный сводный файл сохранён в [../data_bus_spec.md](../data_bus_spec.md).

## Оглавление

| № | Файл | О чём |
|---|---|---|
| 1 | [01-purpose.md](01-purpose.md) | Назначение |
| 2 | [02-architecture.md](02-architecture.md) | Архитектура верхнего уровня |
| 3 | [03-receiver.md](03-receiver.md) | Receiver Service — эндпоинты, семантика, конфиг узла, URL-режимы, авторизация, состояния |
| 4 | [04-sender.md](04-sender.md) | Sender Service — gRPC API, устройство, логирование в ClickHouse |
| 5 | [05-storage.md](05-storage.md) | Хранилища: PostgreSQL, ClickHouse, Kafka, Redis, шифрование |
| 6 | [06-metrics.md](06-metrics.md) | Метрики и наблюдаемость |
| 7 | [07-web-ui.md](07-web-ui.md) | Веб-интерфейс — экраны, аутентификация, audit log, API-токены |
| 8 | [08-config.md](08-config.md) | Конфигурация приложения — файлы, env, `config.yml` |
| 9 | [09-resilience.md](09-resilience.md) | Высоконагруженность и отказоустойчивость |
| 10 | [10-testing.md](10-testing.md) | Тестирование — уровни, сценарный тест |
| 11 | [11-swagger.md](11-swagger.md) | Swagger / OpenAPI |
| 12 | [12-repo-structure.md](12-repo-structure.md) | Структура репозитория |
| 13 | [13-build-run.md](13-build-run.md) | Сборка и запуск — Makefile, Docker Compose |
| 14 | [14-logging-sentry.md](14-logging-sentry.md) | Логирование и Sentry |
| 15 | [15-acceptance.md](15-acceptance.md) | Критерии приёмки |
| 16 | [16-out-of-scope.md](16-out-of-scope.md) | Out of scope в v1 / планы на v2 |
| 17 | [17-patterns.md](17-patterns.md) | Паттерны разработки (Clean Architecture, фронтенд) |

## Как пользоваться

- Открывайте конкретный раздел, чтобы не загружать весь файл.
- Внутри файлов сохранены оригинальные номера подпунктов (`### 3.1`, `### 7.4.1` и т.д.).
- Для общего обзора есть таблица выше.
- Исходный сводный документ `data_bus_spec.md` остаётся источником правды и обновляется при правках разделов (при изменении раздела не забывайте синхронизировать сводный файл, либо генерируйте его сборщиком).
