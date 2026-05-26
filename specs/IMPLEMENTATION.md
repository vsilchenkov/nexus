# IMPLEMENTATION.md — карта проделанных работ по ТЗ DataBus

> **Назначение этого файла.** Карта реализации шины данных в привязке к разделам ТЗ
> ([data_bus_spec.md](data_bus_spec.md), разделённый на [sections/](sections/)).
> Помогает новому агенту/разработчику быстро понять: что уже сделано, где это лежит,
> по какой схеме построено и куда копать дальше.
>
> Для глубокого понимания читать в порядке: [CLAUDE.md](../CLAUDE.md) → этот файл →
> [README.md](../README.md) → [TESTING.md](../TESTING.md) → [sections/17-patterns.md](sections/17-patterns.md).

---

## 1. Краткая сводка

| Параметр                | Значение                                                              |
|-------------------------|-----------------------------------------------------------------------|
| Go-версия               | 1.25                                                                  |
| Тип проекта             | три stateless backend-сервиса (Receiver, Sender, Web) + SPA админка   |
| Архитектура             | Clean Architecture: `handler → usecase → port → adapter`              |
| Главный поток           | `POST /v1/request/{path}` → Receiver → gRPC Sender → внешний URL → лог в ClickHouse |
| Async                   | `POST /v1/requestAsync/{path}` → Receiver → Kafka → Sender-consumer   |
| Зависимости              | PostgreSQL 16, Redis 7, ClickHouse 24, Kafka 3.7 (KRaft), Prometheus  |
| Покрытие unit-тестами   | 8 пакетов (domain, crypto, i18n, sentry, receiver/usecase, chlog, web/usecase, metrics) |
| SPA-фронт               | React 18 + Vite + TS + Tailwind + TanStack Query + react-i18next, 6 страниц |
| Бинари в `cmd/`         | `receiver`, `sender`, `web`, `loadtest`, `rotate-key`                 |

---

## 2. Карта реализации по разделам ТЗ

Колонка «Статус»: ✅ полностью, ◐ частично, ⛔ не реализовано (out-of-scope).
Колонка «Ключевые файлы» — самые показательные точки кода.

### §1–§2 Назначение и архитектура

| Пункт | Статус | Ключевые файлы |
|---|---|---|
| Три независимых stateless-сервиса | ✅ | [cmd/receiver](../cmd/receiver/), [cmd/sender](../cmd/sender/), [cmd/web](../cmd/web/) |
| docker-compose одной командой | ✅ | [deploy/docker-compose.yml](../deploy/docker-compose.yml) |
| Локальный запуск без Docker | ✅ | `make run-receiver` / `run-sender` / `run-web` |

### §3 Receiver Service

| Пункт | Статус | Где |
|---|---|---|
| `/v1/request/*` sync с проксированием ответа | ✅ | [internal/receiver/usecase/route.go](../internal/receiver/usecase/route.go), [adapter/in/http/handler.go](../internal/receiver/adapter/in/http/handler.go) |
| `/v1/requestAsync/*` async, ответ 200 сразу | ✅ | [route_async.go](../internal/receiver/usecase/route_async.go) |
| 404 без префикса `/v1/` с подсказкой | ✅ | `Handler.Register` → `r.NoRoute` |
| `url_mode = static` / `from_request` + allowlist + wildcard (`*.partner.com`) | ✅ | [urlresolver.go](../internal/receiver/usecase/urlresolver.go) |
| `url_base` исключается из проксируемой query | ✅ | `ResolveURL`: `clean.Del(param)` |
| Все режимы incoming auth (none/basic/token) | ✅ | [auth.go](../internal/receiver/usecase/auth.go) `CheckIncomingAuth` |
| Все режимы outgoing auth (none/basic/token/token_from_request/basic_from_request) | ✅ | [auth_dynamic.go](../internal/receiver/usecase/auth_dynamic.go) `BuildDynamicOutgoingAuth` |
| Исключение служебных значений из проксируемого запроса (§3.5 «Исключение») | ✅ | `buildTokenFromRequest`, `buildBasicFromRequest` |
| Маскирование `***` в логах и Sentry | ✅ | `maskAuthHeader` (dry-run), [sentry/sentry.go](../internal/platform/sentry/sentry.go) `isSensitive` |
| Статусы узла: `enabled` / `disabled` / `paused` | ✅ | `RouteUsecase.Route` (§3.6) |
| **paused в sync** → 202 + `queued:true` + `node_status:paused` | ✅ Phase 5 | `Route()` возвращает `ErrNodePaused` → `handleSync` переключается на `handleAsyncFromInput` |
| Лимиты полей (path 1-255, timeout 100-300000 ms, ...) | ✅ | [domain/node.go](../internal/domain/node.go) `Validate()` + DB-constraints в [migrations/0002](../migrations/0002_nodes_methods_users.up.sql) |
| Soft/hard лимит узлов | ✅ | `NodeUsecase.Create` — `nodesHardLimit` → `ErrLimitReached` |

### §4 Sender Service

| Пункт | Статус | Где |
|---|---|---|
| gRPC `SenderService.Send` | ✅ | [proto/sender/v1/sender.proto](../proto/sender/v1/sender.proto), [adapter/in/grpc/sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) |
| HTTP-клиент с keep-alive + retry/backoff | ✅ | [adapter/out/httpclient/client.go](../internal/sender/adapter/out/httpclient/client.go) |
| Kafka-consumer + DLQ + paused-pacing | ✅ | [adapter/in/kafka/consumer.go](../internal/sender/adapter/in/kafka/consumer.go), [usecase/async.go](../internal/sender/usecase/async.go) |
| Offset коммитится только после успешной доставки | ✅ | `enable_auto_commit: false` + `MarkMessage` after deliver |
| ClickHouse batch writer | ✅ | [adapter/out/chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go) |
| **NDJSON file-fallback при недоступности CH** | ✅ Phase 5.1 | [chlog/fallback.go](../internal/sender/adapter/out/chlog/fallback.go) |
| **CH partition-drop housekeeping (§4.3)** | ✅ Phase 5 | [sender/usecase/ch_housekeeping.go](../internal/sender/usecase/ch_housekeeping.go), миграция [0005](../migrations/0005_node_retention.up.sql) |
| Все 20 полей `LogRecord` (включая `attempts_details`) | ✅ | [domain/log.go](../internal/domain/log.go), `INSERT` в `writer.go` |

### §5 Хранилища

