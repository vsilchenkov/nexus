# Шина данных — техническое задание (разделено по разделам)

Каждый раздел `nexus_spec.md` вынесен в отдельный файл. Исходный сводный файл сохранён в [../nexus_spec.md](../nexus_spec.md).

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
| 18 | [18-multi-tenancy.md](18-multi-tenancy.md) | Multi-tenancy v2 — команды, изоляция, CH-БД per team, перенос узлов |
| 19 | [19-ch-templates.md](19-ch-templates.md) | Шаблоны запросов ClickHouse — каталог DDL, CODEC/индексы/TTL, авто-создание таблицы узла |
| 20 | [20-notifications.md](20-notifications.md) | Уведомления операторам в Telegram — cron-расписание, ошибки узлов, тестовая отправка |
| 21 | [21-ui-redesign.md](21-ui-redesign.md) | Редизайн UI под эталон (дизайн-токены, UI-kit, app-shell) + HTTP-API метрик панели (Prometheus + ClickHouse) |
| 22 | [22-logging-controls-cards.md](22-logging-controls-cards.md) | Контроль логирования узла (тумблер, обрезка тел), раскладка карточками Overview, перевод Telegram-алертов на Prometheus |
| 23 | [23-allowed-hosts-catalog.md](23-allowed-hosts-catalog.md) | Каталог разрешённых хостов (SSRF) — общий справочник exact/wildcard/regex, привязка к узлам, preview, denорм-снимок |
| 24 | [24-headers-catalog.md](24-headers-catalog.md) | Справочник HTTP-заголовков — combobox с автодополнением и автосозданием, usage_count on-read |
| 25 | [25-topbar-swagger.md](25-topbar-swagger.md) | Swagger в шапке — два дока (Receiver + Web), popover, раздача обоих Web-бинарём |
| 26 | [26-roles-access-control.md](26-roles-access-control.md) | RBAC — три роли (Admin/Manager/Viewer), иерархия рангов, матрица доступа, self-service смена своего пароля |
| 27 | [27-rabbitmq-async.md](27-rabbitmq-async.md) | Тип узла RabbitMQAsync — Puller-воркер RabbitMQ→Kafka, поля `rmq_*`/`pull_*`, runtime-`degraded`, `POST /api/nodes/test-rmq`, метрики, UI, сценарные тесты |

## Как пользоваться

- Открывайте конкретный раздел, чтобы не загружать весь файл.
- Внутри файлов сохранены оригинальные номера подпунктов (`### 3.1`, `### 7.4.1` и т.д.).
- Для общего обзора есть таблица выше.
- Исходный сводный документ `nexus_spec.md` остаётся источником правды и обновляется при правках разделов (при изменении раздела не забывайте синхронизировать сводный файл, либо генерируйте его сборщиком).

## Что уже реализовано

Карта реализации по разделам ТЗ — в [../IMPLEMENTATION.md](../IMPLEMENTATION.md).
Там же — ссылки на ключевые файлы кода, архитектурные решения и список «куда копать дальше».
