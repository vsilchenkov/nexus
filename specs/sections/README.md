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
| 28 | [28-online-metrics.md](28-online-metrics.md) | Онлайн-метрики и UX — публичный адрес приложения, онлайн-обновление метрик, период просмотра (1h..30d+календарь), маскирование данных авторизации, понятные ошибки валидации, фильтр RabbitMQAsync, багфиксы стенда (CH-таблица, резолв async) |
| 29 | [29-node-comment.md](29-node-comment.md) | Комментарий узла — текстовое описание для команды (поле `comment`, форма/обзор узла), UI-метаданные вне маршрутизации |
| 30 | [30-logging-panic-recovery.md](30-logging-panic-recovery.md) | Логирование, обработка паник и идентификация запросов — `safego.Recover` во всех горутинах, кастомный gin-recovery (500 + лог + Sentry), сквозной `request_id` (UUID v4, `X-Request-Id`) в логах/Sentry, версия приложения в футере SPA (`GET /api/version`), bump логгера до v1.7.9 (`WithContext`) |
| 31 | [31-kafka-monitoring.md](31-kafka-monitoring.md) | Мониторинг Kafka — admin-only alarm-dashboard `/kafka` (в блоке Аудита, не в Настройках): health-banner, KPI, throughput/lag графики (recharts), таблица топиков, top-узлы, брокеры. API `/api/kafka/{overview,timeseries,topics,by-node,test}` из Prometheus (`method="requestAsync"`) + Kafka Admin (`segmentio/kafka-go`, кеш Redis 30с), мягкая деградация, rate-limit 60/мин |
| 32 | [32-loop-protection.md](32-loop-protection.md) | Защита от зацикливания — служебный hop-счётчик `X-Nexus-Hops` (инкремент на каждом проходе через шину, обрыв при `receiver.max_hops`, дефолт 5 → **508 Loop Detected**, sync+async), self-reference валидация `target_url` при сохранении узла (`web.self_ingress_hosts`), метрика `nexus_loop_detected_total` |
| 33 | [33-chart-tooltips.md](33-chart-tooltips.md) | Доработка тултипов графиков — единый презентационный `<ChartTooltip>` (период → главное значение → серии с маркерами → подвал/дельта → действие) на recharts + Radix **без новых зависимостей**, интеграция во все графики (TrafficChart, Throughput/Lag Kafka, MiniSpark, Sparkline Overview), action «открыть логи за момент», i18n; partition-lag и «неделю назад» — out-of-scope (нет данных) |
| 34 | [34-ops-session-version-async-queue.md](34-ops-session-version-async-queue.md) | Операбельность — «Настройки» вниз сайдбара; настраиваемая длительность сессии (app_settings + динамический провайдер TTL + hot-reload, валидация 5мин..30сут); обогащённый `GET /api/version` (commit/build_date) + dev-only override версии (гейт `web.allow_version_override`, прод запрещён); управление async-очередью Kafka на узле requestAsync (глубина/список 50+тело peek'ом, удаление одного/период/всё через Redis-tombstones `qcancel:<id>`, проверка в Sender, `/api/nodes/:id/async-queue/*`); фикс replay 405 (слать `IncomingMethod`, не залогированный `OutgoingMethod`) |
| 35 | [35-queue-tab-rework.md](35-queue-tab-rework.md) | Переработка вкладки «Очередь» (перерабатывает §34.4/§34.6) — реальная модель потока (enabled+мёртвый адрес → DLQ, не живая очередь); «неудачные доставки» из ClickHouse-логов (`done=0`, `CountFailed` + `/logs/failed-count`, переиспользование списка/тела/replay) вместо дорогого Kafka-DLQ-peek (удалён); честная семантика (живая очередь — tombstone-purge admin; неудачи — фильтр+пауза+replay, без фейк-очистки); `PATCH /nodes/:id/status` (пауза/отключение, manager+); RBAC-фикс (failed-view viewer+, управление очередью admin); KPI-шапка + две секции |
| 36 | [36-dlq-reprocessor.md](36-dlq-reprocessor.md) | Авто-репроцессор DLQ (развивает §34.4/§35) — фоновый периодический sweeper в Sender повторно доставляет неудачные async-сообщения из `nexus.async.dlq` до per-node TTL (`dlq_ttl_seconds`, дефолт 24ч); уважает circuit breaker, tombstones, статус узла; republish-в-хвост, backoff = интервал прохода; финальный лог `ttl_expired`; глобальные флаги `reprocess_enabled/interval/max_scan`; UI-поле TTL + подсказка; метрики |
| 37 | [37-node-id-in-logs.md](37-node-id-in-logs.md) | Идентификатор узла в логах — колонка `node_id` (UUID) в CH-таблице: per-node атрибуция для узлов, делящих одну таблицу (счётчики/графики/очистка/replay фильтруют по узлу, `DeleteFailed` не задевает чужие записи). Sender пишет node_id (sync через gRPC, async/DLQ локально); миграция существующих таблиц (`ALTER ADD COLUMN` в Web+Sender); read-фильтр `(node_id=? OR node_id='')` (legacy-совместимость); node id в UI «Конфиг» |
| 38 | [38-clog-retry-kafka.md](38-clog-retry-kafka.md) | Durable-retry проваленных CH-батчей через Kafka (замена NDJSON-fallback) — при недоступности ClickHouse Sender продьюсит проваленный батч в топик `nexus.logs.retry`, отдельная группа `<group>-clog-retry` дренит его обратно в CH (retry-in-place с бэкоффом до восстановления). Реплицируемо, без локальных файлов; Kafka нагружается только во время простоя CH (сообщение на батч, не на запрос). Удалён `clickhouse.fallback_dir`, добавлен `kafka.retry_topic`. Отправка sync/async не блокируется, ошибки CH по-прежнему в Sentry |
| 39 | [39-path-passthrough.md](39-path-passthrough.md) | Path-passthrough — опциональный флаг узла `path_passthrough`: хвост входящего пути после пути узла приклеивается к `target_url` (longest-prefix-резолв + `JoinPath`, traversal-safe; оба URL-режима; sync/async/callback). По умолчанию off (точный матч сохранён). Лог-колонки разведены: `http_method` (глагол, новая, после `type`) и `method` (репурпозен → подпуть запроса, напр. `v1/GetParcelsInfo`); проброс `request_path` через gRPC/envelope, идемпотентная миграция CH-таблиц в Web+Sender. Replay passthrough-записи по полному подпути. UI: toggle в форме + колонки в логах |
| 40 | [40-any-http-method.md](40-any-http-method.md) | HTTP-метод «Любой» (`ANY`) для входящего и исходящего метода узла. Вх=ANY — узел принимает запрос любым методом (без 405). Исх=ANY — Sender зеркалит метод входящего запроса (PUT→PUT); pull-узлы → POST. Резолв `effectiveOutgoingMethod` в Receiver; replay ANY-узла по залогированному `http_method`. Миграция 0020 (CHECK +ANY), DTO `oneof …ANY`, UI-пункт «Любой». По умолчанию ничего не меняется |
| 41 | [41-universal-request-auth.md](41-universal-request-auth.md) | Универсальная динамическая авторизация — источник (заголовок/параметр) + имя поля для **входящей** (token/basic, колонки `incoming_auth_dynamic_*`, миграция 0021) и **исходящей** (`*_from_request`) авторизации; новый каталог «полей запроса» (`request_fields_catalog`, миграция 0022, `/api/request-fields`) с обязательным выбором поля. Умный дедуп схемы (`Bearer <jwt>` не удваивается; кейс `?Bearer=Bearer+<jwt>`); пустое поле → исход без `Authorization` (не 401), вход → 401. Пересмотр статуса **«Down»** в Overview: по исходу ПОСЛЕДНЕГО вызова (gauge `nexus_node_last_request_error`), а не доле ошибок за период |
| 42 | [42-log-body-streaming.md](42-log-body-streaming.md) | Динамическая подгрузка тел логов — фикс зависания вкладки «Логи» на большом теле. `Get` отдаёт **превью** (первые 64K рун, нарезка `substringUTF8` в CH) + полные длины `request_len`/`response_len`; остаток — срезами по рунам через `GET …/log/{logId}/body` (`which/offset/limit`, `eof`); очень большое — потоковым скачиванием `…/body/download` (`text/plain` attachment). Порт `LogReader.GetByIDPreview`/`GetBodyChunk` (`which` whitelist), `GetByID` сохранён для replay. UI: «показать весь» (с предупреждением >2 МБ) / «скачать» / «копировать»; `prettyMaybe` не парсит тела >256K (убирает синхронный фриз). Без миграций/ENV |

## Как пользоваться

- Открывайте конкретный раздел, чтобы не загружать весь файл.
- Внутри файлов сохранены оригинальные номера подпунктов (`### 3.1`, `### 7.4.1` и т.д.).
- Для общего обзора есть таблица выше.
- Исходный сводный документ `nexus_spec.md` остаётся источником правды и обновляется при правках разделов (при изменении раздела не забывайте синхронизировать сводный файл, либо генерируйте его сборщиком).

## Что уже реализовано

Карта реализации по разделам ТЗ — в [../IMPLEMENTATION.md](../IMPLEMENTATION.md).
Там же — ссылки на ключевые файлы кода, архитектурные решения и список «куда копать дальше».