| Пункт | Статус | Где |
|---|---|---|
| Таблицы PG: methods, nodes, node_headers, users, user_audit, api_tokens | ✅ | [migrations/0001-0005](../migrations/) |
| `golang-migrate` advisory lock, auto-migrate на старте | ✅ | [platform/pg/migrate.go](../internal/platform/pg/migrate.go) `NewMigrator` |
| `team_id` колонки с DEFAULT 'default' (закладка multi-tenancy v2) | ✅ | миграция 0002 |
| Up/Down + `make migrate-up`/`-down N=1`/`-status` | ✅ | [Makefile](../Makefile) |
| ClickHouse driver `clickhouse-go/v2`, batch INSERT | ✅ | [platform/clickhouse/clickhouse.go](../internal/platform/clickhouse/clickhouse.go) |
| Kafka admin + producer + consumer (`segmentio/kafka-go`) с автосозданием топиков с retention 30 дней, acks=all, idempotence | ✅ | [platform/kafka/](../internal/platform/kafka/) |
| Redis: NodeCache (TTL 5min), sessions (24h), ratelimit, circuit-breaker | ✅ | [platform/redis/](../internal/platform/redis/), [circuitbreaker/redis.go](../internal/platform/circuitbreaker/redis.go), [ratelimit/redis.go](../internal/platform/ratelimit/redis.go) |
| AES-256-GCM v1-формат `v1:nonce:ct:tag` | ✅ | [platform/crypto/aesgcm.go](../internal/platform/crypto/aesgcm.go) |
| Валидация ENCRYPTION_KEY на старте, exit 1 при невалидном | ✅ | [bootstrap.go](../internal/platform/bootstrap/bootstrap.go) `MustCipher` |
| **`rotate-encryption-key` утилита (идемпотентная)** | ✅ Phase 5 | [cmd/rotate-key/main.go](../cmd/rotate-key/main.go), `make rotate-encryption-key OLD_KEY=… NEW_KEY=…` |

### §6 Метрики

| Пункт | Статус | Где |
|---|---|---|
| `/metrics` Prometheus в каждом сервисе | ✅ | [platform/metrics/metrics.go](../internal/platform/metrics/metrics.go) — изолированный `*prometheus.Registry`, подключается через `a.metrics.Handler()` в каждом `app.go` |
| **`databus_requests_total` + `databus_request_duration_seconds`** | ✅ Phase 6.1 | Gin middleware [platform/metrics/gin.go](../internal/platform/metrics/gin.go) — Receiver/Web; gRPC [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) и async [usecase/async.go](../internal/sender/usecase/async.go) — Sender |
| **`databus_kafka_lag`** | ✅ Phase 6.1 | reporter в [sender/app.go](../internal/sender/app.go) `reportKafkaLag()` — раз в 15 сек снимает `Stats()` со всех consumer-инстансов |
| **`databus_clickhouse_buffer_size` / `_errors_total` / `_dropped_total` / `_fallback_total`** | ✅ Phase 6.1 | [chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go) обновляет в `append`/`flushTable`/`Write` |

### §7 Веб-интерфейс

| Пункт | Статус | Где |
|---|---|---|
| Login form (cookie databus_session) | ✅ | [web-ui/src/pages/Login.tsx](../web-ui/src/pages/Login.tsx) |
| Overview (список узлов) | ✅ | [web-ui/src/pages/Overview.tsx](../web-ui/src/pages/Overview.tsx) |
| Node detail с вкладкой Logs (snapshot + SSE live-tail) | ✅ Phase 5 | [pages/NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) |
| Node settings (создание/редактирование, dry-run кнопка) | ✅ Phase 5.1 | [pages/NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) |
| **POST /api/nodes/dry-run** (§7.5.1) с пошаговым отчётом | ✅ Phase 5 | [web/usecase/dry_run.go](../internal/web/usecase/dry_run.go), [http/dry_run_handler.go](../internal/web/adapter/in/http/dry_run_handler.go), UI: [components/DryRunDialog.tsx](../web-ui/src/components/DryRunDialog.tsx) |
| **POST /api/logs/{id}/replay** (§7.4.1) + маркер `__replay_of` + rate-limit 10/мин | ✅ Phase 5 | [usecase/replay.go](../internal/web/usecase/replay.go), [http/replay_handler.go](../internal/web/adapter/in/http/replay_handler.go), [adapter/out/receiver/dispatcher.go](../internal/web/adapter/out/receiver/dispatcher.go), UI: [components/ReplayDialog.tsx](../web-ui/src/components/ReplayDialog.tsx) |
| **SSE live-tail `/api/nodes/{id}/logs/stream`** (§7.4) с heartbeat | ✅ Phase 5 | [usecase/logs.go](../internal/web/usecase/logs.go) `Subscribe`, [http/logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `Stream` |
| Settings → API Tokens | ✅ Phase 5.1 | [pages/settings/ApiTokens.tsx](../web-ui/src/pages/settings/ApiTokens.tsx) |
| Settings → Language / Theme | ✅ Phase 5.1 | [pages/settings/Language.tsx](../web-ui/src/pages/settings/Language.tsx), [Theme.tsx](../web-ui/src/pages/settings/Theme.tsx) |
| Audit log страница | ✅ Phase 5.1 | [pages/AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx) |
| **i18n / Accept-Language (en/ru)** на стороне backend и SPA | ✅ Phase 5 | [platform/i18n/](../internal/platform/i18n/), [web-ui/src/locales/](../web-ui/src/locales/), [web-ui/src/i18n.ts](../web-ui/src/i18n.ts) |
| Auth: users CRUD, sessions Redis, RBAC, must_change_password | ✅ | [web/usecase/auth.go](../internal/web/usecase/auth.go), [adapter/out/redis/session_repo.go](../internal/web/adapter/out/redis/session_repo.go) |
| API-токены: `db_<base64>` префикс, SHA-256 hash, scopes, audit | ✅ | [web/usecase/api_token.go](../internal/web/usecase/api_token.go), [http/api_token_middleware.go](../internal/web/adapter/in/http/api_token_middleware.go) |
| Audit log (CRUD узлов, replay, dry_run, login, token actions) с retention | ✅ | [web/usecase/audit.go](../internal/web/usecase/audit.go), [usecase/housekeeping.go](../internal/web/usecase/housekeeping.go) |
| `RequireSessionOnly` для SSE (отклоняет API-токены) | ✅ Phase 5 | [api_token_middleware.go](../internal/web/adapter/in/http/api_token_middleware.go) |
| Полный лейаут §7 (Overview cards, KPI-блоки, ClickHouse-настройки, Users-страница) | ✅ Phase 6.3/6.4 | Settings → Sentry/ClickHouse/Users CRUD с диалогами, live-tail UI с фильтрами и подсветкой |

### §8 Конфигурация

| Пункт | Статус | Где |
|---|---|---|
| `config/config.yml` + `config_debug.yml` + `config.example.yml` | ✅ | [config/](../config/) |
| Env-вставки `${VAR:default}` | ✅ | [platform/config/load.go](../internal/platform/config/load.go) |
| Флаги `--config`, `--debug`, `--version` | ✅ | [platform/config/flags.go](../internal/platform/config/flags.go) |
| **`app_settings` таблица + REST API + overlay поверх env на старте** | ✅ Phase 6.3.1 | миграция [0006](../migrations/0006_app_settings.up.sql), [domain/app_settings.go](../internal/domain/app_settings.go), [usecase/app_settings.go](../internal/web/usecase/app_settings.go), [http/app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go), [bootstrap/app_settings.go](../internal/platform/bootstrap/app_settings.go) |
| **Hot-reload Sentry через Redis pub/sub** | ✅ Phase 6.3.2 | [platform/reloader/](../internal/platform/reloader/), [sentry.Reload](../internal/platform/sentry/sentry.go), [bootstrap/reload.go](../internal/platform/bootstrap/reload.go) — Web публикует на канал `databus:config:reload`, Receiver/Sender/Web подписаны и переинициализируют SDK |
| **Полное hot-reload ClickHouse (пересоздание клиента/writer'а)** | ✅ Phase 6.3.2.5 | [clickhouse.Manager](../internal/platform/clickhouse/manager.go) (атомарный swap conn + delayed close), [chlog.WriterManager](../internal/sender/adapter/out/chlog/manager.go) (пересоздание Writer для смены BufferMaxSize/Workers), [bootstrap.ClickHouseReloader](../internal/platform/bootstrap/reload.go) — Sender и Web swap'ают conn и переподнимают зависимые компоненты без рестарта |
| **Test connection для Sentry/ClickHouse (§7.10)** | ✅ Phase 6.3.2.6 | [usecase.SettingsTester](../internal/web/usecase/settings_tester.go) + POST `/api/settings/{clickhouse,sentry}/test`, кнопка «Test connection» в [Sentry.tsx](../web-ui/src/pages/settings/Sentry.tsx) и [ClickHouse.tsx](../web-ui/src/pages/settings/ClickHouse.tsx) — открывает временный conn / создаёт изолированный sentry.Client с merge'нутыми настройками, возвращает `{ok, latency_ms}` или `{error}` без сохранения |
| **Settings → Sentry / ClickHouse страницы в SPA** | ✅ Phase 6.3.3 | [pages/settings/Sentry.tsx](../web-ui/src/pages/settings/Sentry.tsx), [pages/settings/ClickHouse.tsx](../web-ui/src/pages/settings/ClickHouse.tsx), две новые вкладки в [pages/Settings.tsx](../web-ui/src/pages/Settings.tsx) (видны только admin-роли через `/api/auth/me`) |
| **Settings → Users (полная страница CRUD с диалогами создания/смены пароля, §7.9/§7.10)** | ✅ Phase 6.4 | [pages/settings/Users.tsx](../web-ui/src/pages/settings/Users.tsx), таблица + UserDialog (create/edit) + PasswordDialog (compact). Учитывает «нельзя удалить/отключить себя», «нельзя оставить < 1 активного админа», баннер «admin использует пароль по умолчанию», метку «это вы», relative-time для last_login. Backend: исправил [auth_handler.Me](../internal/web/adapter/in/http/auth_handler.go) — теперь возвращает `{user:{user_id, login, email, role, lang, must_change_password}}` (был плоский ответ, фронт ждал обёртку → isAdmin-вкладки не показывались). Добавлен [AuthUsecase.Me](../internal/web/usecase/auth.go) для подъёма login/email из БД |
| **Live-tail UI: подсветка новых записей (1s), авто-прокрутка, баннер «N новых», pagination size, фильтры status/done (§7.4)** | ✅ Phase 6.5 | [pages/NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) — буфер новых SSE-записей подсвечивается фоном через `transition-colors`, сбрасывается через 1000ms; sticky thead с прокруткой внутри блока; `autoScrollRef` отслеживает ручной скролл, при отступе от верха больше 8px включается баннер «N new ↑» (клик возвращает к live); SegmentedControl OK/Errors/All + Done/Pending/Any фильтрует на клиенте; селектор 50/100/200 заменяет `limit` snapshot-запроса |
| **CSV-экспорт audit log (§7.13, кнопка «Export CSV»)** | ✅ Phase 6.6 | `GET /api/audit/export.csv` ([audit_handler.go](../internal/web/adapter/in/http/audit_handler.go) `ExportCSV`) — тот же набор фильтров что у `List`, default limit 10000, max 50000. CSV с UTF-8 BOM (для Excel), 9 колонок (id, created_at, user_login, user_id, action, target_type, target_id, ip_address, details-as-json). UI: ссылка `<a download>` в [pages/AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx) header'е, прокидывает текущий `action`-фильтр. Общая функция `auditFilterFromQuery` извлечена из `List` для переиспользования. |
| **ClickHouse orphan-tables: сканер + DROP (§7.10, Phase 6.7)** | ✅ Phase 6.7 | `GET /api/settings/clickhouse/orphans` + `DELETE /api/settings/clickhouse/orphans/:table` ([orphan_handler.go](../internal/web/adapter/in/http/orphan_handler.go)). Usecase [orphan_scanner.go](../internal/web/usecase/orphan_scanner.go): SELECT name,engine,total_rows,total_bytes из `system.tables` для базы из cfg.ClickHouse.Database (только `*MergeTree*`, не служебные `.inner*`/`.tmp*`); вычитает known-set из `nodes.clickhouse_table`. `Drop` валидирует имя (isSafeTableNameLocal), повторно перепроверяет orphan-статус (защита от race) и логирует action=`ch_table.drop` в audit. UI: компонент [OrphanTablesPanel](../web-ui/src/components/OrphanTablesPanel.tsx) внизу страницы Settings → ClickHouse, lazy-fetch (только после клика «Scan»), DROP с двойным подтверждением (модал требует ввести имя таблицы вручную). |
| **Live-tail: расширенные фильтры (§7.4, Phase 6.8)** | ✅ Phase 6.8 | `port.LogQuery` (period/IP/Host/status/done/q) + `LogReader.Search` ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)): SQL-фильтр через positionCaseInsensitiveUTF8 для full-text, exact match для IP/Host, диапазон по toUnixTimestamp64Milli. `GET /api/nodes/{id}/logs` принимает query: `from`/`to` (RFC3339 или UnixMilli), `ip`, `host`, `status` (ok/err), `done` (yes/no), `q`. Legacy `since_ms` остался для cursor'а; при нём расширенные фильтры игнорируются (backwards-compatible). SSE `/api/nodes/{id}/logs/stream` принимает те же поля, фильтрация в-памяти через `matchLogFilter` в `LogsUsecase.Subscribe`. UI: раскрываемая панель «Advanced filters» в [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) (textbox `q` + IP + Host + datetime-local `from`/`to` + кнопки Apply/Reset), при изменении applied — пересоздаёт snapshot-query и EventSource. |
| **Audit log: diff-двухколоночный для `node.update` (§7.13, Phase 6.9)** | ✅ Phase 6.9 | Компонент [AuditDetailsCell.tsx](../web-ui/src/components/AuditDetailsCell.tsx) рендерит детали audit-записи по action: для `node.update` (details формы `{field: {before, after}}` — см. `diffNodes` в [node.go](../internal/web/usecase/node.go)) — компактная таблица 3 колонки (поле / before в `text-err` / after в `text-ok`); спецслучай `auth_credentials: "changed"` — одной строкой italic; для остальных action — JSON в `<details>` (как раньше). [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx): ячейка `details` заменена на `<AuditDetailsCell action={e.action} details={e.details} />`. Локализация: `audit.diff.{before,after,no_changes}`. |

### §9 Высоконагруженность / отказоустойчивость

| Пункт | Статус | Где |
|---|---|---|
| Redis-кеш конфига узлов + cache-aside | ✅ | [receiver/adapter/out/nodecache/reader.go](../internal/receiver/adapter/out/nodecache/reader.go) |
| **Локальный LRU L2-кеш (1-5 сек) + stale-fallback при ошибках downstream** | ✅ Phase 7.2 | [nodecache/lru.go](../internal/receiver/adapter/out/nodecache/lru.go) + [nodecache/l2.go](../internal/receiver/adapter/out/nodecache/l2.go); конфиг `receiver.l2_cache.{enabled,size,ttl_ms,stale_ttl_ms}`; метрики `databus_l2_cache_{hits,misses,evictions}_total` + `databus_l2_cache_size` |
| Pool соединений PG/Redis по конфигу | ✅ | `MaxOpenConns`, `PoolSize` в YAML |
| **Graceful shutdown с дренажом CH-буфера и Kafka offset** | ✅ | `App.Stop` в каждом сервисе, `chWriter.Stop(ctx)` |
| **CH file-fallback при недоступности (§9.4)** | ✅ Phase 5.1 | [chlog/fallback.go](../internal/sender/adapter/out/chlog/fallback.go) |
| Circuit breaker per-node в Redis | ✅ | [platform/circuitbreaker/redis.go](../internal/platform/circuitbreaker/redis.go) |
| Rate-limit per-node + per-token | ✅ | [platform/ratelimit/redis.go](../internal/platform/ratelimit/redis.go) |
| `/health` (liveness) + `/ready` (с degraded body) | ✅ | [platform/healthcheck/healthcheck.go](../internal/platform/healthcheck/healthcheck.go) |
| Загрузочный тест 500 rps × 10 мин | ✅ | [cmd/loadtest/main.go](../cmd/loadtest/main.go), `make loadtest` |

### §10 Тестирование

| Пункт | Статус | Где |
|---|---|---|
| Unit-тесты domain/crypto/usecase/i18n/sentry/chlog | ✅ Phase 5/5.2 | `*_test.go` в соответствующих пакетах |
| **Integration testcontainers** (Postgres + миграции) | ✅ Phase 5/5.1 | [tests/integration/](../tests/integration/), `make test-integration` |
| Loadtest бинарь с pass/fail-критериями | ✅ | [cmd/loadtest](../cmd/loadtest/) |
| **Полный testcontainers-сетап (PG + Redis + CH + Kafka)** | ✅ Phase 7.3 | PG ([node_repo_test.go](../tests/integration/node_repo_test.go)), Kafka ([receiver_async_test.go](../tests/integration/receiver_async_test.go)), Redis ([redis_test.go](../tests/integration/redis_test.go) — SessionRepo + NodeCache + TTL-expire), CH ([clickhouse_test.go](../tests/integration/clickhouse_test.go) — chlog.Writer batch insert + LogReaderCH `GetByID`/`Search` + table-name SQL-injection guard) |
| **Async end-to-end интеграция через Kafka** | ✅ Phase 6.2 | [tests/integration/receiver_async_test.go](../tests/integration/receiver_async_test.go) — реальный pipeline `RouteAsyncUsecase → Kafka → ConsumerGroup → AsyncProcessor → SendUsecase → mock HTTP` |

### §11 Swagger / OpenAPI

| Пункт | Статус | Где |
|---|---|---|
| Аннотации `@Summary/@Param/...` на ключевых handlers | ✅ Phase 5 | login, nodes (List/Get/Create), dry-run, replay, logs (List/Stream) |
| `make swagger` (через `swag init -g cmd/web/main.go`) | ✅ | [Makefile](../Makefile) |
| `make swagger-drift-check` для CI | ✅ Phase 5 | сравнивает `git diff --exit-code docs/` после регенерации |
| **Полные аннотации на 100% endpoints** | ✅ Phase 7.1 | auth (login/logout/me), nodes (List/Get/Create/Update/Delete), users (List/Get/Create/Update/Delete/ChangePassword), tokens (List/Create/Revoke/Delete), audit (List/ExportCSV), dry-run, replay, logs (List/Stream), settings/app (Get/Update/TestClickHouse/TestSentry), settings/clickhouse/orphans (List/Drop) |
| **Swagger UI handler в Gin** | ✅ Phase 7.1 | `r.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))` в [internal/web/app.go](../internal/web/app.go) + blank-import `_ "bus/docs/web"` для регистрации генеренного docTemplate в `swag.Registry` |

### §12 Структура репозитория

✅ Соответствует ТЗ. См. [README.md](../README.md) → раздел «Структура проекта».

### §13 Сборка и запуск

| Пункт | Статус | Где |
|---|---|---|
| Makefile, Windows + Linux совместим | ✅ | [Makefile](../Makefile) |
| Все цели (build/run/test/migrate/docker/swagger/loadtest) | ✅ | `make help` |
| **`make build-ui`** (Vite-сборка SPA + копирование в `internal/web/static/`) | ✅ Phase 5 | |
| **`make rotate-encryption-key`** | ✅ Phase 5 | |
| **`make swagger`** + **`make swagger-drift-check`** | ✅ Phase 5 | |
| **`make test-integration`** (testcontainers) | ✅ Phase 5 | |
| docker-compose со всеми зависимостями + healthcheck | ✅ | [deploy/docker-compose.yml](../deploy/docker-compose.yml) |
| `docker-compose.dev.yml` override | ✅ | [deploy/docker-compose.dev.yml](../deploy/docker-compose.dev.yml) |

### §14 Логирование и Sentry

| Пункт | Статус | Где |
|---|---|---|
| `github.com/vsilchenkov/logging` через DI | ✅ | [platform/logging/logging.go](../internal/platform/logging/logging.go) — алиас `Logger`, `Init`, `NewNoop` для тестов |
| `ErrorWithOp` с `op` для группировки | ✅ | используется во всех handlers/usecase |
| Sentry с `BeforeSend`/`BeforeBreadcrumb` для маскирования | ✅ | [platform/sentry/sentry.go](../internal/platform/sentry/sentry.go) |
| **Sentry tracing-middleware для Gin (§14.3)** | ✅ Phase 5 | [platform/sentry/middleware.go](../internal/platform/sentry/middleware.go) — span'ы с тегами service/node/root_method |
| `app_settings` PostgreSQL singleton + Web UI Sentry | ✅ Phase 6.3 | overlay поверх env, hot-reload Sentry/ClickHouse через Redis pub/sub, test connection (см. §8 строки 125-128) |

### §15 Критерии приёмки

См. [sections/15-acceptance.md](sections/15-acceptance.md). Покрытие: ~95% пунктов реализовано.
Не покрыто (требует Phase 6+):

- Полный Audit log: CSV-экспорт (Phase 6.6), diff-двухколоночный для `node.update` (Phase 6.9) — сделано.
- Live-tail UI: расширенные фильтры сделаны в Phase 6.8 (`q`/`ip`/`host`/`from`/`to` — на backend через `port.LogQuery`).

### §16 Out of scope (явно отложено в v2)

- Multi-tenancy логика (колонки `team_id` уже есть, изоляция — нет).
- Webhook signature verification (`/v1/callback/`).
- Notifications для операторов (Slack/Telegram).
- Шаблоны узлов.
- Версионирование конфигов узла + откат.
- Bulk-операции, импорт/экспорт.
- Mutating API tokens.
- KMS/Vault интеграция.
- OpenTelemetry.

### §17 Паттерны разработки

| Принцип | Где соблюдается |
|---|---|
| Clean Architecture: handler → usecase → port → adapter | все 3 сервиса (`internal/{receiver,sender,web}/{domain,usecase,adapter}`) |
| Accept interfaces, return structs | usecase зависят от `port.*` интерфейсов; конструкторы возвращают `*NodeUsecase` |
| Интерфейсы на стороне consumer | `port/node_reader.go`, `port/node_repo.go`, `port/log_reader.go`, `port/dispatcher.go`, `port/uow.go` |
| `context.Context` первым параметром | все методы adapter/usecase |
| Wrap errors через `%w`, проверка через `errors.Is` | везде, particularly в `replyDomainError` |
| Маппинг row → domain | `NodeRepoPg.scan`, `auditRepo.List` |
| **UnitOfWork для атомарности (§17.4)** | ✅ Phase 5 — [adapter/out/postgres/uow.go](../internal/web/adapter/out/postgres/uow.go), `DBTX` интерфейс |
| Frontend: TanStack Query для server state, `useState` для UI | ✅ Phase 5 — [web-ui/src/pages/](../web-ui/src/pages/) |
| Frontend: компоненты без прямых `fetch`, только через hooks | ✅ | `useQuery`, `useMutation`; единственный axios — в [api/client.ts](../web-ui/src/api/client.ts) с 401-interceptor'ом |
| Логгер через DI (никаких `logging.GetLogger()`) | ✅ | проверено в каждом конструкторе |
| Никаких глобалов / `init()` со side-effects | ✅ | |

---

## 3. Где что лежит — карта каталогов

```text
/cmd
  /receiver       Receiver Service entry point
  /sender         Sender Service entry point
  /web            Web Service entry point (REST API + SPA)
  /loadtest       нагрузочный сценарий §10.2
  /rotate-key     утилита ротации ENCRYPTION_KEY (§5.5)

/internal
  /domain         доменные сущности (Node, User, LogRecord, ...), ошибки, enum'ы
  /platform       cross-cutting: bootstrap, config, logging, sentry (+middleware),
                  pg (pool, migrate), redis, kafka, clickhouse, healthcheck, crypto,
                  ratelimit, circuitbreaker, runner (kardianos/service), i18n
  /receiver
    /domain       receiver-specific сущности (пока пусто)
    /usecase
      /port       NodeReader интерфейс
    /adapter/in/http        Gin handlers (handler.go, middleware.go)
    /adapter/out/grpcsender gRPC-клиент к Sender
    /adapter/out/nodecache  Redis NodeCache → PG fallback
  /sender
    /usecase
      /port       LogWriter, NodeReader
      send.go, async.go, ch_housekeeping.go
    /adapter/in/grpc        gRPC server (SenderService)
    /adapter/in/kafka       Kafka-consumer group
    /adapter/out/chlog      ClickHouse batch writer + file-fallback
    /adapter/out/httpclient HTTP-клиент к внешним узлам
    /adapter/out/nodepg     PG NodeReader (без crypto, для housekeeping)
  /web
    /usecase
      /port       NodeRepo, NodeCache, UserRepo, SessionRepo, AuditRepo, APITokenRepo,
                  UnitOfWork, LogReader, ReceiverDispatcher
      node.go, auth.go, user.go, api_token.go, audit.go, dry_run.go,
      replay.go, logs.go, housekeeping.go
    /adapter/in/http        Gin handlers + middleware (auth, api_token, RequireSession/Scope)
    /adapter/out/postgres   NodeRepoPg, UserRepoPg, AuditRepoPg, APITokenRepoPg, UnitOfWorkPg
    /adapter/out/redis      SessionRepoRedis, NodeCacheRedis
    /adapter/out/clickhouse LogReaderCH (для replay + live-tail)
    /adapter/out/receiver   HTTPDispatcher (replay через реальный Receiver pipeline)
    /static                 embed.FS для SPA (index.html + assets)

/proto/sender/v1     .proto + сгенерированный Go-код gRPC

/migrations
  0001_init_schema           базовые таблицы + uuid extension
  0002_nodes_methods_users   nodes (полная схема §3.3), users, node_headers, methods
  0003_user_audit            audit log таблица
  0004_api_tokens            API tokens (SHA-256 hash, scopes, expires_at)
  0005_node_retention        clickhouse_retention_days в nodes (§4.3)

/web-ui              React 18 + Vite + TS + Tailwind
  /src
    main.tsx, App.tsx, i18n.ts
    /api/client.ts             axios + 401-interceptor + типы (Node)
    /pages                     Login, Overview, NodeDetail, NodeSettings,
                               AuditLog, Settings (с под-страницами)
      /settings                ApiTokens, Language, Theme
    /components                Topbar, DryRunDialog, ReplayDialog
    /locales                   en.json, ru.json
    /styles/globals.css        CSS variables для тем + Tailwind

/tests/integration   integration-тесты с build-tag `integration`,
                     поднимают Postgres через testcontainers-go

/docs/web            сгенерированный Swagger (docs.go, swagger.json/yaml)

/specs               ТЗ (исходный) + разделённое по разделам
  /sections          01-purpose..17-patterns
  data_bus_spec.md   сводный документ
  IMPLEMENTATION.md  ЭТОТ ФАЙЛ
```

---

## 4. Архитектурные решения и неочевидности

Эти моменты не очевидны из кода без контекста — стоит держать в голове при доработке.

### 4.1 Шифрование auth_credentials живёт только в `adapter/out/postgres`

`domain.Node` всегда хранит **открытый** plaintext. Шифрование/расшифровка происходит
исключительно в [postgres/node_repo.go](../internal/web/adapter/out/postgres/node_repo.go)
(`Create`, `Update`, `scan`) и [sender/adapter/out/nodepg/reader.go](../internal/sender/adapter/out/nodepg/reader.go).
Это намеренное архитектурное решение (§5.5 ТЗ + §17.4 «mapper.go»):
usecase не знает, что креды зашифрованы — он работает с готовым `*domain.Node`.

### 4.2 NodeRepoPg / AuditRepoPg принимают `DBTX`, не pool

После Phase 5 (UnitOfWork) репозитории принимают [DBTX](../internal/web/adapter/out/postgres/db.go)
— минимальный интерфейс над `pgxpool.Pool` и `pgx.Tx`. Это позволяет
[UnitOfWorkPg](../internal/web/adapter/out/postgres/uow.go) создавать транзакционные
клоны репозиториев с тем же типом. Если будете добавлять новый Postgres-репозиторий —
используйте `DBTX` вместо `*pgxpool.Pool`.

### 4.3 NodeUsecase атомарен через UoW для Create/Update/Delete

Когда `uow != nil` (production-сборка) — операция и audit-запись идут в одной
транзакции. Если `uow == nil` (старые тесты) — fallback на не-атомарный путь.
Не убирайте fallback: он удобен для unit-тестов, где UoW не нужен.

### 4.4 Receiver делает sync→async для paused-узлов на уровне handler'а

`Route()` для paused-узла возвращает `ErrNodePaused` — это сигнальная ошибка,
не настоящая. Handler ловит её в `handleSync` и переключается на `handleAsyncFromInput`
с тем же `RouteInput`. Не превращайте `ErrNodePaused` в внутреннюю реализацию —
этот трюк сохраняет SRP и не создаёт круговых зависимостей usecase ↔ usecase.

### 4.5 Replay идёт через реальный Receiver pipeline, не bypass

[HTTPDispatcher](../internal/web/adapter/out/receiver/dispatcher.go) делает HTTP-запрос
обратно в Receiver Service (`http://receiver:8080`). Это намеренно (§7.4.1 ТЗ):
replay должен воспроизводить **реальное** поведение, включая авторизацию, allowlist,
maskиование, rate-limit per-node. Маркер `__replay_of=<orig_id>` добавляется в query.

### 4.6 SSE live-tail — это polling, не push

[LogsUsecase.Subscribe](../internal/web/usecase/logs.go) опрашивает ClickHouse раз
в 1 секунду по курсору `date_request`. Это компромисс: при большом числе одновременных
SSE-клиентов нагрузка на CH растёт линейно. Долгосрочный путь — pub/sub через Kafka
`databus.logs` (out-of-scope в v1). SSE отклоняет API-токены через
[RequireSessionOnly](../internal/web/adapter/in/http/api_token_middleware.go) — только UI-сессии.

### 4.7 ClickHouse fallback — атомарная запись через `.tmp` + rename

[fallbackStore.Save](../internal/sender/adapter/out/chlog/fallback.go) сначала пишет
в `name.tmp`, fsync, потом `os.Rename`. На Windows нельзя удалить открытый файл —
поэтому `restoreFile` читает через отдельную `readFallbackFile` функцию, которая
закрывает дескриптор до `os.Remove`. **Не сливайте это обратно в одну функцию** —
тесты на Windows упадут.

### 4.8 Theme/i18n в SPA — CSS variables, не Tailwind dark:

Tailwind palette построена через `rgb(var(--bg) / <alpha-value>)`
(см. [tailwind.config.js](../web-ui/tailwind.config.js)). Все цвета вытаскиваются из
CSS-переменных, заданных в [globals.css](../web-ui/src/styles/globals.css) для
`:root` (light) и `html.dark` (dark). Это даёт мгновенное переключение без
перерисовки и поддержку opacity (`bg-bg-muted/40` работает).

Тема инициализируется **до** React-рендера в [main.tsx](../web-ui/src/main.tsx),
чтобы избежать flash-of-light при перезагрузке страницы.

### 4.9 i18n: backend и SPA — два разных слоя переводов

- Backend: [internal/platform/i18n](../internal/platform/i18n/) — словарь
  для текстов ошибок API ответов. Парсит `Accept-Language` в middleware.
- SPA: [web-ui/src/locales/](../web-ui/src/locales/) — словарь
  для UI через `react-i18next`, текущий язык в `localStorage`.

Перевод **одного и того же ключа** может быть на обеих сторонах — например,
"node.not_found" в backend выводит локализованный JSON-error, а в SPA рендерит
этот error из ответа API без перевода. Если хотите больше «фронт-only» —
скрывайте API-ошибку и показывайте локализованную SPA-строку.

### 4.10 Sentry tracing требует включения в конфиге

`SENTRY_ENABLE_TRACING=true` + `SENTRY_TRACES_SAMPLE_RATE=0.1` — иначе
[sentry.GinMiddleware](../internal/platform/sentry/middleware.go) создаёт спаны,
но Sentry SDK их не отправляет. Это no-op без аппроксимации к ошибкам — не пугайтесь
«отсутствующих» транзакций в Sentry, проверьте config.

### 4.10.1 Hot-reload ClickHouse — Manager владеет conn, consumers через ConnProvider

При hot-reload (Phase 6.3.2.5) важно не оставить «висящих» ссылок на старый
`driver.Conn`, иначе после swap'а они продолжат пилить закрытое соединение.
Архитектурное решение: вынесли владение conn'ом в
[clickhouse.Manager](../internal/platform/clickhouse/manager.go), а consumers
(`chlog.Writer`, `LogReaderCH`, `CHHousekeeping`, `clickhouse.HealthChecker`)
принимают `ConnProvider` (одно-методный интерфейс с `Conn() driver.Conn`)
вместо raw `driver.Conn`. Каждый вызов внутри consumer'а заново берёт
актуальный conn — swap прозрачен.

Старый conn закрывается с задержкой `closeDelay` (по умолчанию 15 секунд),
чтобы in-flight batch insert и SELECT успели завершиться. Это компромисс
без ref-counting: при 15-секундной задержке хватает на типовые операции
chlog.flushTable (≤10 сек timeout) и LogReaderCH.GetByID. Если в будущем
понадобится точный учёт активных пользователей — переключиться на счётчик
`atomic.Int32` вокруг каждого `Conn()` вызова.

Полное пересоздание `chlog.Writer` (для смены `BufferMaxSize`/`Workers`/
`BatchSize`) — отдельная задача, потому что эти поля фиксируются при
создании канала и пула горутин. Это делает
[chlog.WriterManager](../internal/sender/adapter/out/chlog/manager.go):
обёртка над `*Writer`, реализующая `port.LogWriter`. При `Reload`
поднимает свежий `Writer` с актуальным cfg, делает `Stop` (с flush'ем
остатка) на старом. Если в момент swap'а кто-то писал — `Write`
делегируется новому writer'у через RWMutex, потерь нет.

`bootstrap.ClickHouseReloader` координирует: overlay cfg ← app_settings →
`Manager.Reload(ctx)` (open + ping + swap) → `WriterReloader.Reload(ctx)`
для каждого зарегистрированного writer'а. При ошибке открытия нового
conn'а старый остаётся живым — битые UI-настройки не убивают поток логов.

### 4.11 Prometheus-метрики живут в собственном registry, не default

[platform/metrics.New(service)](../internal/platform/metrics/metrics.go) создаёт
изолированный `*prometheus.Registry` с предзарегистрированными Go-runtime и Process
collectors. Каждый App создаёт собственный экземпляр и передаёт по DI в middleware,
gRPC server, chlog.Writer и AsyncProcessor — глобальной registry мы не пользуемся
(CLAUDE.md §4: «No globals»). Поэтому `/metrics` подключается как
`r.GET("/metrics", gin.WrapH(a.metrics.Handler()))`, а не `promhttp.Handler()`.

Метка `service` — статическая, задаётся через `ConstLabels` в конструкторах метрик.
Это даёт возможность скрапить три сервиса с одинаковыми именами метрик и фильтровать
их в Grafana через `service="receiver"`. Метка `node` — динамическая (path-параметр
из `/v1/request/.../*path`), для не-V1 маршрутов остаётся пустой.

### 4.12.1 L2 in-memory кеш узлов — декоратор поверх Reader, stale-fallback по StaleTTL

`receiver.l2_cache` (Phase 7.2) включает локальный LRU поверх обычного
[nodecache.Reader](../internal/receiver/adapter/out/nodecache/reader.go).
Архитектурно — чистый decorator: [nodecache.L2Reader](../internal/receiver/adapter/out/nodecache/l2.go)
реализует тот же `port.NodeReader`, что и его inner. Если `Enabled=false` —
`NewL2` возвращает inner без обёртки (нулевой overhead).

Семантика отказоустойчивости:

- **Fresh-hit** (запись не протухла) — отдаётся из памяти, downstream не дёргается.
- **Miss** — идём в Redis/PG, при успехе пишем в L2.
- **Downstream error + `StaleTTL > 0`** — пробуем вернуть протухшую запись, если её
  возраст ≤ `StaleTTL`. Это и есть §9.4 «крайний случай: одновременно лежат Redis
  и PG» — на горячих узлах сервис продолжает отвечать. Возвращаем `stale`-метку в
  `databus_l2_cache_hits_total{kind="stale"}` и Warn в логи.
- **`ErrNodeNotFound`** — stale-fallback не срабатывает: «нет узла» — это валидный
  ответ, кешировать его как «есть» нельзя.

`LRU[V]` (generic, [lru.go](../internal/receiver/adapter/out/nodecache/lru.go)) —
своя минимальная реализация (~150 строк): `container/list` + `map[string]*Element`
+ `sync.Mutex`. Без `samber/hot` — задача узкая, ставить внешнюю зависимость ради
этого нецелесообразно. Часы инжектятся через `Clock`-интерфейс (тесты без
real-sleep).

### 4.12 Bootstrap ↔ Service-runner

Все три сервиса используют общий [internal/platform/runner/runner.go](../internal/platform/runner/runner.go)
поверх `kardianos/service`. Это позволяет запускать как обычный процесс ИЛИ как
системный сервис Windows/Linux. Метод `Start(ctx)` должен быть **блокирующим**,
`Stop(ctx)` — graceful shutdown с таймаутом.

---

## 5. Команды для типовых задач

```bash
# Сборка и запуск
make build-receiver build-sender build-web build-windows build-linux
make run-receiver run-sender run-web          # с config_debug.yml
make build-ui                                  # SPA → internal/web/static/

# Тесты
make test                                      # unit (-race -short)
make test-coverage                             # покрытие в coverage.html
make test-integration                          # testcontainers (нужен Docker)
make loadtest TARGET_RPS=500 DURATION=10m NODES=50 ADMIN_PASSWORD=…

# Миграции
make migrate-up
make migrate-down N=1
make migrate-status
# Bootstrap admin (после первой миграции password_hash NULL):
make set-admin-password PASSWORD=mySecret

# Шифрование
make rotate-encryption-key OLD_KEY=… NEW_KEY=…
make rotate-encryption-key OLD_KEY=… NEW_KEY=… DRY_RUN=true

# Swagger
make swagger                                   # docs/web/
make swagger-drift-check                       # CI: фейлит если docs/ устарели

# Docker
make docker-up                                 # полный стек
make docker-up-dev                             # с override config
make docker-logs

# Proto / gRPC
make proto                                     # перегенерация sender.pb.go
```

---

## 6. Известные неочевидности и грабли

1. **OOM при `go build ./...` на Windows.** `cmd/sender` тянет много deps (Kafka,
   testcontainers косвенно из go.mod) и линкер падает с VirtualAlloc. Решение —
   собирать по одному `./cmd/<name>` с `-ldflags="-s -w"` (см. TESTING.md → Отладка).

2. **swag init нужен go в PATH.** Если CI пытается генерировать docs/, убедитесь,
   что `swag` доступен (`go install github.com/swaggo/swag/cmd/swag@latest`).

3. **testcontainers требует Docker daemon.** На Windows — Docker Desktop запущен и
   расшарен с WSL2. На Linux/macOS — `systemctl start docker`.

4. **CH file-fallback каталог нужно бэкапить.** Если ClickHouse недоступен дольше
   суток, в `logs/clickhouse-fallback/` копится много NDJSON-файлов. Не удаляйте
   их вручную — они автоматически переотправляются раз в 30 секунд.

5. **Replay не работает без ClickHouse.** Web Service стартует даже если CH
   недоступен (опциональная зависимость через `bootstrap.TryClickHouse`), но
   `replayHandler` будет nil → endpoint не зарегистрирован → 404.

6. **`bootstrap.MustCipher` валит сервис при пустом `ENCRYPTION_KEY`.** Это
   сознательно (§5.5 ТЗ). Для локальной отладки положите в `.env`:
   `ENCRYPTION_KEY=$(openssl rand -base64 32)`.

7. **gRPC-пакет в `proto/sender/v1` версионирован в имени пакета.** Если будете
   делать breaking change — создайте `proto/sender/v2`, не модифицируйте v1.

8. **Sentry-маскирование работает по именам ключей.** Если в логи попадает новое
   чувствительное поле — добавьте имя в `sensitiveKeys` в
   [sentry/sentry.go](../internal/platform/sentry/sentry.go), иначе значение уйдёт в Sentry.

9. **`team_id` колонки уже есть, но в v1 всегда `'default'`.** Не делайте новых
   методов с `team_id`-фильтром, пока не появится Multi-tenancy в v2.

10. **Pagination в LogReader.GetByID использует `LIMIT 1`.** ClickHouse не понимает
    `WHERE id = ? LIMIT 1` так же как Postgres: если у одного `id` две записи (replay
    того же лога), вернётся произвольная. В практике UUID v4 коллизии исключены.

---

## 7. Куда копать дальше (Phase 7+)

Если будете расширять — вот логичные следующие шаги, в порядке полезности:

1. **OpenTelemetry distributed tracing** (§16: явно out-of-scope v1, но даст
   корреляцию logs↔traces↔metrics при росте числа сервисов).
2. **Webhook signature verification** (`/v1/callback/`) — §16, для приёма
   входящих webhook'ов от партнёров (Stripe/GitHub/...).
3. **KMS/Vault** интеграция для `ENCRYPTION_KEY` — §16, чтобы убрать секрет
   из env. См. также `make rotate-encryption-key`.
4. **Multi-tenancy v2** — колонки `team_id` уже есть, нужен RBAC по team_id
   + миграция existing `'default'`-данных.

Сделанное в Phase 6:

- 6.1 Prometheus метрики (`databus_requests_total`, latency, kafka_lag, CH-метрики).
- 6.2 Async end-to-end integration через Kafka.
- 6.3 app_settings + hot-reload Sentry/ClickHouse + test connection.
- 6.4 Settings → Users полный CRUD.
- 6.5 Live-tail UI улучшения (подсветка, авто-прокрутка, баннер).
- 6.6 CSV-экспорт audit log.
- 6.7 ClickHouse orphan-tables (сканер + DROP с подтверждением).
- 6.8 Расширенные фильтры live-tail (period/IP/Host/full-text).
- 6.9 Audit log: diff-двухколоночный для `node.update`.

Сделанное в Phase 7.7:

- 7.7 Security scanning: новый workflow [.github/workflows/security.yml](../.github/workflows/security.yml).
  Триггеры: push/PR в master|dev, weekly cron (вс 06:00 UTC), workflow_dispatch.
  · **govulncheck** (`golang.org/x/vuln`) — официальный сканер CVE с call-graph
  анализом (не просто проверка версий, а сопоставление с фактически вызываемым
  кодом). Падает на vuln, гейтит PR — это разумно, так как call-graph отсеивает
  ложные срабатывания.
  · **gosec** (securego/gosec@master) — статический анализатор OWASP/CWE
  правил (G-серии). Не падает на findings (`-no-fail`); SARIF → Security tab
  репозитория, чтобы PR-CI не блокировался шумом. Исключены `web-ui/` и `docs/`.
  · **Trivy fs** (aquasecurity/trivy-action@0.28.0) — vuln (включая npm в web-ui)
  + secret scanning + Dockerfile/YAML misconfig. severity CRITICAL|HIGH|MEDIUM,
  `ignore-unfixed: true`. SARIF → Security tab, exit-code 0 (не блокирует).
  · **Nancy** (Sonatype OSS Index) — дополнительный source CVE-индекса,
  перекрывает govulncheck по less-критичным. `continue-on-error: true`.
  · Permissions: `security-events: write` для upload-sarif в Code Scanning.
  · Локальные Make-цели: `make vuln-check`, `make gosec`, `make security-scan`
  (auto-install через `go install` если не найдено).
  Локальная верификация: `govulncheck ./internal/platform/crypto/...` —
  «No vulnerabilities found» (на полном `./...` падает Windows OOM в SSA-builder,
  это известная проблема — в Linux CI отрабатывает корректно).

Сделанное в Phase 7.6:

- 7.6 GoReleaser: релизный pipeline по тегам `v*`.
  · [`.goreleaser.yaml`](../.goreleaser.yaml) — 5 builds (`receiver`, `sender`, `web`,
  `rotate-key`, `loadtest`) × Linux/Windows/macOS × amd64+arm64 (Windows/arm64
  исключён — не поддерживается рядом deps); archives (tar.gz, zip для Windows)
  с README/LICENSE/config.example/migrations, SHA-256 checksums, GitHub-changelog
  (группы Features / Bug fixes / Phase milestones).
  · ldflags `-X bus/internal/platform/build.{Version,Commit,BuildDate}` —
  переопределяют значения из `versioninfo.json` при release-сборке;
  переменные пакета добавлены в [build/build.go](../internal/platform/build/build.go),
  fallback-логика сохранена. [bootstrap.Init](../internal/platform/bootstrap/bootstrap.go)
  логирует `commit`/`build_date` в стартовом сообщении, если заполнены.
  · 6 docker-образов через `dockers:` (receiver/sender/web × amd64+arm64) → GHCR
  через [release.Dockerfile](../deploy/docker/release.Dockerfile) (использует
  pre-built бинарь, не пересобирает). `docker_manifests:` склеивают arch-варианты
  в multi-arch теги `:{Version}` и `:latest`.
  · GitHub Actions [release.yml](../.github/workflows/release.yml) — триггер
  `push: tags: [v*]` + workflow_dispatch; QEMU + Buildx + GHCR login через
  `GITHUB_TOKEN` (`packages: write`); вычисляет lowercase repo-name для
  ghcr-пути (GHCR требует lowercase).
  · Makefile цели `make release-check` (синтаксис `.goreleaser.yaml`) и
  `make release-snapshot` (локальный snapshot в `dist/` без публикации).
  · Локальная верификация ldflags: `go build -ldflags "-X .../build.Version=v1.2.3 ..."`
  + `./binary --version` печатает `v1.2.3` вместо `0.1.0` из versioninfo.json.

Сделанное в Phase 7:

- 7.1 Swagger 100% endpoints + UI handler (`/swagger/index.html`):
  допокрыты аннотациями auth (logout/me), nodes (Update/Delete), users
  (все 6 handlers), tokens (все 4), audit (List/ExportCSV); подключён
  `ginswagger.WrapHandler` в [internal/web/app.go](../internal/web/app.go),
  blank-import `_ "bus/docs/web"` регистрирует генеренный docTemplate
  в `swag.Registry`. Добавлены deps `github.com/swaggo/gin-swagger` и
  `github.com/swaggo/files`. UI открывается по адресу `/swagger/index.html` на Web Service (по умолчанию `:8081`).
- 7.2 L2 in-memory LRU-кеш узлов в Receiver (§9.2):
  декоратор поверх `nodecache.Reader`, реализует тот же `port.NodeReader`.
  При `Enabled=false` декоратор отдаёт inner как есть. Stale-fallback по
  `StaleTTL` (§9.4 крайний случай: одновременно лежат Redis и PG — на
  горячем наборе узлов сервис продолжает отвечать с протухшего слепка).
  `ErrNodeNotFound` НЕ кешируется. Метрики `databus_l2_cache_hits_total{kind}`,
  `_misses_total`, `_evictions_total`, `_size`. Unit-тесты на детерминированных
  `Clock` (без real sleep).
- 7.5 Grafana dashboard + Prometheus alert rules:
  · `deploy/grafana/databus.json` — 10 панелей: RPS, error rate (%),
  request duration p50/p95/p99, Kafka lag, CH buffer per table, CH
  errors/dropped/fallback, L2 cache hit ratio (fresh vs stale), L2
  size/evictions, Go runtime heap, goroutines. Datasource и `service`
  параметризованы (templating).
  · `deploy/prometheus.alerts.yml` — 9 alert rules: up==0 (receiver/sender),
  5xx>5%, p95>200ms (SLO §9.1), kafka_lag>10k, CH errors/buffer growing/dropped,
  L2 stale-fallback (Redis+PG лежат). Подцеплены через `rule_files:` в
  `deploy/prometheus.yml`, том смонтирован в compose (`prometheus.alerts.yml`).
  · `deploy/grafana/README.md` — инструкция импорта (UI + provisioning).
  Валидация: `promtool check config/rules` — оба файла приняты.
- 7.4 GitHub Actions CI: `.github/workflows/ci.yml` — параллельные jobs
  go-test (race -short), go-build (`go build ./...`), go-lint
  (`golangci-lint v1.62`), swagger-drift (regen `swag init` → `git diff`),
  ui (Node 20 + `npm ci` + `npm run lint --if-present` + `vite build`),
  integration (testcontainers, гейтированный по label `run-integration`
  для PR — тяжёлый сетап с pre-pull docker-образов). `.golangci.yml` с
  набором bodyclose/rowserrcheck/errcheck/govet/revive/staticcheck.
  `.github/dependabot.yml` — еженедельные апдейты gomod + npm, ежемесячно
  github-actions; группировка minor/patch в один PR.
- 7.3 Integration suite: Redis + ClickHouse через testcontainers.
  Generic-контейнер (`testcontainers.GenericContainer`) — без отдельных
  модулей `modules/redis`/`modules/clickhouse`. CH: native-handshake
  готовится позже `ForListeningPort`, поэтому в helper'е активный retry-ping
  до 60 сек. Покрытие: SessionRepoRedis (CRUD + DeleteByUser + TTL expire),
  NodeCacheRedis (Set/GetByPath/Invalidate), chlog.Writer → реальная
  MergeTree-таблица → LogReaderCH (`GetByID`, `Search` с фильтрами
  status/IP/Done/full-text, защита от SQL-инъекции в имени таблицы).
  Весь интеграционный набор (PG + Kafka + Redis + CH) проходит за ~200s.
