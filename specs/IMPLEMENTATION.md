# IMPLEMENTATION.md — карта проделанных работ по ТЗ Nexus

> **Назначение этого файла.** Карта реализации шины данных в привязке к разделам ТЗ
> ([nexus_spec.md](nexus_spec.md), разделённый на [sections/](sections/)).
> Помогает новому агенту/разработчику быстро понять: что уже сделано, где это лежит,
> по какой схеме построено и куда копать дальше.
>
> Для глубокого понимания читать в порядке: [CLAUDE.md](../CLAUDE.md) → этот файл →
> [README.md](../README.md) → [TESTING.md](../TESTING.md) → [sections/17-patterns.md](sections/17-patterns.md).

---

## 1. Краткая сводка

| Параметр                | Значение                                                              |
|-------------------------|-----------------------------------------------------------------------|
| Go-версия               | 1.26                                                                  |
| Тип проекта             | три stateless backend-сервиса (Receiver, Sender, Web) + SPA админка   |
| Архитектура             | Clean Architecture: `handler → usecase → port → adapter`              |
| Главный поток           | `POST /api/v1/request/<team_slug>/{path}` → Web (единый вход, reverse-proxy) → Receiver → gRPC Sender → внешний URL → лог в ClickHouse `nexus_<team_slug>.<table>` |
| Async                   | `POST /api/v1/requestAsync/<team_slug>/{path}` → Web → Receiver → Kafka → Sender-consumer |
| Multi-tenancy           | Phase 10 ✅: команды через `/api/teams`, своя CH-БД per team (`nexus_<slug>`), team-switcher в Topbar, scope в nodes/audit/logs/replay/api_tokens. Legacy URL без слога продолжает работать как default-team. |
| Зависимости              | PostgreSQL 16, Redis 7, ClickHouse 24, Kafka 3.9 (KRaft), Prometheus  |
| Покрытие unit-тестами   | 14 пакетов (domain, crypto, i18n, sentry, receiver/usecase, chlog, web/usecase, metrics, healthcheck, config, clickhouse, reloader, nodecache, sender/usecase, **+ build / httpclient в Phase 7.13, + receiver/http + web/http middlewares в Phase 7.14**) + integration: circuitbreaker (Phase 7.13) |
| SPA-фронт               | React 18 + Vite + TS + Tailwind + TanStack Query + react-i18next, 6 страниц |
| Бинари в `cmd/`         | `receiver`, `sender`, `web`, `loadtest`, `rotate-key`, `echosrv` (тестовый получатель для стенда) |

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
| `/api/v1/request/*` sync с проксированием ответа | ✅ | [internal/receiver/usecase/route.go](../internal/receiver/usecase/route.go), [adapter/in/http/handler.go](../internal/receiver/adapter/in/http/handler.go) |
| `/api/v1/requestAsync/*` async, ответ 200 сразу `{result:true}` / ошибка `{result:false,message}` (#7) | ✅ | [route_async.go](../internal/receiver/usecase/route_async.go), [handler.go](../internal/receiver/adapter/in/http/handler.go) `replyAsyncError`, `classifyDomainError` |
| **`/api/v1/callback/*` (webhook с HMAC-SHA256, §16)** | ✅ Phase 8.1 | [handler.go](../internal/receiver/adapter/in/http/handler.go) `handleCallback`, [webhook_signature.go](../internal/receiver/usecase/webhook_signature.go) `VerifyWebhookSignature`, миграция [0007](../migrations/0007_webhook_signature.up.sql) |
| 404 без префикса `/api/v1/` с подсказкой | ✅ | `Handler.Register` → `r.NoRoute` |
| **Единый вход: Web reverse-proxy `/api/v1/request\|requestAsync\|callback` → Receiver** | ✅ | [internal/web/adapter/in/http/receiver_proxy.go](../internal/web/adapter/in/http/receiver_proxy.go) `RegisterReceiverProxy`, регистрируется в [app.go](../internal/web/app.go) до `SPAFallback`. Без этого боевой путь проваливался в SPA-fallback и возвращал `index.html`. |
| `url_mode = static` / `from_request` + allowlist + wildcard (`*.partner.com`) | ✅ | [urlresolver.go](../internal/receiver/usecase/urlresolver.go) |
| `url_base` исключается из проксируемой query | ✅ | `ResolveURL`: `clean.Del(param)` |
| **Path-passthrough (§39): хвост входящего пути → `target_url`, opt-in флаг `path_passthrough`** | ✅ §39 | [resolver.go](../internal/receiver/usecase/resolver.go) `resolveNode`/`prefixMatch` (longest-prefix + remainder), [urlresolver.go](../internal/receiver/usecase/urlresolver.go) `appendPathSuffix` (`JoinPath`, traversal-safe), миграция [0019](../migrations/0019_node_path_passthrough.up.sql) |
| **Лог-колонки `http_method` (глагол) + `method` (репурпозен → подпуть passthrough) (§39)** | ✅ §39 | [ch_log_schema.go](../internal/domain/ch_log_schema.go), gRPC `request_path`, envelope, [ensure_schema.go](../internal/platform/clickhouse/ensure_schema.go) `EnsureHTTPMethodColumn` |
| **HTTP-метод `ANY` (вх: accept-all; исх: зеркало входящего) (§40)** | ✅ §40 | [enums.go](../internal/domain/enums.go) `HTTPMethodAny`, [route.go](../internal/receiver/usecase/route.go) `methodMatches`/`effectiveOutgoingMethod`, [puller.go](../internal/receiver/usecase/puller.go), миграция [0020](../migrations/0020_node_method_any.up.sql) |
| Все режимы incoming auth (none/basic/token) | ✅ | [auth.go](../internal/receiver/usecase/auth.go) `CheckIncomingAuth` |
| Все режимы outgoing auth (none/basic/token/token_from_request/basic_from_request) | ✅ | [auth_dynamic.go](../internal/receiver/usecase/auth_dynamic.go) `BuildDynamicOutgoingAuth` |
| **Универсальная динамическая авторизация: источник+поле для входа и выхода; умный Bearer; пусто→без auth (§41)** | ✅ §41 | [auth.go](../internal/receiver/usecase/auth.go) `CheckIncomingAuth`(source/field), [auth_dynamic.go](../internal/receiver/usecase/auth_dynamic.go) `buildFromRequest`/`withScheme`, [enums.go](../internal/domain/enums.go) `IncomingAuthSource`, миграция [0021](../migrations/0021_incoming_auth_dynamic.up.sql) |
| **Каталог «полей запроса» `request_fields_catalog` + `/api/request-fields` (§41)** | ✅ §41 | [domain/request_field_catalog.go](../internal/domain/request_field_catalog.go), [usecase](../internal/web/usecase/request_field_catalog.go), [repo](../internal/web/adapter/out/postgres/request_field_catalog_repo.go), [handler](../internal/web/adapter/in/http/request_field_catalog_handler.go), миграция [0022](../migrations/0022_request_fields_catalog.up.sql) |
| **Статус «Down» в Overview = по последнему вызову (gauge `nexus_node_last_request_error`) (§41)** | ✅ §41 | [metrics.go](../internal/platform/metrics/metrics.go) `NodeLastRequestError`, Sender `sender_service.go`/`async.go`, [prometheus/client.go](../internal/web/adapter/out/prometheus/client.go) `NodeLastErrors`, [usecase/metrics.go](../internal/web/usecase/metrics.go) `applyLastErrors`, [Overview.tsx](../web-ui/src/pages/Overview.tsx) `nodeVariant` |
| **Динамическая подгрузка тел логов: превью + срез по рунам + скачивание (§42)** | ✅ §42 | порт [log_reader.go](../internal/web/usecase/port/log_reader.go) `GetByIDPreview`/`GetBodyChunk`, адаптер [clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) (`previewCols`, `bodyColumn` whitelist), [usecase/logs.go](../internal/web/usecase/logs.go), [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) (`Get`→preview, `GetBody`, `GetBodyDownload`, `writeLogReadError`), [routes.go](../internal/web/adapter/in/http/routes.go); integration [clickhouse_bodychunk_test.go](../tests/integration/clickhouse_bodychunk_test.go) |
| **UI логов: превью / «показать весь» (с предупреждением >2 МБ) / «скачать» / «копировать»; `prettyMaybe` без фриза (§42)** | ✅ §42 | [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) (`LogBodyBlock`), [lib/logBody.ts](../web-ui/src/lib/logBody.ts) (+[logBody.test.ts](../web-ui/src/lib/logBody.test.ts)), [types.ts](../web-ui/src/components/node/types.ts) (`request_len`/`response_len`/`LogBodyChunk`), i18n `logs.detail.*` (ru/en) |
| **Бесконечный скролл списка логов (§42.9)** — `useInfiniteQuery`, курсор по времени (`to`=date_request, дедуп по id), без изменений бэка; авто-рефетч по позиции скролла (`atTop`), отключён в Live | ✅ §42.9 | [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) (`useInfiniteQuery`, `infiniteItems` дедуп, `onScroll` низ-детекция, `atTop`, loading/`no_more` строки), i18n `logs.no_more` (ru/en); бэкенд переиспользует курсор `GET /api/nodes/{id}/logs` (`to`/`limit`) — без правок |
| **Истинные размеры тел `request_size`/`response_size` (байты, до усечения) + backfill-миграция + колонка «Ответ» в UI (§42.10)** | ✅ §42.10 | схема [ch_log_schema.go](../internal/domain/ch_log_schema.go) (+2 колонки Int64), [log.go](../internal/domain/log.go), заполнение [send.go](../internal/sender/usecase/send.go) (`len()` до `truncateRunes`; TooLarge/resp==nil → 0), INSERT [chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go), миграция [ensure_schema.go](../internal/platform/clickhouse/ensure_schema.go) `EnsureBodySizeColumns`+`BackfillBodySizes` (вызовы в обоих app.go), SELECT/scan [clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go), DTO [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) (`request_size`/`response_size`, всегда), UI [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) (колонка «Ответ», скобки в заголовках панелей), [format.ts](../web-ui/src/lib/format.ts) `fmtSize` + i18n `logs.size_units`; integration [clickhouse_bodysize_backfill_test.go](../tests/integration/clickhouse_bodysize_backfill_test.go) |
| **Транспорт больших тел: gRPC-лимит 4 МиБ → 64 МиБ (server+client), Kafka producer BatchBytes (§42.8)** | ✅ §42.8 | конфиг [config.go](../internal/platform/config/config.go)/[defaults.go](../internal/platform/config/defaults.go) (`grpc_max_message_bytes`, `sender_grpc.max_message_bytes`); сервер [sender/app.go](../internal/sender/app.go) (`MaxRecvMsgSize`/`MaxSendMsgSize`); клиент [grpcsender/client.go](../internal/receiver/adapter/out/grpcsender/client.go) (`MaxCallRecv/SendMsgSize`); Kafka [producer.go](../internal/platform/kafka/producer.go) (`producerBatchBytes`); тесты [client_largemsg_test.go](../internal/receiver/adapter/out/grpcsender/client_largemsg_test.go) (8 МиБ round-trip + негативный `ResourceExhausted`), [producer_batchbytes_test.go](../internal/platform/kafka/producer_batchbytes_test.go) |
| Исключение служебных значений из проксируемого запроса (§3.5 «Исключение») | ✅ | `buildTokenFromRequest`, `buildBasicFromRequest` |
| Маскирование `***` в логах и Sentry | ✅ | `maskAuthHeader` (dry-run), [sentry/sentry.go](../internal/platform/sentry/sentry.go) `isSensitive` |
| **Методы узла: входящий (enforcement, иначе 405) + исходящий (диктует вызов получателя), деф. POST (#5)** | ✅ | [domain/enums.go](../internal/domain/enums.go) `HTTPMethod`, [domain/node.go](../internal/domain/node.go), миграция [0015](../migrations/0015_node_methods.up.sql), [route.go](../internal/receiver/usecase/route.go) `methodMatches` + `OutgoingMethod`, [route_async.go](../internal/receiver/usecase/route_async.go), [puller.go](../internal/receiver/usecase/puller.go) |
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
| **Durable-retry проваленных батчей через Kafka (§38, заменил NDJSON)** | ✅ §38 | [chlogretry/retrier.go](../internal/sender/adapter/out/chlogretry/retrier.go), [clogwire](../internal/sender/clogwire/clogwire.go), retry-consumer [clog_retry_consumer.go](../internal/sender/adapter/in/kafka/clog_retry_consumer.go) |
| **CH partition-drop housekeeping (§4.3)** | ✅ Phase 5 | [sender/usecase/ch_housekeeping.go](../internal/sender/usecase/ch_housekeeping.go), миграция [0005](../migrations/0005_node_retention.up.sql) |
| Все 20 полей `LogRecord` (включая `attempts_details`) | ✅ | [domain/log.go](../internal/domain/log.go), `INSERT` в `writer.go` |

### §5 Хранилища

| Пункт | Статус | Где |
|---|---|---|
| Таблицы PG: methods, nodes, node_headers, users, user_audit, api_tokens | ✅ | [migrations/0001-0005](../migrations/) |
| `golang-migrate` advisory lock, auto-migrate на старте | ✅ | [platform/pg/migrate.go](../internal/platform/pg/migrate.go) `NewMigrator` |
| `team_id` колонки с DEFAULT 'default' (закладка multi-tenancy v2) | ✅ → ◐ Phase 10.1 | миграция 0002 (legacy) → миграция 0008 (UUID FK на `teams`, см. §16 Phase 10) |
| **`teams`, `user_teams` + FK во всех team-aware таблицах** | ✅ Phase 10.1 | [migrations/0008_multi_tenancy.up.sql](../migrations/0008_multi_tenancy.up.sql); сидинг 'default'-team (`ch_database='nexus_default'`), admin → owner |
| **TeamProvisioner: `CREATE DATABASE nexus_<slug>` атомарно с PG-tx** | ✅ Phase 10.C.1 | [adapter/out/clickhouse/team_provisioner.go](../internal/web/adapter/out/clickhouse/team_provisioner.go), [usecase/team.go](../internal/web/usecase/team.go); при упавшем CH `repo.Delete` откатывает PG-row; имя БД жёстко валидируется regex'ом |
| **Нормализация `nodes.clickhouse_table` → `<team.ch_database>.<table>`** | ✅ Phase 10.C.2 | [usecase/node.go](../internal/web/usecase/node.go) `normalizeCHTable` — write-time префикс при Create/Update; unprefixed `<x>` → `nexus_default.<x>`. Backfill-миграция (0009) удалена как ненужная (стенд greenfield) |
| Up/Down + `make migrate-up`/`-down N=1`/`-status` | ✅ | [Makefile](../Makefile) |
| ClickHouse driver `clickhouse-go/v2`, batch INSERT | ✅ | [platform/clickhouse/clickhouse.go](../internal/platform/clickhouse/clickhouse.go) |
| Kafka admin + producer + consumer (`segmentio/kafka-go`) с автосозданием топиков с retention 7 дней + `retention.bytes=40 ГиБ`/партицию, acks=all, idempotence | ✅ | [platform/kafka/](../internal/platform/kafka/) |
| Redis: NodeCache (TTL 5min), sessions (24h), ratelimit, circuit-breaker | ✅ | [platform/redis/](../internal/platform/redis/), [circuitbreaker/redis.go](../internal/platform/circuitbreaker/redis.go), [ratelimit/redis.go](../internal/platform/ratelimit/redis.go) |
| AES-256-GCM v1-формат `v1:nonce:ct:tag` | ✅ | [platform/crypto/aesgcm.go](../internal/platform/crypto/aesgcm.go) |
| Валидация ENCRYPTION_KEY на старте, exit 1 при невалидном | ✅ | [bootstrap.go](../internal/platform/bootstrap/bootstrap.go) `MustCipher` |
| **`rotate-encryption-key` утилита (идемпотентная)** | ✅ Phase 5 | [cmd/rotate-key/main.go](../cmd/rotate-key/main.go), `make rotate-encryption-key OLD_KEY=… NEW_KEY=…` |

### §6 Метрики

| Пункт | Статус | Где |
|---|---|---|
| `/metrics` Prometheus в каждом сервисе | ✅ | [platform/metrics/metrics.go](../internal/platform/metrics/metrics.go) — изолированный `*prometheus.Registry`, подключается через `a.metrics.Handler()` в каждом `app.go` |
| **`nexus_requests_total` + `nexus_request_duration_seconds`** | ✅ Phase 6.1 | Gin middleware [platform/metrics/gin.go](../internal/platform/metrics/gin.go) — Receiver/Web; gRPC [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) и async [usecase/async.go](../internal/sender/usecase/async.go) — Sender |
| **`nexus_kafka_lag`** | ✅ Phase 6.1 | reporter в [sender/app.go](../internal/sender/app.go) `reportKafkaLag()` — раз в 15 сек снимает `Stats()` со всех consumer-инстансов |
| **`nexus_clickhouse_buffer_size` / `_errors_total` / `_dropped_total` / `_fallback_total`** | ✅ Phase 6.1 | [chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go) обновляет в `append`/`flushTable`/`Write` |
| **HTTP-API метрик для панели (§21)** | ✅ Phase 21.1 (+§21 CH-источник) | Глобальные KPI Overview (incoming/outgoing/errors 24ч) и Kafka-мониторинг — из Prometheus ([adapter/out/prometheus/client.go](../internal/web/adapter/out/prometheus/client.go)). **Per-node метрики — из ClickHouse-логов** ([adapter/out/clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) `NodeKPI`/`NodeChart`): и страница узла (вкладки «Обзор»/«Метрики»), и **throughput рабочего стола** (`NodesOverview`, per-node конкурентно) — один источник, цифры стола = цифры узла, без неточного `increase()`. Usecase [usecase/metrics.go](../internal/web/usecase/metrics.go), порт [port/metrics_provider.go](../internal/web/usecase/port/metrics_provider.go) + [port/log_reader.go](../internal/web/usecase/port/log_reader.go), handler [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go): `GET /api/metrics/overview`, `/api/metrics/nodes`, `/api/metrics/nodes/{id}`. Без CH `NodesOverview` деградирует на Prometheus. |

### §7 Веб-интерфейс

> **Phase 21 (§21):** все экраны §7 переведены на единый визуальный эталон
> ([nexus_ui.html](nexus_ui.html)) — дизайн-токены, UI-kit, app-shell (левый
> сайдбар + топбар), вкладки узла, KPI/графики из API метрик. Подробности —
> [sections/21-ui-redesign.md](sections/21-ui-redesign.md) и разделы 4.11.2/4.11.3 ниже.

| Пункт | Статус | Где |
|---|---|---|
| Login form (cookie nexus_session) | ✅ | [web-ui/src/pages/Login.tsx](../web-ui/src/pages/Login.tsx) |
| Overview (список узлов) | ✅ | [web-ui/src/pages/Overview.tsx](../web-ui/src/pages/Overview.tsx) |
| Node detail с вкладкой Logs (snapshot + SSE live-tail) | ✅ Phase 5 | [pages/NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) |
| Node settings (создание/редактирование, dry-run кнопка) | ✅ Phase 5.1 | [pages/NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) |
| **Полный адрес узла (origin+/api/v1) + кнопка «Скопировать» (#2)** | ✅ | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (поле path + preview), [node/ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx), компонент [ui/CopyButton.tsx](../web-ui/src/components/ui/CopyButton.tsx) |
| **Селекторы методов узла (входящий/исходящий) в форме (#5 UI)** | ✅ | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (карточки Route/Target), preview, ConfigTab |
| **Скролл результата dry-run + адаптивность форм (#3, #9)** | ✅ | [ui/Modal.tsx](../web-ui/src/components/ui/Modal.tsx) (flex-col, тело overflow-y-auto, footer фиксирован), `grid-cols-1 sm:grid-cols-2`/`flex-wrap`/`overflow-x-auto` в формах и таблицах |
| **POST /api/nodes/dry-run** (§7.5.1) с пошаговым отчётом | ✅ Phase 5 | [web/usecase/dry_run.go](../internal/web/usecase/dry_run.go), [http/dry_run_handler.go](../internal/web/adapter/in/http/dry_run_handler.go), UI: [components/DryRunDialog.tsx](../web-ui/src/components/DryRunDialog.tsx) |
| **POST /api/logs/{id}/replay** (§7.4.1) + маркер `__replay_of` + rate-limit 10/мин | ✅ Phase 5 | [usecase/replay.go](../internal/web/usecase/replay.go), [http/replay_handler.go](../internal/web/adapter/in/http/replay_handler.go), [adapter/out/receiver/dispatcher.go](../internal/web/adapter/out/receiver/dispatcher.go), UI: [components/ReplayDialog.tsx](../web-ui/src/components/ReplayDialog.tsx) |
| **SSE live-tail `/api/nodes/{id}/logs/stream`** (§7.4) с heartbeat | ✅ Phase 5 | [usecase/logs.go](../internal/web/usecase/logs.go) `Subscribe`, [http/logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `Stream` |
| **Ленивые тела логов (§7.4.2): list/stream без `request`/`response`, тела по клику** | ✅ Phase QA.2026-06 | `GET /api/nodes/{id}/log/{logId}` ([logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `Get`, [logs.go](../internal/web/usecase/logs.go) `GetByID`, route в [routes.go](../internal/web/adapter/in/http/routes.go)); `toLogDTO(r, includeBodies)` режет тела для списков/SSE. UI: раскрытие строки в [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) (`LogBodies` грузит тело лениво); аудит — ленивый `<pre>` по `onToggle` в [AuditDetailsCell.tsx](../web-ui/src/components/AuditDetailsCell.tsx) |
| Settings → API Tokens | ✅ Phase 5.1 | [pages/settings/ApiTokens.tsx](../web-ui/src/pages/settings/ApiTokens.tsx) |
| Settings → Language / Theme | ✅ Phase 5.1 | [pages/settings/Language.tsx](../web-ui/src/pages/settings/Language.tsx), [Theme.tsx](../web-ui/src/pages/settings/Theme.tsx) |
| Audit log страница | ✅ Phase 5.1 | [pages/AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx) |
| **i18n / Accept-Language (en/ru)** на стороне backend и SPA | ✅ Phase 5 | [platform/i18n/](../internal/platform/i18n/), [web-ui/src/locales/](../web-ui/src/locales/), [web-ui/src/i18n.ts](../web-ui/src/i18n.ts) |
| Auth: users CRUD, sessions Redis, RBAC, must_change_password | ✅ | [web/usecase/auth.go](../internal/web/usecase/auth.go), [adapter/out/redis/session_repo.go](../internal/web/adapter/out/redis/session_repo.go) |
| **Роль `manager` + иерархия рангов (RBAC, §26)** | ✅ Phase A | `viewer<manager<admin` через [domain.UserRole.Rank/AtLeast](../internal/domain/enums.go), middleware `RequireMinRole` ([auth_middleware.go](../internal/web/adapter/in/http/auth_middleware.go)), группа `authedManager` ([routes.go](../internal/web/adapter/in/http/routes.go)), миграция [0013_user_role_manager](../migrations/0013_user_role_manager.up.sql). Менеджер: узлы CRUD/dry-run, каталоги Allowed Hosts/Headers, чтение Audit; не трогает users/teams/общие настройки/CH-шаблоны/move. UI-гейтинг по `minRole` ([web-ui/src/lib/roles.ts](../web-ui/src/lib/roles.ts), [Settings.tsx](../web-ui/src/pages/Settings.tsx)) |
| **Self-service смена своего пароля `POST /api/me/password` (§26.4)** | ✅ Phase A | `AuthUsecase.ChangeOwnPassword` (подтверждение текущего пароля, инвалидация всех сессий) [auth.go](../internal/web/usecase/auth.go), handler [auth_handler.go](../internal/web/adapter/in/http/auth_handler.go), UI [pages/settings/Password.tsx](../web-ui/src/pages/settings/Password.tsx) |
| API-токены: `db_<base64>` префикс, SHA-256 hash, scopes, audit | ✅ | [web/usecase/api_token.go](../internal/web/usecase/api_token.go), [http/api_token_middleware.go](../internal/web/adapter/in/http/api_token_middleware.go) |
| Audit log (CRUD узлов, replay, dry_run, login, token actions) с retention | ✅ | [web/usecase/audit.go](../internal/web/usecase/audit.go), [usecase/housekeeping.go](../internal/web/usecase/housekeeping.go) |
| **IP в аудите/логах нормализуется в IPv4 (`::1`→`127.0.0.1`, IPv4-mapped) (#4)** | ✅ | [platform/clientip/clientip.go](../internal/platform/clientip/clientip.go) `NormalizeIPv4` — применён в Web (`actorFromCtx`, `userActor`, login) и Receiver (`clientIP`) |
| `RequireSessionOnly` для SSE (отклоняет API-токены) | ✅ Phase 5 | [api_token_middleware.go](../internal/web/adapter/in/http/api_token_middleware.go) |
| Полный лейаут §7 (Overview cards, KPI-блоки, ClickHouse-настройки, Users-страница) | ✅ Phase 6.3/6.4 | Settings → Sentry/ClickHouse/Users CRUD с диалогами, live-tail UI с фильтрами и подсветкой |

### §8 Конфигурация

| Пункт | Статус | Где |
|---|---|---|
| `config/config.yml` + `config_debug.yml` + `config.example.yml` | ✅ | [config/](../config/) |
| Env-вставки `${VAR:default}` | ✅ | [platform/config/load.go](../internal/platform/config/load.go) |
| Флаги `--config`, `--debug`, `--version` | ✅ | [platform/config/flags.go](../internal/platform/config/flags.go) |
| **`app_settings` таблица + REST API + overlay поверх env на старте** | ✅ Phase 6.3.1 | миграция [0006](../migrations/0006_app_settings.up.sql), [domain/app_settings.go](../internal/domain/app_settings.go), [usecase/app_settings.go](../internal/web/usecase/app_settings.go), [http/app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go), [bootstrap/app_settings.go](../internal/platform/bootstrap/app_settings.go) |
| **Hot-reload Sentry через Redis pub/sub** | ✅ Phase 6.3.2 | [platform/reloader/](../internal/platform/reloader/), [sentry.Reload](../internal/platform/sentry/sentry.go), [bootstrap/reload.go](../internal/platform/bootstrap/reload.go) — Web публикует на канал `nexus:config:reload`, Receiver/Sender/Web подписаны и переинициализируют SDK |
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
| **Локальный LRU L2-кеш (1-5 сек) + stale-fallback при ошибках downstream** | ✅ Phase 7.2 | [nodecache/lru.go](../internal/receiver/adapter/out/nodecache/lru.go) + [nodecache/l2.go](../internal/receiver/adapter/out/nodecache/l2.go); конфиг `receiver.l2_cache.{enabled,size,ttl_ms,stale_ttl_ms}`; метрики `nexus_l2_cache_{hits,misses,evictions}_total` + `nexus_l2_cache_size` |
| Pool соединений PG/Redis по конфигу | ✅ | `MaxOpenConns`, `PoolSize` в YAML |
| **Graceful shutdown с дренажом CH-буфера и Kafka offset** | ✅ | `App.Stop` в каждом сервисе, `chWriter.Stop(ctx)` |
| **Durable-retry при недоступности CH (§9.4 → §38, Kafka вместо NDJSON)** | ✅ §38 | [chlogretry/retrier.go](../internal/sender/adapter/out/chlogretry/retrier.go) + retry-consumer |
| Circuit breaker per-node в Redis | ✅ | [platform/circuitbreaker/redis.go](../internal/platform/circuitbreaker/redis.go) |
| Rate-limit per-node + per-token | ✅ | [platform/ratelimit/redis.go](../internal/platform/ratelimit/redis.go) |
| `/health` (liveness) + `/ready` (с degraded body) | ✅ | [platform/healthcheck/healthcheck.go](../internal/platform/healthcheck/healthcheck.go) |
| Загрузочный тест 500 rps × 10 мин | ✅ | [cmd/loadtest/main.go](../cmd/loadtest/main.go), `make loadtest` |
| Реалистичный микс трафика (§10.2: async / dynamic-url / auth token+basic / random headers) | ✅ Phase 10.2.A | [cmd/loadtest/nodes.go](../cmd/loadtest/nodes.go) — односценарные узлы по `--ratio-*`, per-mode отчёт |
| No-loss async/rmq (число строк CH = числу отправленных, §10.2) | ✅ Phase 10.2.A.3 (poll-until-stable) | [cmd/loadtest/noloss.go](../cmd/loadtest/noloss.go) — `--ch-addr`, фильтр `type IN (requestAsync,RabbitMQAsync)`; опрос до стабилизации (`--ch-noloss-max-wait`), а не единичный замер |

### §10 Тестирование

| Пункт | Статус | Где |
|---|---|---|
| Unit-тесты domain/crypto/usecase/i18n/sentry/chlog | ✅ Phase 5/5.2 | `*_test.go` в соответствующих пакетах |
| **Integration testcontainers** (Postgres + миграции) | ✅ Phase 5/5.1 | [tests/integration/](../tests/integration/), `make test-integration` |
| Loadtest бинарь с pass/fail-критериями + микс трафика + no-loss | ✅ Phase 10.2.A | [cmd/loadtest](../cmd/loadtest/) — `--ratio-async/-dynamic-url/-auth-token/-auth-basic`, per-mode `modes` в report.json, CH no-loss (`--ch-addr`); единый CI-job `loadtest` гонит полный микс (sync+async+dyn-url+auth+rmq) за один прогон |
| **Полный testcontainers-сетап (PG + Redis + CH + Kafka)** | ✅ Phase 7.3 | PG ([node_repo_test.go](../tests/integration/node_repo_test.go)), Kafka ([receiver_async_test.go](../tests/integration/receiver_async_test.go)), Redis ([redis_test.go](../tests/integration/redis_test.go) — SessionRepo + NodeCache + TTL-expire), CH ([clickhouse_test.go](../tests/integration/clickhouse_test.go) — chlog.Writer batch insert + LogReaderCH `GetByID`/`Search` + table-name SQL-injection guard) |
| **Async end-to-end интеграция через Kafka** | ✅ Phase 6.2 | [tests/integration/receiver_async_test.go](../tests/integration/receiver_async_test.go) — реальный pipeline `RouteAsyncUsecase → Kafka → ConsumerGroup → AsyncProcessor → SendUsecase → mock HTTP` |
| **DLQ-сценарий после retry-exhaustion** | ✅ Phase 9.3 | [tests/integration/sender_dlq_test.go](../tests/integration/sender_dlq_test.go) — mock=500 + узел с `retry_count=2`; отдельный kafka-reader на `nexus.async.dlq` проверяет headers `id` / `node_path` / `orig_topic` / `reason=status=500 attempts=3` / `last_attempt_at` |
| **Replay-сценарий через ClickHouse** | ✅ Phase 9.3 | [tests/integration/replay_test.go](../tests/integration/replay_test.go) — PG+CH; `ReplayUsecase` поверх реального `LogReaderCH` проверяет маркер `__replay_of=<orig_id>` в query, сохранение исходных query-параметров, тело из CH-записи и audit-запись `node.replay` |
| **Auth E2E (login + session + role)** | ✅ Phase 9.3 | [tests/integration/auth_test.go](../tests/integration/auth_test.go) — PG `UserRepoPg` + Redis `SessionRepoRedis`; happy/bad-password/inactive, `Check` продлевает TTL, `ChangePassword` инвалидирует все сессии, audit `user.login.*` / `user.password.change` |
| **Receiver incoming auth через реальный HTTP** | ✅ Phase 9.3 | [tests/integration/receiver_incoming_auth_test.go](../tests/integration/receiver_incoming_auth_test.go) — Gin + `httptest.NewServer`; узлы none/basic/token, проверка 401/200 для отсутствующего/малформенного/неверного/верного `Authorization`, гарантия что 401 не достигает upstream |

### §11 Swagger / OpenAPI

| Пункт | Статус | Где |
|---|---|---|
| Аннотации `@Summary/@Param/...` на ключевых handlers | ✅ Phase 5 | login, nodes (List/Get/Create), dry-run, replay, logs (List/Stream) |
| `make swagger` (через `swag init -g cmd/web/main.go`) | ✅ | [Makefile](../Makefile) |
| `make swagger-drift-check` для CI | ✅ Phase 5 | сравнивает `git diff --exit-code docs/` после регенерации |
| **Полные аннотации на 100% endpoints** | ✅ Phase 7.1 | auth (login/logout/me), nodes (List/Get/Create/Update/Delete), users (List/Get/Create/Update/Delete/ChangePassword), tokens (List/Create/Revoke/Delete), audit (List/ExportCSV), dry-run, replay, logs (List/Stream), settings/app (Get/Update/TestClickHouse/TestSentry), settings/clickhouse/orphans (List/Drop) |
| **Swagger UI handler в Gin** | ✅ Phase 7.1 | `r.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))` в [internal/web/app.go](../internal/web/app.go) + blank-import `_ "nexus/docs/web"` для регистрации генеренного docTemplate в `swag.Registry` |
| **Два дока: Web + Receiver** (§25) | ✅ Phase 25.C | `make swagger` генерирует `docs/web` (instance `swagger`) и `docs/receiver` (instance `receiver`, `--exclude` изоляция); Web раздаёт `/swagger/web/*any` и `/swagger/receiver/*any`, старый `/swagger/index.html` → редирект на web |

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
| **Sentry tracing-middleware для Gin (§14.3)** | ✅ Phase 5 | [platform/sentry/middleware.go](../internal/platform/sentry/middleware.go) — span'ы с тегами service/node/root_method/**request_id** (§30). **Инфра-пути (`/metrics`, `/health`, `/ready`, `""`) пропускаются** (`skipSentry`) — иначе скрейпы Prometheus/healthcheck заваливают Sentry транзакциями; зеркалит пропуск в `metrics.GinMiddleware`. Тесты `TestSkipSentry`, `TestGinMiddleware_SkipsInfraPaths` |
| Обработка паник + `request_id` + версия в UI | ✅ §30 | логгер v1.7.9 (`WithContext`); см. карту §30 ниже и решение §4.27 |
| `app_settings` PostgreSQL singleton + Web UI Sentry | ✅ Phase 6.3 | overlay поверх env, hot-reload Sentry/ClickHouse через Redis pub/sub, test connection (см. §8 строки 125-128) |

### §15 Критерии приёмки

См. [sections/15-acceptance.md](sections/15-acceptance.md). Покрытие: ~95% пунктов реализовано.
Не покрыто (требует Phase 6+):

- Полный Audit log: CSV-экспорт (Phase 6.6), diff-двухколоночный для `node.update` (Phase 6.9) — сделано.
- Live-tail UI: расширенные фильтры сделаны в Phase 6.8 (`q`/`ip`/`host`/`from`/`to` — на backend через `port.LogQuery`).

### §16 Out of scope (явно отложено в v2)

- Multi-tenancy v2 — ✅ **Phase 10 закрыта** (см. подробный список ниже,
  итого 18 коммитов, 7 блоков A→G).
  - ✅ Phase 10.1: миграция 0008 (`teams`, `user_teams`, FK на `nodes`/`users`/`api_tokens`/`user_audit`, `UNIQUE(team_id, path)`, сидинг 'default'-team).
  - ✅ Phase 10.2: `domain.Team`, `TeamRepository` (PG-impl CRUD + membership), `User.TeamID → DefaultTeamID`, резолв UUID 'default'-team в Web-bootstrap и проброс в NodeUsecase/OrphanScanner.
  - ✅ Phase 10.B.1: `Session.CurrentTeamID` в Redis, `APIToken.TeamID`, endpoints `GET /api/me/teams` + `POST /api/me/switch-team` (последний только для session-cookie: API-токены ограничены одной командой). `AuthUsecase` теперь принимает `port.TeamRepo`.
  - ✅ Phase 10.B.2: team-scope в `NodeUsecase.{Get,Update,Delete}` (cross-team → 404), `NodeHandler.Create` подставляет `currentTeamID(c)`, `APITokenUsecase.Create` принимает `teamID` и пишет его в `api_tokens.team_id`.
  - ✅ Phase 10.C.1: `TeamProvisioner` (PG-tx + `CREATE DATABASE nexus_<slug>` атомарно с откатом PG-row), `TeamUsecase` (CRUD + Members), HTTP `/api/teams` (admin-only). Creator → owner. `default`-team удалить нельзя.
  - ✅ Phase 10.C.2: `NodeUsecase` нормализует `clickhouse_table` до `<team.ch_database>.<table>` в Create/Update через `TeamRepo`. Sender и `ch_housekeeping` без изменений — `chlog.Writer` уже принимает `db.table` строкой, `splitDBTable` уже умеет парсить.
  - ✅ Phase 10.C.3: нормализация `nodes.clickhouse_table` до `<team.ch_database>.<table>` — на write-time в `NodeUsecase.normalizeCHTable` (Create/Update). Backfill-миграция 0009 удалена как ненужная (стенд greenfield, узлов со старым/unprefixed форматом нет).
  - ✅ Phase 10.D.1: team-scope в `LogsUsecase.{ListSince,Search,Subscribe}` и `ReplayUsecase.Replay` (cross-team → 404). `DryRunHandler` ставит `n.TeamID = currentTeamID(c)` на узле формы. Handler'ы передают `currentTeamID(c)` во все эти usecase.
  - ✅ Phase 10.D.2: `OrphanScanner` сканирует все `teams.ch_database` (allow-list), `knownTables` собирает узлы всех команд, drop guard разрешает DROP только в tenant-БД. `ch_housekeeping` (Sender) автоматически multi-team — берёт узлы всех команд из PG и идёт по `db.table` через `splitDBTable`.
  - ✅ Phase 10.E.1: `NodeReader.Get(teamSlug, path)` — PG-запрос через `JOIN teams ON nodes.team_id = teams.id WHERE teams.slug=$1 AND nodes.path=$2`, Redis-ключ `node:<team_slug>:<path>`, L2-кеш по `<team_slug>/<path>`. Receiver принимает `/v1/request/<team_slug>/<node_path>` и legacy `/v1/request/<node_path>` (default-team). Parser `splitTeamSlugAndPath` различает 1- и 2-сегментные URL.
  - ✅ Phase 10.E.2: cross-team изоляция в Receiver обеспечивается JOIN'ом из E.1 (чужой `team_slug` → 404, не утечка существования). API-токены в Receiver не используются — incoming auth узла остаётся ответственным за аутентификацию клиента. Фиксируется unit-тестом `TestSplitTeamSlugAndPath` (8 кейсов).
  - ✅ Phase 10.F.1: `user_audit.team_id` реально записывается через `Actor.TeamID` (берётся из сессии в `userActor(c)`/`actorFromCtx(c)`). `AuditFilter.TeamID` + handler-overlay: по умолчанию admin видит только свою команду; `?team_id=*` или `?team_id=<uuid>` — override. CSV-экспорт включает колонку `team_id`.
  - ✅ Phase 10.F.2: SPA — страница `Settings → Teams` (admin-only). `TeamsPanel` (CRUD + delete-guard для `default`), `TeamDialog` (slug immutable после создания, preview `nexus_<slug>`), `MembersDialog` (add/update-role/remove, select из `/api/users` без уже-членов). i18n en/ru.
  - ✅ Phase 10.F.3: Topbar team-switcher — `<select>` со списком из `/api/me/teams`, при смене вызывает `/api/me/switch-team` и `qc.invalidateQueries()` (все списки nodes/audit/logs/tokens перерисовываются под новый scope).
  - ✅ Phase 10.G.1: end-to-end integration-тест `TestMultiTenancy_Isolation_E2E` (testcontainers PG) — 10 свойств: одинаковый path в двух командах, cross-team Get/Update/Delete возвращает 404, ClickHouseTable префиксуется через NodeUsecase, audit-записи несут team_id, FK ON DELETE RESTRICT блокирует удаление team с активными узлами. Прогон ~5 сек.
  - ✅ Phase 11.A: scope пользователей по членству — `ListUsersFilter.TeamID`, `UserRepoPg.List` через `JOIN user_teams` (userCols квалифицированы алиасом `u`, чтобы `created_at` не был ambiguous), `UserUsecase` принимает `TeamRepo` + `defaultTeamID`. `Create` добавляет membership в current_team через `AddMember` (иначе новый юзер не попал бы в scoped-список). Handler передаёт `currentTeamID(c)` в List/Create. Полное ТЗ — §18, см. [sections/18-multi-tenancy.md](sections/18-multi-tenancy.md).
  - ✅ Phase 11.A-fix: `ListMembers` (участники команды) обогащается `login`/`email` через `JOIN users` ([team_repo.go](../internal/web/adapter/out/postgres/team_repo.go), поля в [domain.TeamMember](../internal/domain/team.go), `login`/`email` в `teamMemberResponse`). Раньше API отдавал только `user_id`, а SPA резолвил логин из team-scoped `/api/users` — для участника, которого нет в **текущей** команде (напр. owner `admin` в Default Team при активной команде `vika`), показывался сырой UUID. Теперь `MembersDialog` рендерит `login` (+ email) прямо из ответа ([Teams.tsx](../web-ui/src/pages/settings/Teams.tsx)).
  - ✅ Phase 11.B: перенос узла между командами — `POST /api/nodes/:id/move {target_team_slug}` (admin-only). `NodeUsecase.Move`: PG-перенос (team_id + clickhouse_table rebase на БД целевой команды) в UoW-транзакции + audit `node.move`; конфликт пути → `ErrNodeAlreadyExists` (409); перенос в свою команду → 403; чужой узел → 404. CH-таблица логов следует за узлом через `TeamProvisioner.RenameTable` (RENAME TABLE old_db.tbl TO new_db.tbl, best-effort: при отсутствии исходной таблицы — `ErrSourceTableAbsent`, пропуск). UI: кнопка «Move» на Overview + диалог выбора команды. Integration-тест `TestMultiTenancy_NodeMove_E2E`.
  - ✅ Phase 11.C: Redis ACL — `RedisSection.Username` (yaml `username`), проброшен в `goredis.Options.Username`. `config.example.yml`: `username: ${REDIS_USER:}`; `.env.example`: `REDIS_USER=`. Пустое значение = default-юзер (обратная совместимость). `config_debug.yml` не трогается (для локального ACL-Redis добавить `username: <user>` вручную).
  - ✅ Phase 11.D: multi-tenancy зафиксирована как ТЗ §18 — новый раздел [sections/18-multi-tenancy.md](sections/18-multi-tenancy.md) (модель данных, CH-БД per team, scope, Receiver URL, перенос узла, UI), пункт в `16-out-of-scope.md` помечен реализованным со ссылкой на §18, строка в `sections/README.md`, синхронизирован сводный `nexus_spec.md`. В `CLAUDE.md` — правило: новая крупная фича → новый раздел в `specs/sections/`.
  - ✅ Phase 11.E: список пользователей де-scoped до **глобального** — `/api/users` (admin-only) возвращает всех пользователей, а не участников текущей команды. Снято в [user_handler.go](../internal/web/adapter/in/http/user_handler.go) (`List` больше не шлёт `currentTeamID(c)`) и [user.go](../internal/web/usecase/user.go) (`List` не подставляет `defaultTeamID`). SQL не менялся — `UserRepoPg.List` уже делает `JOIN user_teams` только при непустом `TeamID`. `Create` по-прежнему добавляет membership в текущую команду (не менялся). Фронт не трогался: `Settings → Users` и пикер `MembersDialog` (query `users-all`) автоматически получают всех (бонус: теперь в команду можно добавить любого не-члена). Причина: пользователь — глобальная сущность, членство в командах — отдельная ось; team-scope списка заставлял «искать людей по командам». Тест `TestUserUC_List_Global_NoTeamScope`. ТЗ — §18.3.
- ~~Webhook signature verification (`/v1/callback/`)~~ — реализовано в Phase 8.1.
- ~~OpenTelemetry distributed tracing~~ — реализовано в Phase 8.2 (HTTP-server-span'ы) + 8.3 (HTTP outbound + gRPC unary client/server interceptor'ы) + 8.4 (Kafka headers propagation для async-пути). End-to-end trace через UI → Web → Receiver → {gRPC → Sender → внешний URL} / {Kafka → Sender-consumer → внешний URL}.
- ◐ Notifications для операторов — **Telegram реализован (Phase F2, см. §20)**;
  Slack/generic-webhook и доп. триггеры остаются расширением.
- Шаблоны узлов (предзаполненный конфиг узла — НЕ путать с §19 «шаблоны
  CH-таблиц», которые реализованы).
- Версионирование конфигов узла + откат.
- Bulk-операции, импорт/экспорт.
- Mutating API tokens.
- KMS/Vault интеграция.
- OpenTelemetry.

### §19 Шаблоны запросов ClickHouse

Полный ТЗ-раздел — [sections/19-ch-templates.md](sections/19-ch-templates.md).

| Пункт | Статус | Где |
|---|---|---|
| Единый источник 20 колонок | ✅ Phase F1.1 | [domain/ch_log_schema.go](../internal/domain/ch_log_schema.go) `RequiredLogColumns` |
| Доменная модель шаблона + Validate (белые списки CODEC/index/partition) | ✅ Phase F1.1 | [domain/ch_template.go](../internal/domain/ch_template.go), рендер [ch_template_render.go](../internal/domain/ch_template_render.go) |
| Таблица `ch_templates` (JSONB spec, partial-unique default) + сид «Standard logs» + `nodes.clickhouse_template_id` | ✅ Phase F1.2 | [migrations/0009](../migrations/0009_ch_templates.up.sql), [postgres/ch_template_repo.go](../internal/web/adapter/out/postgres/ch_template_repo.go) |
| `TeamProvisioner.CreateTable` / `VerifyTemplate` (live temp create+drop) | ✅ Phase F1.3 | [clickhouse/team_provisioner.go](../internal/web/adapter/out/clickhouse/team_provisioner.go) |
| CHTemplateUsecase (CRUD+audit, delete-guard, verify) + handler + routes | ✅ Phase F1.4 | [usecase/ch_template.go](../internal/web/usecase/ch_template.go), [http/ch_template_handler.go](../internal/web/adapter/in/http/ch_template_handler.go) |
| Авто-создание таблицы при Create/Update узла + DTO + UI (селектор + панель управления) | ✅ Phase F1.5 | [usecase/node.go](../internal/web/usecase/node.go) `provisionTable`, [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx), [CHTemplatesPanel.tsx](../web-ui/src/components/CHTemplatesPanel.tsx) |
| Локализованные ошибки валидации шаблона (i18n-`code` + перевод в языке UI, inline-вывод у полей) | ✅ Phase F1.6 | [http/ch_template_handler.go](../internal/web/adapter/in/http/ch_template_handler.go) `chTemplateErrorCode`/`chTemplateValidationCode`, [i18n.go](../internal/platform/i18n/i18n.go) ключи `ch_template.*`, [CHTemplatesPanel.tsx](../web-ui/src/components/CHTemplatesPanel.tsx) |

### §20 Уведомления операторам (Telegram)

Полный ТЗ-раздел — [sections/20-notifications.md](sections/20-notifications.md).

| Пункт | Статус | Где |
|---|---|---|
| Настройки `notifications.telegram` в app_settings (mask/merge/cron-валидация) + **фикс marshal'а Update** + reloader-секция | ✅ Phase F2.1 | [domain/app_settings.go](../internal/domain/app_settings.go), [usecase/app_settings.go](../internal/web/usecase/app_settings.go), [postgres/app_settings_repo.go](../internal/web/adapter/out/postgres/app_settings_repo.go), [reloader.go](../internal/platform/reloader/reloader.go) |
| Telegram-клиент (sendMessage) | ✅ Phase F2.2 | [platform/telegram/client.go](../internal/platform/telegram/client.go) |
| ~~`LogReader.CountErrors`~~ → **§22: `PromMetrics.NodeErrors` (Prometheus)** + Redis checkpoint + distributed lock | ✅ Phase F2.3 / 22.4 | источник ошибок переведён на Prometheus (`nexus_request_incomplete_total`), см. §22; [redis/notif_checkpoint.go](../internal/web/adapter/out/redis/notif_checkpoint.go), [redis/notif_lock.go](../internal/web/adapter/out/redis/notif_lock.go) |
| NotificationScheduler (cron, окно ошибок, send-if>0, hot-reload) | ✅ Phase F2.4 | [usecase/notification.go](../internal/web/usecase/notification.go), dep `robfig/cron/v3` |
| Wiring + `POST /api/settings/notifications/test` | ✅ Phase F2.5 | [usecase/settings_tester.go](../internal/web/usecase/settings_tester.go) `TestTelegram`, [app.go](../internal/web/app.go) |
| UI Settings → Notifications | ✅ Phase F2.6 | [pages/settings/Notifications.tsx](../web-ui/src/pages/settings/Notifications.tsx) |

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

### §22 Контроль логирования узла, обрезка тел, карточки Overview, Telegram→Prometheus

ТЗ — [sections/22-logging-controls-cards.md](sections/22-logging-controls-cards.md). Ветка
`feature/logging-controls-cards`.

| Пункт | Статус | Где |
|---|---|---|
| Поля узла `logging_enabled` / `max_body_size_enabled` / `max_body_size` | ◐ Phase 22.1 | миграция [0010](../migrations/0010_node_logging_controls.up.sql), [domain/node.go](../internal/domain/node.go), [postgres/node_repo.go](../internal/web/adapter/out/postgres/node_repo.go), [dto.go](../internal/web/adapter/in/http/dto.go), proto [sender.proto](../proto/sender/v1/sender.proto) |
| Проводка полей Node → Sender (sync gRPC + async PG) | ◐ Phase 22.1 | [route.go](../internal/receiver/usecase/route.go), [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go), [async.go](../internal/sender/usecase/async.go), readers [nodepg](../internal/sender/adapter/out/nodepg/reader.go) / [nodecache](../internal/receiver/adapter/out/nodecache/reader.go) |
| Отключение логирования + обрезка по символам в Sender (§22.2, актуально) | ✅ Phase 22.2 | [send.go](../internal/sender/usecase/send.go) (`truncateRunes`, guard на `Write`); тесты [send_test.go](../internal/sender/usecase/send_test.go) + integration [clickhouse_test.go](../tests/integration/clickhouse_test.go) |
| **§43-rev: лимиты размера тела — `max_body_size` режет только лог (§22.2); транспортные лимиты из конфига → 413/502** | ✅ §43-rev | request > `receiver.max_body_bytes` → 413 ([receiver/.../handler.go](../internal/receiver/adapter/in/http/handler.go) `errBodyTooLarge`/`replyReadBodyError`); response > `sender.grpc_max_message_bytes` → 502 memory-safe ([httpclient/client.go](../internal/sender/adapter/out/httpclient/client.go) `LimitReader`+`TooLarge`, [send.go](../internal/sender/usecase/send.go)); проводка [sender/app.go](../internal/sender/app.go); тесты send_test/httpclient/handler + integration [clickhouse_largebody_test.go](../tests/integration/clickhouse_largebody_test.go) |
| **§44: счётчики дашборда — шапка = Σ строк таблицы (CH/уникальные), не Prometheus (попытки); период 24ч+localStorage; тоггл/интервал автообновления; %ошибок по узлу; diagnostics API; дата в логах; колонка «Команды»** | ✅ §44 | usecase [metrics.go](../internal/web/usecase/metrics.go) (`NodesOverview.Totals`/`sumTotals`, `Overview`→Kafka, `Diagnostics`), [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go) (`totals`, `Diagnostics`)+[routes.go](../internal/web/adapter/in/http/routes.go); app_settings `MetricsRefetchMs` ([domain/app_settings.go](../internal/domain/app_settings.go), [usecase/app_settings.go](../internal/web/usecase/app_settings.go), public в [app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go)); users teams [team_repo.go](../internal/web/adapter/out/postgres/team_repo.go) `ListTeamsByUsers`, [user.go](../internal/web/usecase/user.go) `ListWithTeams`; фронт [Overview.tsx](../web-ui/src/pages/Overview.tsx), [period.ts](../web-ui/src/lib/period.ts), [useNodeMetrics.ts](../web-ui/src/components/node/useNodeMetrics.ts), [format.ts](../web-ui/src/lib/format.ts) `fmtLogTs`, [Users.tsx](../web-ui/src/pages/settings/Users.tsx); скил `.claude/skills/nexus-prod`. **§44 keyset-пагинация логов** (`before_id`, курсор `(date_request, ID)` — фикс «не двигает вниз» на плотных секундах): [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) `Search`, [port/log_reader.go](../internal/web/usecase/port/log_reader.go) `LogQuery.BeforeID`, [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go), [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx); integration `TestClickHouse_KeysetPagination_DenseSecond_E2E`. **§44.L режим подсчёта уникальных** (`metrics_approx_counts`, дефолт точно): `NodeKPI(…, approx)` countDistinct↔uniq — [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go), [metrics.go](../internal/web/usecase/metrics.go) `approxCounts`, [General.tsx](../web-ui/src/pages/settings/General.tsx) тоггл. Тесты metrics/auth usecase + db_test/node_repo integration |
| **§18.9: резолв current_team по членству при входе (боевой баг); §44 Sentry NEXUS-7: невалидный UUID `:id`→404** | ✅ | [auth.go](../internal/web/usecase/auth.go) (`resolveLoginTeam`, `MyTeamsAndCurrent`/`healed`), [team_repo.go](../internal/web/adapter/out/postgres/team_repo.go) (`RemoveMember` переназначает default), [Topbar.tsx](../web-ui/src/components/Topbar.tsx); `isInvalidUUID` ([db.go](../internal/web/adapter/out/postgres/db.go)) в node/user/team `scanRow` |
| **§45: смена команды по умолчанию пользователя прямо в списке (клик по чипу, валидация членства)** | ✅ §45 | `PUT /api/users/:id/default-team` ([routes.go](../internal/web/adapter/in/http/routes.go), [user_handler.go](../internal/web/adapter/in/http/user_handler.go) `SetDefaultTeam`); usecase [user.go](../internal/web/usecase/user.go) `SetDefaultTeam` (валидация членства `ListUserTeams`, audit); repo [user_repo.go](../internal/web/adapter/out/postgres/user_repo.go) `UpdateDefaultTeam` + порт; `ErrUserNotTeamMember` ([errors.go](../internal/domain/errors.go)); фронт [Users.tsx](../web-ui/src/pages/settings/Users.tsx) (чипы-кнопки + `setDefaultTeam` mutation), i18n `teams.set_as_default`. Тесты `TestUserUC_SetDefaultTeam_*` |
| **§46: персистентный статус «Down» узла через Redis (переживает рестарт; §41-гаудж in-memory терялся при деплое)** | ✅ §46 | writer (Sender): `usecase.NodeStatusWriter` + [platform/nodestatus](../internal/platform/nodestatus/redis.go) (`RedisWriter`/`Noop`, ключ `nexus:node:last_error:<path>`, TTL 30д), вызовы в [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) (sync) и [async.go](../internal/sender/usecase/async.go) рядом с `SetNodeLastRequestError`, обвязка [sender/app.go](../internal/sender/app.go). reader (Web): `port.NodeStatusReader` + [adapter/out/redis/node_status.go](../internal/web/adapter/out/redis/node_status.go) (MGET); двухисточниковый `applyLastErrors` (Redis приоритет, Prometheus fallback) + инъекция в [metrics.go](../internal/web/usecase/metrics.go)/[app.go](../internal/web/app.go). Анализ §46.5: персистентности требует только `nexus_node_last_request_error`. Тесты: usecase (приоритет/fallback/деградация) + integration `TestNodeStatus_WriterReader_Redis_E2E` |
| **§47: доработка фильтра логов узла и деталей лога (frontend-only, без Go-кода)** | ✅ §47 | §47.1 клик по столбцу графика по сегментам ([TrafficChart.tsx](../web-ui/src/components/ui/TrafficChart.tsx): красный сегмент → `onlyErrors:true`, синий → `false`; min-высота красного 2px; было `onlyErrors: errors>0` → бакет с любой ошибкой открывал только ошибки). §47.2 поле «Параметры» в детали лога ([LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) `LogBodies`: блок над телами, `prettyMaybe`+`CopyButton`, показ только при непустом `parameters`; поле уже отдаётся [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `toLogDTO`, тип `LogDetail`); i18n `logs.detail.parameters` ru/en. §47.3 убран per-node процент ошибок из [Overview.tsx](../web-ui/src/pages/Overview.tsx) (удалён `nodeErrPct`, проп `CardStat.sub`; частичный реверс §44.D — общий `error_rate` шапки сохранён). §47.4 кликабельный `Sparkline` на карточке дашборда ([Overview.tsx](../web-ui/src/pages/Overview.tsx) — проп `onOpenLogs`, `useNavigate` → `/nodes/:id?tab=logs&from&to`, только при `clickhouse_table`); [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) читает query через `useSearchParams` → стартовая вкладка «Логи» + `LogsInitialFilter` (`status=all`, `msToDatetimeLocal`). ТЗ [47-logs-filter-refinements.md](sections/47-logs-filter-refinements.md); пересобран embed-бандл `internal/web/static/` |
| **§48: расширенный поиск по логам узла (log-search v2)** | ✅ §48 | ТЗ [48-log-search-extended.md](sections/48-log-search-extended.md), ветка `feature/log-search-extended`. Phase 48.1: пакет [domain/logsearch](../internal/domain/logsearch/logsearch.go) — парсер мини-языка (`&`/`\|`/`-`/префиксы `url: params: req: resp:`, `\`-экранирование) + режимы Aa/ab\|/.* (`Options`), AST `Expr` (OR из AND-групп `Term`) и in-memory матчер `Expr.Match(url, params, req, resp)` — единая семантика для SQL CH-адаптера и зеркала live-tail; ошибки → sentinel `ErrBadQuery` (→400). Table-driven тесты [logsearch_test.go](../internal/domain/logsearch/logsearch_test.go) (грамматика/экранирование/ошибки/матчинг, кириллический case-fold). Phase 48.2: `LogQuery` + `Method`/`QCase`/`QWord`/`QRegex`/`QExpr` ([port/log_reader.go](../internal/web/usecase/port/log_reader.go) — адаптер читает ТОЛЬКО `QExpr`, сырое `Q` парсит usecase); SQL-генератор [log_search_sql.go](../internal/web/adapter/out/clickhouse/log_search_sql.go) (`exprConds`/`termCond`: plain → `position[CaseInsensitive]UTF8`, word/regex → `match()` с готовым Pattern; field-scoped терм читает одну колонку — тела не сканируются, §48.5; whitelist колонок, позиционные `?`); `Search` в [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) — `method = ?` + `exprConds` (работает поверх listCols-алиасов). Integration `TestClickHouse_SearchExtended_E2E` ([clickhouse_test.go](../tests/integration/clickhouse_test.go)): parameters-поиск, & \| -, префиксы, Aa/ab\|/.*, кириллица, method exact. Phase 48.3: `parseSearch` в [usecase/logs.go](../internal/web/usecase/logs.go) — разбор Q → QExpr в `Search`/`Subscribe` ДО `resolveNode` (400 приоритетнее деградации); зеркало `matchLogFilter` — ветка `Method` + `QExpr.Match(url, parameters, request, response)`; задокументировано ограничение live-tail §48.6 (тела из `ListSince` пустые). Тесты: `TestLogs_Search_BadQuery` (ErrBadQuery до ErrNodeNotFound), `TestLogs_Search_FillsQExpr`, table-driven `TestMatchLogFilter_Extended`; SQL-генератор — unit `TestExprConds` ([log_search_sql_test.go](../internal/web/adapter/out/clickhouse/log_search_sql_test.go): И/ИЛИ/НЕ/поле/режимы, точные строки WHERE + порядок args); сценарные комбинации в integration (4-я запись 500/done=0: НЕ со скоупом, 3 OR-группы, тройное И, (И НЕ)\|поле, экранированный `\&`, q+Status/Done/Method). Phase 48.4: HTTP — `logQueryFromContext` парсит `method`/`q_case`/`q_word`/`q_regex` (`boolFlag`: `1`/`true`) в [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go); `List` мапит `logsearch.ErrBadQuery` → 400 + i18n `error.bad_search_query` ([i18n.go](../internal/platform/i18n/i18n.go) en/ru), `Stream` → SSE event `error` (EventSource не читает тело 4xx; UI всегда сначала делает snapshot и покажет 400 из List); swagger-аннотации List/Stream обновлены (`make swagger`). Тесты: `TestLogsList_SearchParams_Parsed` (прокидывание до адаптера + QExpr), `TestLogsList_BadSearchQuery_400` (4 варианта невалидного q). Phase 48.5: фасеты — `port.LogReader.DistinctMethods`/`DateRange`; адаптер ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)): `SELECT DISTINCT method … method != '' ORDER BY LIMIT` (кап 200; method — 3-я компонента ORDER BY-ключа) и `min/max(date_request)+count()` (count — guard: min() по пустому набору в CH = epoch-1970, при 0 строк → 0/0); usecase `Methods`/`DateRange` через `resolveNode`; эндпоинты `GET /nodes/:id/logs/{methods,date-range}` (scope `logs:read`, деградация 200+флаги через общий `writeFacetError`), DTO `LogMethodsResponse`/`LogDateRangeResponse` ([dto_common.go](../internal/web/adapter/in/http/dto_common.go)), [routes.go](../internal/web/adapter/in/http/routes.go), swagger. Тесты: usecase `TestLogs_Facets_ForwardAndScope`, handler `TestLogsFacets_OK`/`_CHUnavailable_Degrades`, integration (сортировка/дедуп/без пустых; пустая таблица → 0/0). Phase 48.6: UI — [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx): два ряда (Поиск с тумблерами Aa/ab\|/.* внутри поля + `LabelHint`-подсказка полей/синтаксиса + Method; ниже даты С/По + Reset/Apply), IP/Host удалены из формы (initForm/params/SSE/reset/локали — API-параметры живы), SSE шлёт `q_case/q_word/q_regex/method` (deps эффекта — объект `appliedFilters`), 400 → `logs.advanced.bad_query` под полем (per-query retry: 4xx не ретраить), даты: `onFocus` → `/logs/date-range` каждый фокус → атрибуты min/max (`msToDatetimeLocal`); **новый** [LogMethodFilter.tsx](../web-ui/src/components/node/LogMethodFilter.tsx) — Popover+cmdk combobox, `useQuery {enabled: open, staleTime: 0, gcTime: 0}` (fresh при каждом открытии), пункт «Любой», клиентская cmdk-фильтрация ≤200; i18n `logs.advanced.*` en/ru синхронно (method*, hint_*, *_tooltip, bad_query; удалены ip/host). Бандл пересобран → `internal/web/static/`. Phase 48.7 (гейты сдачи): полный `make test-integration` ✅, `go test -race` в docker golang:1.26 (все пакеты, `GOTOOLCHAIN=auto` — образ 1.26.4 < go.mod 1.26.5) ✅, golangci-lint 0 issues, браузерный прогон на стенде `services` (Playwright): поиск по parameters (`params:debug` → 1 строка), `grumpy \| timeout` → 2, `orders & -grumpy` (негация исключает), Aa (`GRUMPY` → 0), ab\| (`cat` не матчит concatenate), regex `time(out\|r)` → 1, невалидный `(` → 400 + сообщение под полем, Method-дропдаун лениво (health/v1/orders/v1/parcels) + фильтрация, min/max дат при фокусе обновляются после новых логов, live-tail с `params:live-tag` фильтрует SSE. **Грабли стенда**: тела request/response ищутся только если у узла включены `log_request_body`/`log_response_body` (§22) — иначе в CH пустые строки и «поиск не находит» (это конфигурация, не баг); `parameters` пишется всегда. Phase 48.8 (доработка UI по фидбеку): нативный datetime-local заменён календарём `react-day-picker` — новый [LogDateField.tsx](../web-ui/src/components/node/LogDateField.tsx) (Radix Popover + DayPicker single, локаль ru/enUS, поле времени, пресеты Сегодня/Вчера, min/max из `/logs/date-range` при каждом открытии). **Клик по дню сразу применяет дату+время и закрывает поповер**; правка времени при уже выбранной дате применяется на лету; дефолт времени «С» 00:00 / «По» 23:59. Значение — прежний `YYYY-MM-DDTHH:mm` (backend не тронут). Компактные размеры через CSS-переменные `--rdp-*` инлайн на `.rdp-root` (наследование с родителя перебивается `style.css`); цвета из токенов `--accent`/`--fg` (light/dark). Единая npm-зависимость `react-day-picker@10` (согласована), `package-lock.json` закоммичен; браузерная проверка на стенде (клик=выбор+закрытие, ru-локаль, тёмная тема, консоль 0 ошибок). Подпись полей поиска приведена к единым английским именам колонок `URL / parameters / request / response` (плейсхолдер + подсказка, обе локали) |
| **§49: избранные команды (звёзды в свитчере, секция в сайдбаре, DnD-порядок, имя команды без slug)** | ✅ §49 | ТЗ [49-favorite-teams.md](sections/49-favorite-teams.md), ветка `feature/favorite-teams`. Phase 49.1: миграция [0023](../migrations/0023_user_team_favorites.up.sql) — `user_team_favorites(user_id, team_id, position)` с **составным FK на `user_teams(user_id, team_id)` ON DELETE CASCADE** (один каскад покрывает удаление пользователя/команды/исключение из членства + БД-инвариант «избранное ⊆ членство»); порт [port/favorite_team_repo.go](../internal/web/usecase/port/favorite_team_repo.go) (`FavoriteTeamRepo` — отдельный малый интерфейс, НЕ расширение `TeamRepo`: стабы существующих тестов не ломаются); impl на `TeamRepoPg` ([team_repo.go](../internal/web/adapter/out/postgres/team_repo.go) `ListFavoriteTeamIDs` — JOIN user_teams + ORDER BY position, `ReplaceFavoriteTeams` — tx DELETE+INSERT, position=индекс, FK 23503 → `ErrUserNotTeamMember`); `domain.ActionUserFavoriteTeams` ([audit.go](../internal/domain/audit.go)). Integration [team_favorites_repo_test.go](../tests/integration/team_favorites_repo_test.go): roundtrip порядка, перезапись, очистка пустым списком, не-член → ошибка (атомарность), каскады RemoveMember/Delete team. Phase 49.2: избранное едет в `GET /api/me/teams` (`favorites: [id...]` — один источник истины для UI, второй запрос не нужен; DTO `MyTeamsResponse.Favorites`+`Healed` в [dto_common.go](../internal/web/adapter/in/http/dto_common.go)); запись — `PUT /api/me/favorite-teams` `{team_ids:[...]}` full-replace (add/remove/reorder атомарно, пустой массив = очистить; `RequireSessionOnly`); usecase [auth.go](../internal/web/usecase/auth.go) — builder `WithFavoriteTeams` (паттерн `WithLoginRateLimit`, nil → фича выключена), `FavoriteTeamIDs` (**никогда не ошибка** — деградация в `[]`, избранное не должно валить MyTeams), `SetFavoriteTeams` (лимит 100 / дубликаты → `ErrFavoriteTeamsInvalid` ([errors.go](../internal/domain/errors.go)), членство → `ErrUserNotTeamMember`, аудит `user.favorite_teams.update`); handler `SetFavoriteTeams` в [auth_handler.go](../internal/web/adapter/in/http/auth_handler.go) (bind `omitempty,dive,uuid` — **без `required`, иначе пустой массив = 400**), route [routes.go](../internal/web/adapter/in/http/routes.go), wiring [app.go](../internal/web/app.go) (`TeamRepoPg` реализует оба порта). Swagger перегенерирован. Unit `TestAuthUC_SetFavoriteTeams`/`TestAuthUC_FavoriteTeamIDs`. Phase 49.3: общий слой [lib/teams.ts](../web-ui/src/lib/teams.ts) (типы + `useMyTeams`/`useSwitchTeam`/`useSetFavoriteTeams`/`invalidateTeamScoped`, вынесено из Topbar — сайдбар переключает команду с той же семантикой инвалидации; `useSwitchTeam.onError` 403 → invalidate `me-teams` (протухшая избранная самоизлечивается); `useSetFavoriteTeams` — optimistic update с откатом); [TeamSwitcher.tsx](../web-ui/src/components/TeamSwitcher.tsx) — кастомный Popover вместо нативного `<select>` (триггер = **имя команды без slug**, §49.1; строка = кнопка выбора + отдельная кнопка-звезда — соседние, не вложенные, без stopPropagation); [Topbar.tsx](../web-ui/src/components/Topbar.tsx) переведён на общие хуки (healed-эффект §44.H остался в нём — один mount); i18n `teams.*` + `nav.favorites` en/ru. Phase 49.4: [SidebarFavorites.tsx](../web-ui/src/components/SidebarFavorites.tsx) — секция «Избранное» в [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx) (между основным nav и подвалом; 0 избранных → скрыта целиком; `max-h-[40vh] overflow-y-auto`); клик = `useSwitchTeam` (no-op на текущей), текущая подсвечена; DnD на `@dnd-kit/{core,sortable,utilities,modifiers}`: `PointerSensor` c `activationConstraint {distance: 6}` (клик ≠ 0px-драг) + `draggedRef`, сбрасываемый макротаском (click после drop гасится), `restrictToVerticalAxis`+`restrictToParentElement`, `onDragEnd` → `arrayMove` → PUT полного списка (optimistic — без мигания); `useSortable` в отдельном `FavoriteItem` (хук нельзя в map). Бандл пересобран → `internal/web/static/`. Phase 49.5: ТЗ оформлено — [49-favorite-teams.md](sections/49-favorite-teams.md), строка в [sections/README.md](sections/README.md), синхронизирован сводный [nexus_spec.md](nexus_spec.md). Phase 49.6 (гейты сдачи): полный `make test-integration` ✅, полный `go test -race` в docker golang:1.26 ✅, браузерный прогон на стенде `services` (Playwright, только Web-сервис): имя команды без slug в Topbar и списке; звёзды → PUT 200; секция «Избранное» (порядок/подсветка текущей); клик по избранной → switch-team 200 + смена Topbar; DnD reorder → PUT + порядок переживает reload; клик после драга не перехватывается; пустой список → секция скрыта; каскад при исключении из членства (звезда ttt → RemoveMember → пропала из favorites/списка); негативные PUT: дубликат/не-член → 400, пустой → 200; аудит `user.favorite_teams.update` виден в `/api/audit`. **Грабли Playwright+dnd-kit**: `dragTo` роняет элемент «на себя» (live-sorting сдвигает rect цели) — драг эмулировать ручными `mouse.move` шагами к ВЕРХНЕЙ части исходного rect цели |
| **§51: управление логированием сервисов (консоль «Логи», runtime-уровень)** | ✅ §51 | ТЗ [51-service-logging.md](sections/51-service-logging.md) — консоль follow-tail отдельным пунктом сайдбара (admin), логи всех трёх сервисов через Redis-кольцо `nexus:logs:<service>`, runtime-смена уровня (`LevelVar` в собственной сборке хендлеров + `app_settings.logging.level` + reload во всех сервисах), скачивание в файл, тестовый контур §51.8. Блоки 51.0–51.8; ветка `feature/service-logging`. **Phase 51.0**: ТЗ §51.8 (тестовый контур и аудит покрытия). **Phase 51.1**: пакет [platform/sensitive](../internal/platform/sensitive/sensitive.go) (`Keys`/`IsSensitive` — вынос из sentry, единый источник маскирования; sentry делегирует); пакет [platform/logsink](../internal/platform/logsink/ring.go) — `RingHandler` (slog.Handler: `Enabled` по `*slog.LevelVar`, маскировка sensitive-атрибутов вкл. группы, кап значений 8КБ, кольцо ~2000, неблокирующий канал к шипперу, дроп+счётчик при переполнении; клоны WithAttrs/WithGroup разделяют core), [shipper.go](../internal/platform/logsink/shipper.go) (`BatchWriter`-порт, батч 64/тик 1с, паника писателя → ошибка, ошибки → stderr напрямую — НЕ slog, иначе рекурсия; дослача хвоста на ctx.Done), [redis_writer.go](../internal/platform/logsink/redis_writer.go) (pipeline LPUSH+LTRIM 2000+EXPIRE 1ч). Тесты: [ring_test.go](../internal/platform/logsink/ring_test.go) (маскировка top-level/группы, перезапись кольца, дроп, неблокируемость 10k, конкурентность), [shipper_test.go](../internal/platform/logsink/shipper_test.go) (батч/тик/key-cap-ttl/хвост при отмене/паника/ошибки не блокируют), goleak TestMain. **Phase 51.2**: собственная сборка цепочки хендлеров вместо вендорного `Initlogger` — [bootstrap/logger.go](../internal/platform/bootstrap/logger.go): `buildBaseHandler` (parity: tint/colorable "15:04:05" ИЛИ JSON-файл app.log c fallback stderr; writer параметром, closer для тестов Windows), `assembleChain` (MultiHandler: base+ring+Sentry), `buildLogger` (LevelVar из cfg.Logging.Level 2..5, Sentry-порог свой — от LevelVar не зависит), `LogController{Level, Ring}` + `StartRedisShipper` (nil-safe, once, done-канал); re-export'ы `NewLogger`/`NewMultiHandler`/`SentryHandler`/`OutputLogFile` в [platform/logging](../internal/platform/logging/logging.go). `bootstrap.Init` возвращает 5-е значение `*LogController` → правки 4× `cmd/*/main.go` (вкл. rotate-key), `app.New` трёх сервисов принимает logCtl, `Start` запускает шиппер (Sender — внутри `if a.redis != nil`), `Stop` ждёт `shipperDone` (`safego.Await`). tint/colorable стали прямыми зависимостями. Тесты [logger_test.go](../internal/platform/bootstrap/logger_test.go) — первые тесты пакета bootstrap (маппинг 2..5, tint vs JSON-file vs fallback, fan-out по порогам, runtime-смена LevelVar глушит base+ring но не Sentry-стаб, StartRedisShipper nil-safe/once). **Phase 51.3**: runtime-уровень через app_settings — domain `LoggingSettings{Level *int}` (секция `logging`, JSONB без миграции) + `ValidateLogLevel` 2..5 + `ErrLogLevelInvalid`; `reloader.SectionLogging` + разворот SectionAll извлечён в `sectionsFor` (забытая секция = publish("all") её не тронет); usecase merge/changedSections/publish `"logging"` (имя = константа Section, каст без маппинга); handler `ErrLogLevelInvalid`→400; bootstrap `appSettingsOverlay.Logging` + чистые `decodeAppSettings` (извлечена из readAppSettings ради тестов без pgxpool) и `applyLogLevelFromOverlay` (валидный→Set, nil/невалидный→YAML-fallback) + `LogLevelReloader`; подписка `SectionLogging` + сид стартового уровня (первый вызов reloader-fn — Init строит логгер ДО чтения app_settings) во всех трёх app.go (Sender — при наличии Redis). Тесты: domain `TestValidateLogLevel`; usecase merge/changedSections/publish/invalid; reloader [sections_test.go](../internal/platform/reloader/sections_test.go) (`sectionsFor(All)` содержит все секции); bootstrap [app_settings_test.go](../internal/platform/bootstrap/app_settings_test.go) (decode-таблица, applyLogLevelFromOverlay: уровень/fallback/идемпотентность/nil-safe); **новый [app_settings_handler_test.go](../internal/web/adapter/in/http/app_settings_handler_test.go)** — закрыта дыра (HTTP-тестов на handler не было): PUT 204/400-таблица/403, GET маскирует секреты и отдаёт logging.level. **Phase 51.4**: API вьювера (admin-only) — domain [service_log.go](../internal/domain/service_log.go) (`ServiceLogEntry`, `ServiceLogLevelRank`, `ValidServiceLogService`; `ErrServiceLogInvalidService/Level`); порт `ServiceLogReader.Tail` ([port/service_log_reader.go](../internal/web/usecase/port/service_log_reader.go)); адаптер [redis/service_logs.go](../internal/web/adapter/out/redis/service_logs.go) (LRANGE + `parseEntries`: битая строка — skip+debug, не ошибка); usecase [service_logs.go](../internal/web/usecase/service_logs.go) (`Tail`: services пусто/"all"→все три, дедуп, limit default 500/cap 2000, merge по TS desc, фильтр min_level — неизвестный ранг не прячем, ошибка одного ридера → warn+partial); handler [service_logs_handler.go](../internal/web/adapter/in/http/service_logs_handler.go) (`GET /api/logs` — bare-массив, пусто=`[]`; `GET /api/logs/download` — text/plain attachment `nexus-logs-<svc>-<ts>.log` через `safeFilePart`, nosniff, хронологический порядок, единый формат `FormatServiceLogLine`); роуты в authedAdmin (нет конфликта с POST /logs/:id/replay — у gin отдельные деревья по методам), wiring [web/app.go](../internal/web/app.go), swagger перегенерирован. Тесты: usecase (merge desc/limit после мержа/min_level/фильтр сервиса/all+дедуп/clamp/partial/empty), adapter `parseEntries` (маппинг/skip битых/фолбэк service), handler (200/фильтры/`[]` не null/400-таблица/download-заголовки+safeFilePart+хронология+parity формата). **Phase 51.5**: UI — пункт сайдбара «Логи» (admin, `ScrollText`, [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)), маршрут `/logs` c admin-guard `LogsRoute` ([App.tsx](../web-ui/src/App.tsx)); консоль follow-tail [pages/Logs.tsx](../web-ui/src/pages/Logs.tsx) (новые снизу, автоскролл при «прилипании» к низу, пауза при ручном скролле вверх + кнопка «К последним», раскрытие строки в атрибуты, кап DOM 2000) + тулбар [components/logs/LogsToolbar.tsx](../web-ui/src/components/logs/LogsToolbar.tsx) (чипсы сервисов — клиентский фильтр; сегмент уровня — РЕАЛЬНЫЙ порог через `PUT /api/settings/app {logging:{level}}` + invalidate `["app-settings"]`; поиск; Live refetchInterval 2.5с; количество 200..2000; ↻; скачать `<a download>`); чистые утилиты [lib/logsUtils.ts](../web-ui/src/lib/logsUtils.ts) (`levelFromInt/ToInt`, `filterEntries`, `stableStringify`, `formatEntryLine`, **стабильный `logsQueryKey` без волатильных частей** — грабли §44); i18n `nav.logs` + `logs.viewer.*` en/ru. Тест-инфра: `src/test/setup.ts` (jest-dom) + `setupFiles` в vitest.config.ts, devDep `@testing-library/jest-dom`; тесты [logsUtils.test.ts](../web-ui/src/lib/logsUtils.test.ts) (маппинг уровней, фильтры, детерминированная сериализация, стабильность queryKey) и [LogsToolbar.test.tsx](../web-ui/src/components/logs/LogsToolbar.test.tsx) (чипсы/сегмент-side-effect/поиск/live/download href); **vitest добавлен в CI job `ui-build`** (`npm run test`). Embed-бандл пересобран и закоммичен (`internal/web/static/`). **Phase 51.6**: integration-сценарии [service_logs_test.go](../tests/integration/service_logs_test.go) (Make-цель `test-int-logs` в фан-ауте `test-integration`): шиппер→реальный Redis (LTRIM cap, EXPIRE, индекс 0 — свежая); мерж трёх ключей (desc/limit/min_level); **кросс-сервисный reload уровня через реальный Redis pub/sub** (3 in-process «сервиса» = `bootstrap.NewLogController` + `Subscriber.Run` + `LogLevelReloader`, web-сторона — реальный `AppSettingsUsecase.Update`+`Publisher`; ожидание фактической подписки через `PubSubNumSub`; закрыт исторический пробел — `Subscriber.Run` не гонялся против реального Redis); маскировка e2e (`hunter2`/вложенный token не покидают процесс); неблокируемость под нагрузкой (50×200 записей при зависшем писателе: ~5мс, дропы растут, кольцо живо). **Сценарий немедленно поймал боевой баг**: `AppSettingsRepoPg.Update` маршалил анонимный struct БЕЗ секции `logging` → уровень «сохранялся», но терялся при записи (та же регрессия, что ловили general/security) — починено + четвёртый roundtrip `TestAppSettingsRepo_LoggingRoundTrip_E2E` + предупреждающий комментарий в [app_settings_repo.go](../internal/web/adapter/out/postgres/app_settings_repo.go). `bootstrap.NewLogController(fallback, service)` экспортирован для тестов. **Phase 51.7 — debug-инструментирование (итог анализа §51.9)**: до §51 в проде был ровно ОДИН `logger.Debug`; выбраны слепые зоны (критерий — «нужно при расследовании, шумно на info»): Receiver — [route.go](../internal/receiver/usecase/route.go) (резолв узла: team/path/node/remainder/hop; параметры вызова Sender: target **без query** через новый `redactURLString`, body_len/timeout/retry; итог: status/attempts/upstream_ms/total_ms — читаются `resp.GetAttempts()/GetDurationMs()`, которые раньше игнорировались), [route_async.go](../internal/receiver/usecase/route_async.go) (produce: topic/payload_bytes/duration_ms; offset producer наружу не отдаёт), [handler.go](../internal/receiver/adapter/in/http/handler.go) (`replyDomainError`/`replyAsyncError`: **все 4xx раньше уходили молча** — теперь debug со status/node/op/client_ip), [nodecache/reader.go](../internal/receiver/adapter/out/nodecache/reader.go) (redis miss/decrypt-fail → причина похода в PG; загрузка из PG с duration_ms; **все проглоченные ошибки write-back** — encrypt/marshal/Set); Sender — [send.go](../internal/sender/usecase/send.go) (мёртвый `logger` ожил: breaker open fail-fast, fail-open при ошибке Allow — раньше `_`; каждая попытка attempt/status/reason/duration/backoff; RecordFailure-след), [async.go](../internal/sender/usecase/async.go) (старт доставки, исход status/attempts/duration, **успешный уход в DLQ раньше молчал**), [consumer.go](../internal/sender/adapter/in/kafka/consumer.go) (partition/offset/key/size — доступны только в адаптере), [chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go) (успешный INSERT: rows/duration_ms — раньше след был только на ошибке); Web — [node.go](../internal/web/usecase/node.go) (успешные cacheSet/cacheInvalidate с реальным slug — грабли §50), [app_settings.go](../internal/web/usecase/app_settings.go) (успешный reload-publish по секциям). Редиректы httpclient НЕ понижены до debug — Info-строку `sender follows external redirect` проверяет стендовый сценарий §51.7. **Правило впредь** внесено в CLAUDE.md (§1 + чек-лист §10) и ТЗ §51.9 п.5: новый код закладывает debug в неочевидных/опасных/тихих местах. **Phase 51.9 (гейты сдачи)**: полный `-race` в контейнере golang:1.26 ✅, полный `make test-integration` (8 групп, включая новую `test-int-logs`) ✅, vitest 48 тестов + lint `--max-warnings=0` ✅, браузерный прогон на стенде `services` (Playwright) ✅ — строки всех трёх сервисов в консоли (`route: node resolved`/`send: attempt finished`/`kafka: message fetched`/`async: delivery finished`/`clickhouse batch inserted`), смена Debug→Warn через сегмент глушит debug/info во ВСЕХ трёх процессах без рестарта (свежий трафик не даёт ни строки; `app_settings.logging={"level":3}`), возврат на Debug оживляет поток, Live сам подтягивает (refetch 2.5с), раскрытие строки показывает атрибуты, поиск матчит и msg, и attrs, скачивание отдаёт `nexus-logs-all-<ts>.log` (Content-Disposition/nosniff), в кольцах нет сырых кред, консоль браузера 0 ошибок. **Стенд поймал UX-баг (фикс в этом же блоке)**: фильтр по чипсам сервисов был КЛИЕНТСКИМ → «болтливый» Sender (DLQ-репроцессор) выбирал весь `limit` при мерже, и чипс «receiver» показывал «Записей пока нет» при живых логах в Redis. Фильтр переведён на серверный (`serviceParam` → `?service=`, ключ кеша `logsQueryKey(services, limit)` не зависит от порядка кликов), `filterEntries` оставлен только для поиска; +5 unit-тестов |
| **§50: кеш узла по реальной команде + видимость редиректов Sender** | ✅ §50 | ТЗ [50-node-cache-teamslug-redirects.md](sections/50-node-cache-teamslug-redirects.md), ветка `fix/node-cache-team-slug`. **Баг:** Web писал/инвалидировал ключ кеша `node:default:<path>`, Receiver читает `node:<team_slug>:<path>` — для узла вне команды default правки (target_url/статус/**удаление**/перенос) ждали Redis-TTL (до 5 мин); боевой симптом — http-target ещё 5 мин ходил на старый https и падал на TLS. **Фикс:** `port.NodeCache(+teamSlug)` ([node_repo.go](../internal/web/usecase/port/node_repo.go)); адаптер `nodeKey(teamSlug,path)` + **шифрование кредов** AES-256-GCM (Receiver делает Decrypt; раньше plaintext → cache-miss), конструктор берёт `*crypto.Cipher` ([node_cache.go](../internal/web/adapter/out/redis/node_cache.go)); usecase `resolveCacheTeamSlug`/`cacheSet`/`cacheInvalidate` (team_id→slug через `teams.GetByID`, пустой slug=не трогать кеш) в Create/Update/SetStatus/Delete/Move ([node.go](../internal/web/usecase/node.go)) + `refreshCache` ([host_allowlist.go](../internal/web/usecase/host_allowlist.go)); wiring [app.go](../internal/web/app.go). **Видимость редиректов:** `CheckRedirect` в [httpclient/client.go](../internal/sender/adapter/out/httpclient/client.go) — копия http.Client на вызов (Transport общий), лог хопа (Info; Warn при POST→GET с потерей тела), накопление в `HTTPResponse.Redirects`, `redactURL` (query отрезан), лимит 10; `HTTPRequest.NodePath` для `node=` в логе ([port/log_writer.go](../internal/sender/usecase/port/log_writer.go)); `appendRedirectNote` дописывает сводку в `rec.Reason` → видно в UI «Причина», одна запись лога на запрос ([send.go](../internal/sender/usecase/send.go)). Тесты: `TestNodeCache_TeamSlugKey_WebToReceiver_E2E` (Web пишет ключ, который читает Receiver; update/delete доходят немедленно), `TestNodeCache_E2E` (неймспейс+шифрование), httpclient (Info/Warn/цикл/redactURL), `TestAppendRedirectNote`. **§50.4: breaker не открывается по 4xx** (боевой инцидент site/push: 5×422 `NotRegistered` от протухших FCM-токенов открывали breaker → 30с все пуши узла, вкл. валидные, отбивались `circuit_breaker_open` и терялись; волны переоткрытий с пробного запроса) — `upstreamHealthy = lastErr==nil && status<500` в [send.go](../internal/sender/usecase/send.go) (4xx = узел жив, RecordSuccess; лог по-прежнему done=false); тесты `TestSend_4xx_NoRetry_BreakerStaysHealthy`, `TestSend_BreakerHealth_5xxFails_4xxDoesNot` |
| **§52: трёхсостоянье статуса узла OK / Degraded / Down** | ✅ §52 | ТЗ [52-node-degraded-status.md](sections/52-node-degraded-status.md), ветка `feature/node-degraded-status`. Продолжение §50.4: серия 422 красила живой узел в «Down» (флаг был булев «любой не-2xx»). **Phase 52.2**: [domain/node_outcome.go](../internal/domain/node_outcome.go) — `NodeOutcome` (ok/degraded/down), `OutcomeFromStatusCode` (2xx→ok; <=0 или >=500→down, вкл. 503 breaker-open/502 oversize; иначе degraded — граница = upstreamHealthy §50.4), `IsError()` (прежняя семантика), `GaugeValue`/`OutcomeFromGaugeValue` (0/1/2). **Phase 52.3 (писатель)**: gauge `SetNodeLastRequestOutcome` 0=ok/1=degraded/2=down ([platform/metrics](../internal/platform/metrics/metrics.go)); Redis-кодек `EncodeOutcome`/`DecodeOutcome` — «свап» "1"=down/"2"=degraded ради legacy/rolling (см. 4.29; [platform/nodestatus](../internal/platform/nodestatus/redis.go)); порт `NodeStatusWriter.SetLastOutcome`; обе точки записи ([sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) + [async.go](../internal/sender/usecase/async.go)); `incomplete_total`/ack/DLQ не менялись. **Первый тест gRPC-адаптера** [sender_service_test.go](../internal/sender/adapter/in/grpc/sender_service_test.go) (реальный SendUsecase, таблица 200/302/422/500/транспорт). **Phase 52.4 (читатель)**: порт `NodeStatusReader.GetLastOutcomes`; reader MGET+декод ([node_status.go](../internal/web/adapter/out/redis/node_status.go)); `NodeThroughputRow.LastOutcome` + `applyLastOutcomes` (Redis приоритет → Prom `OutcomeFromGaugeValue` → ok; [metrics.go](../internal/web/usecase/metrics.go)); API `last_outcome` аддитивно + `last_error` back-compat (= IsError; [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go)); попутно устранён swagger-дрейф `NodesMetricsResponse` (не было `totals`). **Phase 52.5 (UI)**: Variant `degraded` (отдельный от `warn`=Queue), Pill tone warn «Degraded» (латиницей в обеих локалях), сортировка err→degraded→queue, опция фильтра, `Throughput.lastOutcome` ([Overview.tsx](../web-ui/src/pages/Overview.tsx)); бандл пересобран. **Phase 52.6 (integration)**: round-trip всех исходов + legacy "1"→down + мусор→fallback ([nodestatus_test.go](../tests/integration/nodestatus_test.go)); сценарий инцидента 422×3→degraded, 500→down, 200→ok через реальные AsyncProcessor+Redis ([nodestatus_scenario_test.go](../tests/integration/nodestatus_scenario_test.go)). Полный gRPC-E2E сознательно не делался (тонкий маппинг закрыт юнитом). Неочевидности — §4.29 |
| **§53: копирование узла — кнопка «Скопировать узел»** | ✅ §53 | ТЗ [53-copy-node.md](sections/53-copy-node.md), ветка `feature/copy-node`. **Phase 1.1 (backend)**: `POST /api/nodes/:id/copy` (manager+, [routes.go](../internal/web/adapter/in/http/routes.go)) → `NodeHandler.Copy` + `CopyNodeRequest` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go), ошибки через общий `replyDomainError`: 404/409/400, новых i18n-ключей не потребовалось); usecase [node_copy.go](../internal/web/usecase/node_copy.go) — `Copy` (team-scope как Get, клон `cloneNodeForCopy`: сброс ID/таймстемпов, **всегда paused**, `slices.Clone` для ForwardHeaders; креды копируются plaintext-в-памяти → re-encrypt в pg-адаптере), клонирование ссылок allowlist-хостов `copyHostLinks` в той же UoW-транзакции + пересборка снимка, аудит `domain.ActionNodeCopy` ([audit.go](../internal/domain/audit.go)) с `source_node_id`/`source_path`/`allowed_hosts_cloned`; из `Create` извлечён общий пайплайн `prepareNewNode` ([node.go](../internal/web/usecase/node.go)) — поведение Create не изменено; CH-таблица копируется как есть (§37). Unit [node_copy_test.go](../internal/web/usecase/node_copy_test.go) (happy/scope/конфликт path — `memNodeRepo` научен UNIQUE(team_id,path)/невалидный path/hard-limit/host-links через `fakeUow`/идемпотентный провижининг). **Phase 1.2**: integration `TestNodeUC_Copy_E2E` ([node_copy_test.go](../tests/integration/node_copy_test.go), группа test-int-pg) — decrypt→re-encrypt round-trip кредов, paused, клон ссылок+снимка, аудит, 409, team-scope. **Phase 2.1 (UI)**: кнопка «Копировать» в шапке [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) (manager+) + `CopyNodeDialog` (префилл `<path>-copy`, `validateNodePath` из [nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts), POST → invalidate `["nodes"]` → navigate на копию); i18n `node.actions.copy`+`node.copy.*` en/ru; `node.copy` в фильтре [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx); бандл пересобран → `internal/web/static/` |
| **§54: сохранение фильтров рабочего стола (Overview)** | ✅ §54 | ТЗ [54-overview-filter-persistence.md](sections/54-overview-filter-persistence.md), ветка `feature/overview-filter-persistence`. **Баг-репорт**: фильтр на рабочем столе сбрасывался при открытии узла и возврате — «Назад» браузера ИЛИ кнопка «Узлы». Причина: `search`/`method`/`statusFilter`/живой `period` в чистом `useState`, а Overview — дочерний `Outlet` (при переходе на `/nodes/:id` размонтируется). Кнопка «Узлы» — `NavLink to="/"` ([Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)) **без query**, поэтому одного URL мало → гибрид. **Phase 2.1**: чистый модуль [lib/overviewFilters.ts](../web-ui/src/lib/overviewFilters.ts) по образцу [lib/period.ts](../web-ui/src/lib/period.ts) — `parseFilters` (толерантен: junk → дефолт по-полю независимо, `range` приоритетнее `from`/`to`), `serializeFilters` (**только отличия от дефолта** → чистый URL при дефолтных фильтрах), `applyFilters` (не трогает чужие query-ключи), `saveFilters`/`loadFilters` (зеркало; всё-дефолт → `removeItem`), `FILTERS_STORAGE` — одна константа выбора хранилища. Канонический формат в URL и зеркале один — сериализованная query-строка, поэтому restore = `parse → serialize → setParams` и заодно санитизирует мусор из хранилища. Тесты [overviewFilters.test.ts](../web-ui/src/lib/overviewFilters.test.ts) (42 кейса). **Phase 3.1**: [Overview.tsx](../web-ui/src/pages/Overview.tsx) — 4 `useState` → состояние из `useSearchParams` (конвенция [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx), фикс П12) + зеркало `sessionStorage`; `updateFilters` (единая точка записи), restore/mirror-эффект, debounce поиска 300мс. Звёздочка §44.B, `view`, `autoRefresh` не тронуты. **Phase 3.2** (найдено стендом): [PeriodPicker.tsx](../web-ui/src/components/ui/PeriodPicker.tsx) держал `from`/`to` в своём `useState("")` и не инициализировал их из `value` → восстановленный произвольный период был применён, но поля календаря пустые. Дефект предсуществующий (раньше custom-период не переживал уход со страницы) — §54 сделал кейс регулярным. Фикс: `toLocalInput` (RFC3339 → datetime-local в локальной зоне) + засев `useState` + эффект синхронизации (период приходит ПОСЛЕ монтирования, без ремоунта — клик «Узлы» на активной странице); тесты [PeriodPicker.test.tsx](../web-ui/src/components/ui/PeriodPicker.test.tsx) (4 кейса, 2 падают на старом коде — проверено откатом). Затрагивает 5 страниц (Overview/KafkaMonitor/вкладки узла) — везде показанный период теперь соответствует `value`. Бандл пересобран → `internal/web/static/`. **Гейты сдачи**: vitest 104 ✅, lint `--max-warnings=0` ✅, `golangci-lint` 0 issues ✅, полный `-race` в контейнере golang:1.26 ✅, `make test-integration` ✅, браузерный прогон на стенде `services` (Playwright) ✅ — оба кейса баг-репорта («Назад» и «Узлы»), клик «Узлы» на активной странице, F5, дип-линк с мусором (`status=banana&range=99h` → дефолты по-полю, **зеркало санитизировано**), ручная очистка (зеркало `removeItem`, фильтр не воскресает), чистая сессия → период от звёздочки 14д, custom-период в полях `01.07.2026 03:00`. Неочевидности — §4.31 |
| UI формы: Toggle, карточки «Заголовки» / «Логирование» | ✅ Phase 22.3 | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (две карточки, мастер-тумблер гасит `<fieldset disabled>`), компонент [Toggle](../web-ui/src/components/ui/pickers.tsx), i18n ru/en |
| Telegram-алерты через Prometheus + метрика `nexus_request_incomplete_total` | ✅ Phase 22.4 | [notification.go](../internal/web/usecase/notification.go) (`PromMetrics.NodeErrors` вместо `LogReader.CountErrors`), [metrics.go](../internal/platform/metrics/metrics.go), инкремент в [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go)/[async.go](../internal/sender/usecase/async.go), wiring [app.go](../internal/web/app.go) (требует Prometheus) |
| Карточки Overview под `ui_cards.html` (спарклайн, p95, фильтр) | ✅ Phase 22.5 | [Overview.tsx](../web-ui/src/pages/Overview.tsx) (полоса-акцент, chip+pill, 3 метрики, спарклайн, target, фильтр статусов, сортировка); backend [prometheus/client.go](../internal/web/adapter/out/prometheus/client.go) (`NodeSeries` range-запрос + p95 в `NodeThroughput`), [metrics.go](../internal/web/usecase/metrics.go), DTO [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go) |
| Подсказки-расшифровки на графиках/метриках (info-иконка `?` + богатый тултип на области) | ✅ Phase 22.6 | атом [LabelHint.tsx](../web-ui/src/components/ui/LabelHint.tsx) (HelpCircle + Radix `Tooltip`); `hint?` в [Kpi](../web-ui/src/components/ui/data.tsx); per-bar тултип в [TrafficChart.tsx](../web-ui/src/components/ui/TrafficChart.tsx) (время/запросы/ошибки %, легенда цветов, `delayDuration` в [Tooltip.tsx](../web-ui/src/components/ui/Tooltip.tsx)); тултип на `Sparkline` ([Overview.tsx](../web-ui/src/pages/Overview.tsx)); заголовки в [OverviewTab.tsx](../web-ui/src/components/node/OverviewTab.tsx)/[MetricsTab.tsx](../web-ui/src/components/node/MetricsTab.tsx); ключи `metrics.hints.*` (ru/en). Грабли: `@radix-ui/react-tooltip` v1.2.8 **не** экспортирует `Anchor` — per-bar тултип через обёртку `Tooltip` на каждый столбец (Radix ленив на контенте) |

---

### §23 Каталог разрешённых хостов (Allowed Hosts catalog)

ТЗ — [sections/23-allowed-hosts-catalog.md](sections/23-allowed-hosts-catalog.md). Ветка
`feature/catalogs-and-swagger`.

| Пункт | Статус | Где |
|---|---|---|
| Миграция `host_allowlist` + `node_allowed_hosts` (M2M) + trigger usage_count | ✅ Phase 23.A.1 | [0011](../migrations/0011_host_allowlist.up.sql) |
| Домен `HostAllowlistEntry` (exact/wildcard/regex), `EncodedPattern` (`re:`) | ✅ Phase 23.A.1 | [domain/host_allowlist.go](../internal/domain/host_allowlist.go) |
| Единый матчер `domain.HostAllowed` (+regex) для Receiver и preview | ✅ Phase 23.A.2 | [host_allowlist.go](../internal/domain/host_allowlist.go), [urlresolver.go](../internal/receiver/usecase/urlresolver.go) |
| Port + PG repo + `NodeRepo.UpdateAllowedHostsSnapshot` + `Repos.Hosts` | ✅ Phase 23.A.3 | [port/host_allowlist_repo.go](../internal/web/usecase/port/host_allowlist_repo.go), [postgres/host_allowlist_repo.go](../internal/web/adapter/out/postgres/host_allowlist_repo.go) |
| Usecase: CRUD/preview/link-unlink + пересборка снимка + cache.Set | ✅ Phase 23.A.4 | [usecase/host_allowlist.go](../internal/web/usecase/host_allowlist.go) (+тесты) |
| HTTP: `/api/allowed-hosts*`, `/api/nodes/:id/allowed-hosts*`, swagger | ✅ Phase 23.A.5 | [http/host_allowlist_handler.go](../internal/web/adapter/in/http/host_allowlist_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go), [app.go](../internal/web/app.go) |
| UI: страница Settings → Allowed Hosts (таблица/фильтр/диалог preview) | ✅ Phase 23.A.6 | [pages/settings/AllowedHosts.tsx](../web-ui/src/pages/settings/AllowedHosts.tsx) |
| UI: combobox+chips в форме узла (attach/detach, SSRF-warn) | ✅ Phase 23.A.7 | [components/node/AllowedHostsField.tsx](../web-ui/src/components/node/AllowedHostsField.tsx), [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) |

### §24 Справочник заголовков (Headers catalog)

ТЗ — [sections/24-headers-catalog.md](sections/24-headers-catalog.md).

| Пункт | Статус | Где |
|---|---|---|
| Миграция `headers_catalog` (UNIQUE lower(name)) + домен | ✅ Phase 24.B.1 | [0012](../migrations/0012_headers_catalog.up.sql), [domain/header_catalog.go](../internal/domain/header_catalog.go) |
| Port+repo (usage on-read) + usecase (идемпотентный create) + HTTP | ✅ Phase 24.B.2 | [postgres/header_catalog_repo.go](../internal/web/adapter/out/postgres/header_catalog_repo.go), [usecase/header_catalog.go](../internal/web/usecase/header_catalog.go), [http/header_catalog_handler.go](../internal/web/adapter/in/http/header_catalog_handler.go) |
| UI: combobox (debounce, top-used, автосоздание) в форме узла | ✅ Phase 24.B.3 | [components/node/HeadersField.tsx](../web-ui/src/components/node/HeadersField.tsx) |
| CRUD-управление: Get/Update/Delete (port/repo/usecase/HTTP), guard usage>0 → `ErrHeaderInUse`, audit `header.update`/`header.delete`, `PATCH`/`DELETE /api/headers/:id` (admin) | ✅ Phase 24.6 | [postgres/header_catalog_repo.go](../internal/web/adapter/out/postgres/header_catalog_repo.go), [usecase/header_catalog.go](../internal/web/usecase/header_catalog.go), [http/header_catalog_handler.go](../internal/web/adapter/in/http/header_catalog_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go) |
| UI: страница управления Настройки → «Заголовки» (admin) — таблица, add/rename/delete | ✅ Phase 24.7 | [pages/settings/Headers.tsx](../web-ui/src/pages/settings/Headers.tsx), [Settings.tsx](../web-ui/src/pages/Settings.tsx) |

### §25 Swagger в шапке (Topbar) + два дока

ТЗ — [sections/25-topbar-swagger.md](sections/25-topbar-swagger.md).

| Пункт | Статус | Где |
|---|---|---|
| Receiver swagger-аннотации + генерация двух доков (`--exclude`) | ✅ Phase 25.C.1 | [cmd/receiver/main.go](../cmd/receiver/main.go), [receiver/.../handler.go](../internal/receiver/adapter/in/http/handler.go), [Makefile](../Makefile), `docs/receiver` |
| Web раздаёт `/swagger/web` + `/swagger/receiver` (InstanceName) | ✅ Phase 25.C.2 | [app.go](../internal/web/app.go) |
| FE-инфра Radix/cmdk + обёртки Popover/Tooltip/Command | ✅ Phase D.1 | [components/ui/](../web-ui/src/components/ui/) |
| Topbar Swagger popover (две доки, ↗, tooltip) | ✅ Phase 25.D.2 | [components/Topbar.tsx](../web-ui/src/components/Topbar.tsx), [AppShell.tsx](../web-ui/src/components/AppShell.tsx) |

### §27 Тип узла RabbitMQAsync

ТЗ — [sections/27-rabbitmq-async.md](sections/27-rabbitmq-async.md).

| Пункт | Статус | Где |
|---|---|---|
| Домен: `RootMethodRabbitMQAsync`+`IsPull`, поля `rmq_*`/`pull_*`, `Validate`, `NormalizeForRootMethod`, дефолты | ✅ Phase B | [domain/enums.go](../internal/domain/enums.go), [domain/node.go](../internal/domain/node.go), [domain/errors.go](../internal/domain/errors.go) |
| Миграция 0014: `methods`+`RabbitMQAsync`, колонки `nodes`, `chk_rmq_fields` | ✅ Phase B | [0014_rmq_async_node](../migrations/0014_rmq_async_node.up.sql) |
| Postgres: шифрование `rmq_password`, NULL для не-pull, scan | ✅ Phase B | [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go), [db.go](../internal/web/adapter/out/postgres/db.go) |
| DTO+handler: поля, `rmq_password_set`, «пусто=не менять», сброс несовместимых полей в audit | ✅ Phase B | [dto.go](../internal/web/adapter/in/http/dto.go), [node_handler.go](../internal/web/adapter/in/http/node_handler.go), [usecase/node.go](../internal/web/usecase/node.go) |
| `POST /api/nodes/test-rmq` (manager+, rate-limit, passive declare) | ✅ Phase C | usecase [rmq_tester.go](../internal/web/usecase/rmq_tester.go), adapter [rabbitmq/prober.go](../internal/web/adapter/out/rabbitmq/prober.go), handler [rmq_test_handler.go](../internal/web/adapter/in/http/rmq_test_handler.go), route в группе `authedManager` ([routes.go](../internal/web/adapter/in/http/routes.go)); 3 шага connect/auth/queue (passive declare), всегда 200, rate-limit `web.rmq_test_rate_limit_per_min` (деф. 10) |
| Puller-воркер RabbitMQ→Kafka в Receiver, метрики `nexus_rmq_*`, runtime-`degraded` | ✅ Phase D | usecase [puller.go](../internal/receiver/usecase/puller.go)+[puller_manager.go](../internal/receiver/usecase/puller_manager.go), адаптеры [rabbitmq/](../internal/receiver/adapter/out/rabbitmq/) (connector/nodelister/healthsink), envelope-блок `rmq` ([envelope.go](../internal/receiver/usecase/envelope.go)), метрики ([metrics.go](../internal/platform/metrics/metrics.go)), health-снимок в Redis (`rmq:health`), wiring [receiver/app.go](../internal/receiver/app.go), конфиг `receiver.puller`. Reconcile из PG (без узлового pub/sub), graceful stop (≤10с) |
| UI: форма (3 карточки, проверка, pull-параметры), KPI/degraded | ✅ Phase E | health-ридер [redis/rmq_health.go](../internal/web/adapter/out/redis/rmq_health.go) → `NodeResponse.rmq_status` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go)); UI [RabbitMQSection.tsx](../web-ui/src/components/node/RabbitMQSection.tsx), [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (3-я карточка, скрытие incoming-auth/from_request, проверка подключения), [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) (degraded-pill + KPI, refetch 5с); i18n en/ru |
| Сценарные/e2e-тесты (testcontainers RabbitMQ) + loadtest `--ratio-rmq` | ✅ Phase F | [tests/integration/receiver_rmq_test.go](../tests/integration/receiver_rmq_test.go) (RabbitMQ-контейнер: no-loss + kafka-down requeue), [node_repo_test.go](../tests/integration/node_repo_test.go) (round-trip+CHECK), unit [puller_test.go](../internal/receiver/usecase/puller_test.go), loadtest [cmd/loadtest/rmq.go](../cmd/loadtest/rmq.go) (`--ratio-rmq`/`--rmq-url`), TESTING.md |

### §28 Онлайн-метрики, период просмотра, публичный адрес, UX (8 пунктов ТЗ)

ТЗ — [sections/28-online-metrics.md](sections/28-online-metrics.md). Ветка `feature/online-metrics`.

| Пункт | Статус | Где |
|---|---|---|
| #1 Публичный адрес приложения в настройках | ✅ Phase B | `AppSettings.General.PublicBaseURL` + `ValidatePublicBaseURL` ([domain/app_settings.go](../internal/domain/app_settings.go)), merge/changedSections ([usecase/app_settings.go](../internal/web/usecase/app_settings.go)), `GET /api/settings/public` (любой authed) ([app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go)), UI [settings/General.tsx](../web-ui/src/pages/settings/General.tsx), хелпер [lib/nodeUrl.ts](../web-ui/src/lib/nodeUrl.ts) (применён в [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)/[ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx)) |
| #2 Онлайн-обновление метрик везде | ✅ Phase F | единый `METRICS_REFETCH_MS=12s` ([useNodeMetrics.ts](../web-ui/src/components/node/useNodeMetrics.ts)), применён в [Overview.tsx](../web-ui/src/pages/Overview.tsx), [OverviewTab.tsx](../web-ui/src/components/node/OverviewTab.tsx), [MetricsTab.tsx](../web-ui/src/components/node/MetricsTab.tsx) |
| #3 Маскирование данных авторизации (UI) + viewer-гейтинг | ✅ Phase E | [ui/SecretInput.tsx](../web-ui/src/components/ui/SecretInput.tsx) (password+глазик), [useCurrentRole.ts](../web-ui/src/lib/useCurrentRole.ts) (`useRoleAtLeast`), скрытие New/Edit для viewer ([Overview.tsx](../web-ui/src/pages/Overview.tsx), [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx)), redirect в [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx); бэкенд значения не отдаёт (DTO `*_set`) |
| #4 Период просмотра метрик (1h..30d + календарь) | ✅ Phase F | [ui/PeriodPicker.tsx](../web-ui/src/components/ui/PeriodPicker.tsx) + [lib/period.ts](../web-ui/src/lib/period.ts); backend `rangeBuckets`+`resolveWindow` (from/to RFC3339/UnixMilli) [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go); `PromMetrics.NodeThroughput/NodeSeries` → `(since,until)` [metrics_provider.go](../internal/web/usecase/port/metrics_provider.go), [prometheus/client.go](../internal/web/adapter/out/prometheus/client.go), [usecase/metrics.go](../internal/web/usecase/metrics.go) |
| #5 Понятные ошибки валидации (code/field + i18n) + проверка логирования | ✅ Phase C | карта [node_validation.go](../internal/web/adapter/in/http/node_validation.go), `replyDomainError`→`{error,code,field}` [node_handler.go](../internal/web/adapter/in/http/node_handler.go), i18n `node.validation.*` ([i18n.go](../internal/platform/i18n/i18n.go), [locales](../web-ui/src/locales/)), inline-вывод в [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)/[RabbitMQSection.tsx](../web-ui/src/components/node/RabbitMQSection.tsx); `ErrNodeLogsNotConfigured` в `Validate` [domain/node.go](../internal/domain/node.go) |
| #6 Фильтр RabbitMQAsync на Overview | ✅ Phase D | [Overview.tsx](../web-ui/src/pages/Overview.tsx) (3-й тип в селекторе) |
| #7 Багфикс: CH-таблица узла без шаблона (CH code 60) | ✅ Phase A.1 | `provisionTable` берёт дефолтный шаблон при `logging_enabled` без `template_id` ([usecase/node.go](../internal/web/usecase/node.go)); unit [node_provision_test.go](../internal/web/usecase/node_provision_test.go), E2E [node_ch_template_test.go](../tests/integration/node_ch_template_test.go) |
| #8 Багфикс: резолв async-узла по legacy-пути со слешем | ✅ Phase A.2 | `resolveNode` fallback на default-team ([receiver/usecase/resolver.go](../internal/receiver/usecase/resolver.go)), применён в [route.go](../internal/receiver/usecase/route.go)/[route_async.go](../internal/receiver/usecase/route_async.go); unit [resolver_test.go](../internal/receiver/usecase/resolver_test.go) |

---

### §29 Комментарий узла

ТЗ — [sections/29-node-comment.md](sections/29-node-comment.md). Ветка `feature/node-comment`.

| Пункт | Статус | Где |
|---|---|---|
| Колонка `comment` + домен + валидация (≤2000 рун) | ✅ Phase 29.A | миграция [0016](../migrations/0016_node_comment.up.sql), [domain/node.go](../internal/domain/node.go) (`Comment`, `utf8.RuneCountInString`), `ErrNodeCommentLength` [errors.go](../internal/domain/errors.go) |
| PG repo + DTO + Swagger | ✅ Phase 29.A | comment последней колонкой в [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go) (INSERT/UPDATE/scan), [dto.go](../internal/web/adapter/in/http/dto.go) (req/resp+мапперы); тесты [node_test.go](../internal/domain/node_test.go), [node_repo_test.go](../tests/integration/node_repo_test.go) |
| UI: блок в форме + показ на «Обзоре» + i18n | ✅ Phase 29.B | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (Card+Textarea внизу формы), [OverviewTab.tsx](../web-ui/src/components/node/OverviewTab.tsx) (read-only), `node.form.comment*` ([locales](../web-ui/src/locales/)) |
| Багфикс: ключ node-кеша Web = формату Receiver | ✅ Phase Fix.B | [redis/node_cache.go](../internal/web/adapter/out/redis/node_cache.go) (`node:<DefaultTeamSlug>:<path>`) — см. §4.26 |

---

### §30 Логирование, обработка паник и идентификация запросов

ТЗ — [sections/30-logging-panic-recovery.md](sections/30-logging-panic-recovery.md).
Ветка `feature/logging-panic-requestid`. Зависимость: bump логгера до v1.7.9
(`With`/`WithContext`).

| Пункт | Статус | Где |
|---|---|---|
| Recover в горутинах (helper) | ✅ Phase 1–2 | [safego](../internal/platform/safego/safego.go) (`Recover`/`RecoverCtx`); `defer safego.Recover` в [runner.go](../internal/platform/runner/runner.go), всех горутинах [sender](../internal/sender/app.go)/[receiver](../internal/receiver/app.go)/[web](../internal/web/app.go) app.go, пулах ([kafka consumer](../internal/sender/adapter/in/kafka/consumer.go), [chlog writer](../internal/sender/adapter/out/chlog/writer.go), [puller_manager](../internal/receiver/usecase/puller_manager.go)), [ch manager](../internal/platform/clickhouse/manager.go), [api_token](../internal/web/usecase/api_token.go), [logs](../internal/web/usecase/logs.go) |
| Recover в точках входа | ✅ Phase 2 | production `main` уже под `bootstrap.Shutdown`; [echosrv](../cmd/echosrv/main.go)/[loadtest](../cmd/loadtest/main.go) — `safego.Recover` в main |
| gin recovery → лог + Sentry + 500 | ✅ Phase 3 | [recovery.GinMiddleware](../internal/platform/recovery/middleware.go) заменяет `gin.Recovery()` во всех движках; capture через `logger.WithContext` в request-scoped hub |
| requestId (UUID v4, не перезаписывать) → Sentry | ✅ Phase 3 | [requestid](../internal/platform/requestid/requestid.go) (middleware первым в цепочке, `X-Request-Id`); тег на scope+транзакцию в [sentry/middleware.go](../internal/platform/sentry/middleware.go) |
| Версия в UI | ✅ Phase 4 | публичный `GET /api/version` [version_handler.go](../internal/web/adapter/in/http/version_handler.go) (`cfg.Build.Version`, который теперь = `buildOpt.Version` из git-ldflags, см. ниже «Версия — единый источник git»); футер [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx); порядок присвоения — [DEPLOYMENT.md §9.0](../DEPLOYMENT.md) |

### §31 Мониторинг Kafka

ТЗ — [sections/31-kafka-monitoring.md](sections/31-kafka-monitoring.md). Ветка
`feature/kafka-monitoring`. Admin-only alarm-dashboard `/kafka` (в блоке «Аудит», не в «Настройках»).

| Пункт | Статус | Где |
|---|---|---|
| Конфиг порогов + rate-limit | ✅ Phase A | `WebSection.KafkaAlerts`/`KafkaMonitorRateLimitPerMin` ([config.go](../internal/platform/config/config.go), [defaults.go](../internal/platform/config/defaults.go), [config.example.yml](../config/config.example.yml)) |
| Prometheus Kafka-метрики | ✅ Phase A | `PromMetrics.KafkaOverview/KafkaTimeseries` ([port](../internal/web/usecase/port/metrics_provider.go), [client.go](../internal/web/adapter/out/prometheus/client.go)) — async по `method="requestAsync"` |
| Kafka Admin-адаптер | ✅ Phase B | [kafkaadmin](../internal/web/adapter/out/kafkaadmin/) (Metadata/ListOffsets/ListGroups/OffsetFetch/DescribeGroups, ping через errgroup+safego.Recover) + порт [kafka_admin.go](../internal/web/usecase/port/kafka_admin.go) |
| Redis-кеш метаданных (TTL 30с) | ✅ Phase B | [kafka_cache.go](../internal/web/adapter/out/redis/kafka_cache.go) (`port.KafkaCache`) |
| usecase + health-banner | ✅ Phase C | [kafka_monitor.go](../internal/web/usecase/kafka_monitor.go), [kafka_bynode.go](../internal/web/usecase/kafka_bynode.go), [kafka_health.go](../internal/web/usecase/kafka_health.go) (`evaluateHealth`, reason-коды для i18n на фронте) |
| 5 эндпоинтов + rate-limit + routes | ✅ Phase C | [kafka_handler.go](../internal/web/adapter/in/http/kafka_handler.go) (Swagger, лимит периода 90д, `KafkaRateLimitMiddleware`), [routes.go](../internal/web/adapter/in/http/routes.go) (`authedAdmin`/kafka group), DI [app.go](../internal/web/app.go) |
| SPA страница `/kafka` (recharts) | ✅ Phase D | [KafkaMonitor.tsx](../web-ui/src/pages/KafkaMonitor.tsx) + [components/kafka/](../web-ui/src/components/kafka/); пункт в [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx) (admin-only), маршрут [App.tsx](../web-ui/src/App.tsx) |

**Неочевидности.**
- **Метрики из `method="requestAsync"`, а не `databus_kafka_*`.** Отдельных kafka-метрик в проекте
  нет; async-трафик уже размечен в `nexus_requests_total`/`nexus_request_incomplete_total`/
  `nexus_request_duration_seconds` (Receiver/Sender, `method="requestAsync"`). Поэтому throughput/
  ошибки/латентность взяты из них без новых метрик и без правок Receiver/Sender.
- **Top-узлы из Prometheus, а не ClickHouse.** В CH логи лежат по одной таблице на узел (нет единой
  колонки `node_path` для `GROUP BY`); Prometheus уже агрегирует по метке `node` (`NodeThroughput`).
- **`size_bytes` топика = 0 (best-effort).** Высокоуровневый `segmentio/kafka-go` не экспонирует
  `DescribeLogDirs`; `messages_estimate` считается надёжно из watermarks (ListOffsets). Опция на
  будущее: если в Prometheus есть `kafka_exporter`/JMX — добавить fallback `sum by(topic)(kafka_log_log_size)`.
- **in-flight и produce p95 — реальные метрики (Phase F).** Добавлены `nexus_kafka_in_flight{component}`
  (Sender consumer: `Inc` после `FetchMessage`, `Dec` после `Handle`) и
  `nexus_kafka_produce_duration_seconds{topic}` (producer, через опцию `WithMetrics`). Web берёт
  in-flight из `sum(nexus_kafka_in_flight)`, produce p95 — из гистограммы (ранее были прокси
  lag/обработка).
- **Мягкая деградация.** Нет Prometheus → KPI/графики нули; нет доступа к Kafka (или пустой
  `kafka.brokers`) → admin-клиент не создаётся, блоки топиков/брокеров помечены недоступными.

---

### §32 Защита от зацикливания запросов (loop protection)

ТЗ — [sections/32-loop-protection.md](sections/32-loop-protection.md). Ветка `feature/loop-protection`.
Две меры против петли, когда `target_url` указывает на сам Receiver.

| Пункт | Статус | Где |
|---|---|---|
| Sentinel-ошибки | ✅ Phase A | `ErrLoopDetected`, `ErrNodeTargetURLSelfReference` ([domain/errors.go](../internal/domain/errors.go)) |
| Hop-счётчик `X-Nexus-Hops` (helper) | ✅ Phase B | [receiver/usecase/loop.go](../internal/receiver/usecase/loop.go) (`HeaderHops`, `nextHop`) |
| Проверка+инкремент в sync/async | ✅ Phase B | [route.go](../internal/receiver/usecase/route.go), [route_async.go](../internal/receiver/usecase/route_async.go) (hop в `SendRequest.Headers` / `Envelope.Headers`) |
| Конфиг `receiver.max_hops` | ✅ Phase B | [config.go](../internal/platform/config/config.go), [defaults.go](../internal/platform/config/defaults.go) (дефолт 5; <0 выкл.), [config.yml](../config/config.yml) (`NEXUS_RECEIVER_MAX_HOPS`) |
| 508 в handler + метрика | ✅ Phase B | `classifyDomainError`/`onLoopDetected` ([handler.go](../internal/receiver/adapter/in/http/handler.go)); `nexus_loop_detected_total{mode}` ([metrics.go](../internal/platform/metrics/metrics.go)) |
| Self-ref валидация `target_url` | ✅ Phase C | [node_selfref.go](../internal/web/usecase/node_selfref.go) (`checkSelfReference`/`isSelfReferenceTarget`), вызов в `Create`/`Update` ([node.go](../internal/web/usecase/node.go)); конфиг `web.self_ingress_hosts` → `resolveSelfIngressHosts` ([app.go](../internal/web/app.go)) |
| Маппинг ошибки + i18n | ✅ Phase C | `node.validation.target_url_self` ([node_validation.go](../internal/web/adapter/in/http/node_validation.go), [i18n.go](../internal/platform/i18n/i18n.go) EN/RU, [en.json](../web-ui/src/locales/en.json)/[ru.json](../web-ui/src/locales/ru.json)) |

**Неочевидности.**
- **Hop-заголовок в обход allowlist узла.** Шина форвардит только `forward_headers`; `X-Nexus-Hops`
  служебный и добавляется в исходящую карту заголовков **после** `pickForwardHeaders` — поэтому он
  всегда уходит наружу и читается на следующем витке. Sender про него не знает (просто переносит карту).
- **Логика в usecase, не в handler.** `nextHop` вызывается в `Route`/`RouteAsync` (один helper на оба
  пути) — чтобы и sync, и async ловили петлю одинаково и до отправки наружу.
- **Метрика именуется `nexus_loop_detected_total`** (а не `nexus_receiver_*`): в проекте сервис
  кодируется const-меткой `service="receiver"`, как у всех `nexus_*`. Инкремент — в handler
  (`onLoopDetected`), где известен `mode=sync|async`; `metrics` может быть nil в unit-тестах.
- **`max_hops=0` → дефолт 5, `<0` → выкл.** Паттерн defaults.go (`==0` подставляет дефолт) не даёт
  выразить «0 = выключено», поэтому выключение — отрицательным значением.

---

### §33 Доработка тултипов графиков (chart tooltips)

ТЗ — [sections/33-chart-tooltips.md](sections/33-chart-tooltips.md). Эталон —
[nexus_chart_tooltip.html](nexus_chart_tooltip.html). **Статус: ✅ реализовано** (ветка
`feature/chart-tooltips`, блоки Phase 33.1–33.4; встроенный SPA пересобран и закоммичен).

| Пункт | Статус | Где |
|---|---|---|
| Компонент `<ChartTooltip>` | ✅ Phase 33.1 | [ChartTooltip.tsx](../web-ui/src/components/ui/ChartTooltip.tsx) (пропсы `period/primary/series/footer/action/compact`); хелпер `msToDatetimeLocal` ([format.ts](../web-ui/src/lib/format.ts)) |
| Throughput/Lag Kafka (recharts `content`) | ✅ Phase 33.2 | [charts.tsx](../web-ui/src/components/kafka/charts.tsx) (`ThroughputTip`/`LagTip`-адаптеры payload→props, курсор-кроссхэйр), [KafkaMonitor.tsx](../web-ui/src/pages/KafkaMonitor.tsx) (`stepSeconds`) |
| TrafficChart (Radix) | ✅ Phase 33.3 | [TrafficChart.tsx](../web-ui/src/components/ui/TrafficChart.tsx) — `ChartTooltip` в `content=`, подвал пик/дельта к среднему |
| MiniSpark (добавлен тултип) | ✅ Phase 33.3 | [charts.tsx](../web-ui/src/components/kafka/charts.tsx) (`MiniTip`, при заданном `unit`) |
| Sparkline Overview (вместо `title`) | ✅ Phase 33.3 | `Sparkline` в [Overview.tsx](../web-ui/src/pages/Overview.tsx) (Radix + compact `ChartTooltip`) |
| Action «открыть логи за момент» | ✅ Phase 33.4 | клик по столбцу TrafficChart → `openLogsAt` ([NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx)) → `LogsTab initialFilter` ([LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx)); проброс через [OverviewTab.tsx](../web-ui/src/components/node/OverviewTab.tsx)/[MetricsTab.tsx](../web-ui/src/components/node/MetricsTab.tsx) |
| i18n `metrics.tooltip.*`/`kafka.tooltip.*` | ✅ Phase 33.4 | [en.json](../web-ui/src/locales/en.json)/[ru.json](../web-ui/src/locales/ru.json) (паритет ключей) |

**Неочевидности / решения.**
- **Без `@floating-ui/react`** (макет рекомендовал). Позиционирование/snap — штатные у recharts
  (`content`-проп получает активную точку) и Radix (`side`/collision). Новый пакет не вводим.
- **Action — клик по элементу графика, не кнопка в тултипе.** Hover-тултип ненадёжно ловит клик по
  своей кнопке (исчезает при движении курсора к ней). Поэтому в `ChartTooltip` `action` — лишь
  визуальная подсказка-affordance, а реальный `onClick` висит на самом столбце TrafficChart.
- **Только фронт.** Partition-breakdown lag и сравнение «неделю назад» из макета **не реализованы** —
  Prometheus отдаёт lag агрегатом (`sum(nexus_kafka_lag)`), per-partition ряда и week-ago ряда в
  timeseries нет. Вынесено в out-of-scope §33.7; в `LagTip` оставлена заметка-задел.
- **`ChartTooltip` — чистый презентационный.** Данные наполняют мапперы каждого графика; компонент не
  знает про recharts/Radix. Один компонент переиспользуется во всех 5 местах (compact-режим для спарков).

---

### §34 Операбельность: навигация, сессия, версия, async-очередь Kafka, фикс replay

ТЗ — [sections/34-ops-session-version-async-queue.md](sections/34-ops-session-version-async-queue.md).
**Статус: ✅ реализовано** (ветка `feature/ops-async-queue`, блоки Phase 34.0–34.E.6;
встроенный SPA пересобран и закоммичен).

| Пункт | Статус | Где |
|---|---|---|
| §34.5 Фикс replay 405 (слать `IncomingMethod`, не залогированный `OutgoingMethod`) | ✅ Phase 34.A | [usecase/replay.go](../internal/web/usecase/replay.go) (метод = `node.IncomingMethod`, пустой → POST), тесты `TestReplay_UsesIncomingMethod`/`_IncomingMethodEmptyDefaultsPost` в [replay_test.go](../internal/web/usecase/replay_test.go) |
| §34.1 «Настройки» вниз сайдбара | ✅ Phase 34.B | [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx) — Settings вынесен из основной навигации в подвал (`mt-auto`, общий хелпер `renderNavLink`); бандл пересобран ([internal/web/static/](../internal/web/static/)) |
| §34.3 Обогащённый `/api/version` + dev-override версии | ✅ Phase 34.C | [version_handler.go](../internal/web/adapter/in/http/version_handler.go) (`{version,commit,build_date,override_allowed}`, провайдер override), [app_settings.go](../internal/web/usecase/app_settings.go) (гейт `allowVersionOverride` + merge/changedSections general), [config.go](../internal/platform/config/config.go) (`web.allow_version_override`, `build.commit/build_date`), [bootstrap.go](../internal/platform/bootstrap/bootstrap.go); UI: [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx) (tooltip), [settings/General.tsx](../web-ui/src/pages/settings/General.tsx) (поле под гейтом); тесты `version_handler_test.go`, `app_settings_test.go` |
| §34.2 Настраиваемая длительность сессии | ✅ Phase 34.D | [session_ttl.go](../internal/web/usecase/session_ttl.go) (`SessionTTLProvider` atomic), [auth.go](../internal/web/usecase/auth.go) + [auth_handler.go](../internal/web/adapter/in/http/auth_handler.go) (TTL через провайдер), [app_settings.go](../internal/web/usecase/app_settings.go) (merge/validate/changedSections security), [domain/app_settings.go](../internal/domain/app_settings.go) (`SecuritySettings`, `ValidateSessionTTLSeconds` 5мин..30сут), [reloader.go](../internal/platform/reloader/reloader.go) (`SectionSecurity`), [app.go](../internal/web/app.go) (сидинг + hot-reload); UI: [settings/General.tsx](../web-ui/src/pages/settings/General.tsx) (поле в минутах); тесты `session_ttl_test.go`, `auth_test.go` (динамический TTL), `app_settings_test.go` |
| §34.4 Управление async-очередью Kafka (tombstones) | ✅ Phase 34.E.1–E.6 | tombstones [platform/queuecancel](../internal/platform/queuecancel/redis.go); проверка в Sender [sender/usecase/async.go](../internal/sender/usecase/async.go); peek [kafkaadmin/async_queue.go](../internal/web/adapter/out/kafkaadmin/async_queue.go) (порт `AsyncQueuePeeker`); usecase [web/usecase/async_queue.go](../internal/web/usecase/async_queue.go); API [http/async_queue_handler.go](../internal/web/adapter/in/http/async_queue_handler.go) (`/api/nodes/:id/async-queue/*`); UI [node/QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx) |
| §34.6 Просмотр DLQ во вкладке «Очередь» | ♻️ переработан §35 | DLQ-peek был дорогим (5000×4 партиции каждые 5с) и дублировал ClickHouse — **удалён**. Неудачные доставки теперь берутся из CH-логов (`done=0`). См. §35 ниже. |

**Неочевидности / решения.**
- **§34.6 — мёртвый адрес → DLQ, не живая очередь (поймано на стенде).** При недоступном внешнем
  адресе на **enabled**-узле сообщения в `nexus.async` НЕ копятся: Sender вычитывает, фейлит доставку
  (после retry) и уводит в `nexus.async.dlq` + коммит. Живая очередь (которую читает вкладка) для
  такого узла пуста (LAG=0), застрявшие — в DLQ. Поэтому добавлен просмотр DLQ. Копится в `nexus.async`
  только при **paused**-узле/лежащем Sender. DLQ чистить нельзя (нет DeleteRecords, нет consumer'а для
  tombstone, общая партиция) — только просмотр + retention (30д) + пауза/отключение узла.
- **§34.5 — корень бага.** В лог ClickHouse пишется **исходящий** метод узла: и sync
  ([route.go:152](../internal/receiver/usecase/route.go#L152)), и async
  ([route_async.go:123](../internal/receiver/usecase/route_async.go#L123)) кладут в запрос к Sender
  `string(node.OutgoingMethod)`, а Sender логирует его как `rec.Method`
  ([send.go:105](../internal/sender/usecase/send.go#L105)). Replay переинъецирует запрос через входной
  endpoint Receiver'а, где метод валидируется против `node.IncomingMethod`. При `OutgoingMethod !=
  IncomingMethod` (POST-in / GET-out без тела) старое `orig.Method` давало 405. Фикс — брать
  `IncomingMethod`. `orig.Method` для сборки метода больше не используется.
- **§34.4 — Kafka append-only → логическое удаление.** Физически удалить одно сообщение или вырезать
  период из Kafka нельзя (`segmentio/kafka-go` не имеет даже `DeleteRecords`). Поэтому «удаление» —
  Redis-tombstones `qcancel:<id>` (TTL = retention топика, самоистечение): Sender в `async.Handle`
  ПЕРЕД отправкой (и ДО paused-блока — чтобы чистить бэклог paused-узла) проверяет cancel-set и
  пропускает отменённые (Ack без отправки/DLQ). Fail-open при недоступном Redis. Сообщения физически
  остаются до retention, но во внешний адрес не уходят.
- **§34.2 — грабли репозитория (поймано на стенде).** `AppSettingsRepoPg.Update` маршалит JSONB
  через анонимную struct с ЯВНЫМ списком секций (чтобы не писать `updated_at/by` в `value`). Новую
  секцию надо добавлять и туда, иначе она молча теряется при сохранении (Get вернёт `{}`). Так
  потерялась `security.session_ttl_seconds` — фикс + регресс-тест `TestAppSettingsRepo_SecurityRoundTrip_E2E`.
  Аналогичная регрессия раньше была с `general`. Unit-fake-репо копирует struct целиком и эту грабли
  НЕ ловит — нужен integration-тест реального репо.
- **§34.4 — нюанс paused-узла.** На enabled-узле отмена строго per-message. На paused-узле consumer
  вычитывает сообщения вперёд без commit'а; когда отменённое доходит до Ack, кумулятивный commit Kafka
  отбрасывает и более ранние неотменённые висящие сообщения. Приемлемо для очистки застрявшей очереди
  (см. §34.4 ТЗ); проверено на стенде.
- **§34.4 — peek без GroupID.** Чтение очереди — транзиентный `kafka.Reader` без GroupID,
  `SetOffset(committed)` от offset группы Sender до high-watermark, фильтр `key==node.Path`. Не входит
  в группу, не коммитит, доставке не мешает. Ограничено cap (5000) и bounded-ctx (10с): глубина/список
  — нижняя оценка при `capped`. Per-node счётчик требует скана (lag в Kafka только на партицию, не на
  ключ). `Envelope` для декода продублирован локально в web-адаптере (как уже сделано в sender) —
  чтобы web не зависел от receiver/sender; JSON-теги обязаны совпадать с каноном
  [receiver/usecase/envelope.go](../internal/receiver/usecase/envelope.go).

### §35 Переработка вкладки «Очередь» (производительность, источник из логов, честная семантика, RBAC)

ТЗ — [sections/35-queue-tab-rework.md](sections/35-queue-tab-rework.md).
**Статус: ✅ реализовано** (ветка `feature/queue-tab-rework`, блоки Phase 35.B1–B5 + F).
Переработка §34.4/§34.6: вкладка на стенде (узел `webhook/sendasynq`, enabled, мёртвый адрес, 248k в
DLQ) тормозила, «Очистить» был no-op, шапка плоха, под non-admin — 403.

| Пункт | Статус | Где |
|---|---|---|
| §35.B2 Счётчик неудач из ClickHouse (`done=0`) | ✅ | `CountFailed` в [port/log_reader.go](../internal/web/usecase/port/log_reader.go) + [clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go); usecase [logs.go](../internal/web/usecase/logs.go); `GET /api/nodes/:id/logs/failed-count` ([logs_handler.go](../internal/web/adapter/in/http/logs_handler.go), scope `logs:read`); integration [log_count_errors_test.go](../tests/integration/log_count_errors_test.go) |
| §35.B3 Лёгкая смена статуса `PATCH /nodes/:id/status` | ✅ | `NodeUsecase.SetStatus` ([node.go](../internal/web/usecase/node.go)) — только Status, без перепровижина; handler `UpdateStatus` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go), `oneof=enabled paused disabled`); роут `authedManager.PATCH` ([routes.go](../internal/web/adapter/in/http/routes.go)); тест `TestNodeUC_SetStatus` |
| §35.B1+B5 Удалить дорогой DLQ-peek, удешевить живую очередь | ✅ | Удалены `PeekDLQDepth/PeekDLQList/decodeDLQMeta/scanRecent/scanPartitionRaw` + порт/типы/usecase `DLQ*`/`/dlq/*` роуты/`dlqTopic`; живая очередь (`PeekList/Body/ScanIDs`) оставлена; `peekCapDefault` 5000→1000; мёртвый `/depth` (`PeekDepth/Depth`) убран по ISP — счётчик «ожидают» берётся из длины `PeekList` |
| §35.B4 RBAC-фикс вкладки | ✅ | Failed-view = `logs:read` (viewer+); живая очередь = admin (секция «Ожидают» скрыта для не-admin через `useRoleAtLeast("admin")`); пауза/отключение = manager+ |
| §35.F Переписать вкладку «Очередь» | ✅ | [QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx): KPI-шапка (ожидают/неудачи) + баннер (пауза/отключить) + 2 секции; failed из CH (`?done=no`), ленивое тело (`/log/:id`), replay (`ReplayDialog`), дип-линк «Открыть в логах» (`LogsInitialFilter.done`); i18n en/ru; бандл пересобран |

**Неочевидности / решения.**
- **§35 — неудачи уже в ClickHouse, дублировать Kafka-peek было ошибкой.** `send.Send` логирует запись
  (`done=false`, с reason/телом/attempts) ДО `publishDLQ` — неудачные доставки уже в CH, быстро
  доступны по индексам `done`/`date_request`. §34.6-peek читал то же самое из Kafka, но дорого. §35
  убирает peek и берёт из CH (`CountFailed` + существующий `GET /logs?done=no`).
- **§35/§36.10 — очистка неудачных доставок (обновлено).** Раньше: «удалить неудачи нельзя» (DLQ без
  DeleteRecords, CH-логи — история). Теперь оператор может принудительно очистить «Неудачные доставки»
  узла (`POST /api/nodes/:id/async-queue/purge-failed`, admin-only): (1) ID `done=0`-сообщений за окно
  отменяются tombstone'ом (как §34.4) → DLQ-репроцессор дропает их (`result=dropped`, перестаёт повторять);
  (2) записи `done=0` удаляются из CH-таблицы узла **lightweight DELETE** → счётчик/список обнуляются сразу.
  Физически DLQ по-прежнему не чистится (tombstone + commit). Реализация: `AsyncQueueUsecase.PurgeFailed`
  → порт `FailedLogsPurger` (`LogReaderCH.FailedIDs`/`DeleteFailed`) + `QueueCancelWriter`. Без CH/таблицы —
  no-op. Тесты: unit `TestAsyncQueue_PurgeFailed_*`, integration `TestAsyncQueue_PurgeFailed_E2E`
  (qcancel в Redis + DELETE в CH). Replay и пауза/отключение остаются как раньше.
- **§36.11 — «Повторить все сейчас».** Форс-повтор всех неудачных узла (`POST
  /api/nodes/:id/async-queue/replay-failed`, admin-only): каждое `done=0`-сообщение за окно
  пере-инжектируется через Receiver (`replayOne`, вынесен из `Replay`) и при успехе его оригинал в DLQ
  отменяется (qcancel) — иначе при восстановлении адреса доставилось бы дважды (replay-копия + авто-повтор;
  выбор пользователя — «Re-send + отменить оригиналы»). `ReplayUsecase` получил опц. `cancel`+`retention`
  через `NewReplayUsecaseWithCancel` (старый ctor делегирует с nil — тесты не трогаются); `FailedIDs`
  добавлен в порт `LogReader` (общий набор сообщений с §36.10). Cap 500/вызов (`capped`), один rate-limit
  на операцию, при отмене ctx — частичный результат с ошибкой. Тесты: `TestReplay_ReplayFailed_*` (отмена
  оригиналов, dispatch-error не отменяет, disabled→409).
- **§37 — node_id в логах (per-node атрибуция в общих CH-таблицах).** Узлы могут делить одну
  `clickhouse_table`; до §37 per-node запросы смешивали их (одинаковые счётчики на рабочем столе/странице
  узла; `DeleteFailed` одного удалял записи другого). Добавлена колонка `node_id String` (UUID) в схему
  лога ([ch_log_schema.go](../internal/domain/ch_log_schema.go); §39 довёл до 22 колонок) + поле `LogRecord.NodeID`.
  **Запись:** `node.ID` прокинут sync через gRPC `SendRequest.node_id` (поле 28, перегенерён proto) →
  `SendInput.NodeID` → `rec.NodeID`; async/DLQ — через `buildSendInput`/`logTTLExpired`. **Миграция
  существующих таблиц:** `platform/clickhouse.EnsureNodeIDColumn` (`ALTER … ADD COLUMN IF NOT EXISTS`,
  идемпотентно) на старте **и Web, и Sender** (порядок деплоя не гарантирован; список таблиц —
  `nodeRepo.ListClickHouseTables` / `nodepg.ListClickHouseTables` — оба **без team-фильтра**; до 1.13.2 Web
  брал `nodeRepo.List(TeamID: defaultTeamID)` и не альтерил БД не-default команд — см. §4.32). **Чтение/удаление:** 8 методов `LogReaderCH` фильтруют
  `(node_id = ? OR node_id = '')` (legacy `''` видны/чистятся у любого co-table узла — компромисс, новый
  трафик чист); `GetByID` — нет (уникальный ID). `nodeID` прокинут в порты (LogReader/NodeLogMetrics/
  FailedLogsPurger) и вызовы (logs/metrics/replay/async_queue). **UI:** node id во вкладке «Конфиг».
  Тест `TestLogReader_NodeIDFilter_E2E` (shared-table: фильтр + DeleteFailed не задевает чужие). Почему
  `node_id` (UUID), а не path — устойчив к переименованию (см. [sections/37-node-id-in-logs.md](sections/37-node-id-in-logs.md)).
- **§39 — path-passthrough + лог-колонки `http_method`/`method`.** Opt-in флаг узла `path_passthrough`
  (миграция `0019`, по умолчанию off → нулевая регрессия). `resolveNode` →
  `(node, remainder, error)`: exact-матч первым, при промахе `prefixMatch` (longest-prefix, первый
  существующий узел-префикс выигрывает; passthrough off → 404, без провала к коротким — нет footgun
  `a`/`a/b`). Хвост клеится `appendPathSuffix` (`url.URL.JoinPath` — кодирование + резолв `..` против
  traversal) в Receiver (sync/async), Sender/proto для МАРШРУТА не трогаются. **Пересечение адресов
  детерминировано:** точный узел `ozon/GetAuthToken` затеняет свой подпуть, остальное под `ozon` идёт
  через passthrough. **Логи:** колонка `http_method` (глагол, после `type`) + `method` репурпозен →
  подпуть (для passthrough; пусто иначе) — `RequiredLogColumns` теперь 22 колонки; подпуть прокинут
  gRPC `SendRequest.request_path` (поле 29) + envelope; кросс-маппинг в `send.go`
  (глагол→`http_method`, подпуть→`method`); миграция существующих таблиц `EnsureHTTPMethodColumn`
  (`AFTER type`) в Web+Sender. **Replay** passthrough-записи — по полному подпути
  (`node.Path + "/" + orig.Method`). Тесты: `TestResolveNode_PathPassthrough`, `TestAppendPathSuffix`,
  `TestReplay_PathPassthrough_ReconstructsSubpath`, integration `TestReceiver_PathPassthrough_Sync_E2E`
  (см. [sections/39-path-passthrough.md](sections/39-path-passthrough.md)).
- **§42.10 — истинные размеры тел в логах (`request_size`/`response_size`) + backfill.** Байты
  ПОЛНОГО тела (`len()` до `truncateRunes`, независимо от `log_request_body`/`log_response_body` —
  инвариант checksum'а), НЕ путать с `request_len`/`response_len` API (руны сохранённой усечённой
  копии, `lengthUTF8`). 0 = нет тела / transport-ошибка / TooLarge §43 (тело не в памяти — размер
  неизвестен). Колонки в КОНЦЕ `RequiredLogColumns` (24 шт.) — ALTER без `AFTER` даёт тот же порядок,
  что рендер новых таблиц. Миграция на старте Web+Sender: `EnsureBodySizeColumns` (идемпотентный
  ALTER, образец §37) + `BackfillBodySizes` — разовые мутации `ALTER…UPDATE size = length(col)` для
  исторических строк (нижняя граница у усечённых записей; guard-`count()` не даёт планировать мутации
  на каждом старте; мутации асинхронные, завершения не ждём). Kafka retry-конверт (§38) — JSON
  `LogRecord`: legacy-записи без полей десериализуются в 0 (тест `TestUnmarshal_LegacyEnvelopeWithoutSizes`).
  DTO отдаёт поля ВСЕГДА (без omitempty — фронт не ветвится на undefined); listCols читает их напрямую
  (дешёвые Int64, в отличие от тел). UI: колонка «Ответ» (`fmtSize` + локализованные единицы
  `logs.size_units` «Б|КБ|…»; НЕ fmtBytes — у того en-единицы) — при добавлении колонок в таблицу
  логов не забыть bump всех `colSpan`; размеры в скобках в заголовках панелей `LogBodies`.
  Integration: `TestClickHouse_BodySizeBackfill` (старая схема → Ensure+Backfill → повторный запуск
  без новых мутаций).
- **Контекстная справка к полям узла (§7.6).** Каждый параметр формы узла — иконка-«вопросик» с
  тултипом «зачем параметр». Переиспользован готовый `LabelHint` (HelpCircle+Tooltip, focus-доступный);
  в обёртку `Field` ([web-ui/src/components/ui/form.tsx](../web-ui/src/components/ui/form.tsx)) добавлен
  проп `help` (иконка рядом с лейблом, вне `<label>`); для тумблеров `LabelHint` ставится вручную.
  Тексты — `node.help.*` (+ `node.rmq.*_help`) в en/ru синхронно. `hint` (приписка «·») сохранён.
- **§40 — HTTP-метод `ANY` («Любой»).** Вх=ANY → `methodMatches` accept-all (нет 405); исх=ANY →
  `effectiveOutgoingMethod(node, in.Method)` зеркалит метод входящего запроса (пустой → POST), резолв в
  Receiver (sync `route.go`, async `route_async.go`) — Sender/proto получают конкретный метод. Pull-узлы:
  исх ANY → POST (входящего метода нет, [puller.go](../internal/receiver/usecase/puller.go)). Replay
  ANY-узла берёт залогированный глагол `orig.HTTPMethod` (§39), а не литерал «ANY»
  ([replay.go](../internal/web/usecase/replay.go)). Хранение строкой `'ANY'`, миграция `0020`
  пересоздаёт CHECK с `'ANY'`; DTO `oneof …ANY`; UI-пункт «Любой» (`node.method.any`). Тесты:
  `TestMethodMatches_Any`, `TestEffectiveOutgoingMethod`, `TestReplay_AnyIncomingUsesLoggedMethod`,
  integration `TestReceiver_AnyMethod_E2E` (PUT/DELETE/GET + зеркало + проброс тела/заголовка ответа).
  По умолчанию ничего не меняется (см. [sections/40-any-http-method.md](sections/40-any-http-method.md)).
- **§41 — универсальная динамическая авторизация + умный Bearer + «Down» по последнему вызову.**
  Источник (`header`/`query`) + имя поля выведены в UI и для **входящей** (`token`/`basic`, новые колонки
  `incoming_auth_dynamic_*`, миграция `0021`), и для **исходящей** (`*_from_request`). Входящая —
  `CheckIncomingAuth(node,h,q,body)` (плагирование query в route/route_async/dry_run); `source=header`
  сохраняет схему Bearer/Basic, `source=query` берёт значение напрямую; constant-time. Исходящая —
  `buildFromRequest`/`extractDynamicValue`/`withScheme`: умный дедуп (значение, уже начинающееся с
  `Bearer `/`Basic `, не удваивается — кейс `?Bearer=Bearer+<jwt>`), пустое поле → `Header=""` (запрос
  без `Authorization`, не 401); `basic_from_request` теперь honored source/field, миграция `0021` пинит
  существующие строки на `header`/`Authorization`. Дефолты `SetDefaults` зависят от режима. Каталог
  «полей запроса» (`request_fields_catalog`, миграция `0022`, `/api/request-fields`, usage по двум
  колонкам) + обязательный single-select picker (UX-гейт; на бэке дефолт). **«Down»** переопределён:
  по исходу ПОСЛЕДНЕГО вызова — gauge `nexus_node_last_request_error{node}` (Sender) → `max by(node)` в
  Web → overlay `applyLastErrors` поверх обеих веток Overview → `nodeVariant` по `last_error` (снимок
  «сейчас», независим от окна). Грабли: (1) `basic_from_request` back-compat data-fix в миграции —
  без него source/field-aware код читал бы query `token` вместо заголовка; (2) валидация домена входящих
  полей толерантна к пустым (узлы без `SetDefaults` в тестах не падают); (3) «Down» в CH-ветке Overview —
  gauge только в Prometheus, потому overlay в общем `NodesOverview`, а не в каждой ветке. Тесты: unit
  (auth/auth_dynamic/route/metrics/client/usecase/domain), integration (`receiver_dynamic_auth_test`,
  `request_field_catalog_repo_test`, node_repo round-trip), Vitest (nodeValidation, RequestFieldField).
  См. [sections/41-universal-request-auth.md](sections/41-universal-request-auth.md).
- **§35 — peek-`MaxWait` = 500мс (грабли стенд-теста, тормоз ~9с).** `kafka.Reader` в `scanPartition`/
  `PeekBody` НЕ задавал `MaxWait` → дефолт kafka-go 10с. После чтения последнего сообщения фоновый
  fetch-цикл reader'а пытается прочитать следующий (ещё пустой) offset и блокируется на `MaxWait`, а
  `defer r.Close()` ждёт завершения этого fetch. Итог: peek даже ОДНОГО сообщения у конца очереди висел
  ~9с (а не из-за длины скана — на стенде LAG=1, читалось 1 сообщение). Лечится коротким
  `MaxWait: peekMaxWait` (500мс): peek 9с→0.4с. Особенно проявляется на paused-узле, где commit
  consumer-группы застревает у high и peek читает у самой границы.
- **§35/§36.10 — purge-кнопки pending доступны всегда (обновлено).** Раньше UI прятал кнопки очистки
  живой очереди на не-paused узле (была ошибочная интерпретация наблюдения как требования). Теперь
  «Очистить за период»/«Очистить все ожидающие» видны всегда (admin): на активном узле очередь обычно
  пуста (доставка сразу) → purge вернёт `cancelled:0` (безвредно), на паузе/при лежащем Sender чистят
  backlog. Кнопки очистки неудачных + «Повторить все сейчас» — **admin-only** (маршруты async-queue под
  `authedAdmin`; гейт UI выровнен с маршрутом — иначе менеджер видел бы кнопку и ловил 403). Общий компонент
  `PurgeButtons` (QueueTab.tsx) переиспользуется для pending и failed.
- **§35 — поллинг живой очереди только когда есть смысл.** `pendingQ`: 4с на паузе (очередь
  наполняется — нужна живая обратная связь), 8с при наличии backlog, на `enabled` с пустой очередью —
  выключен (committed==high → `scanQueue` читает ~0, не молотим Kafka). Сам peek и admin-операции скрыты
  для не-admin (не дёргаем admin-эндпоинты из вкладки, видимой viewer+). После `PATCH status` —
  немедленный `invalidate(["aq-list"])`, чтобы наполнение/дренаж были видны сразу.
- **§35 — баннер статус-зависимый (грабли стенд-теста).** Кнопки управления рисуются по ТЕКУЩЕМУ
  статусу: paused → «Возобновить»+«Отключить», disabled → «Включить», enabled → «Пауза»+«Отключить».
  Первая версия показывала кнопки только при `status==enabled` — поставив на паузу из вкладки,
  пользователь не мог вернуть узел (кнопки исчезали). Секция «Ожидают отправки» видна всегда (admin) с
  поясняющим пустым состоянием (на enabled очередь пуста — норма, неудачи в «Неудачные доставки»);
  purge-кнопки — при `pending>0`. Проверено на стенде (paused→pending=6→purge cancelled:6→resume→0).
- **§35 — `SetStatus` vs полный `Update`.** Кнопки паузы/отключения шлют `PATCH /nodes/:id/status`, а не
  `PUT` (который перезаписал бы все поля и перепровижинил CH-таблицу). `SetStatus` читает узел
  (расшифрованные креды), меняет только `Status`, сохраняет как есть — audit-дифф `{status: before→after}`,
  no-op при том же статусе.

### §36 Авто-репроцессор DLQ (повторная доставка неудачных async-сообщений до TTL)

ТЗ — [sections/36-dlq-reprocessor.md](sections/36-dlq-reprocessor.md).
**Статус: ✅ реализовано** (ветка `feature/dlq-reprocessor`; B1–B4 готовы, ожидает пред-сдачных гейтов и merge).

| Пункт | Статус | Где |
|---|---|---|
| §36.B1 Per-node `dlq_ttl_seconds` (дефолт 24ч) | ✅ | миграция [0017_node_dlq_ttl](../migrations/0017_node_dlq_ttl.up.sql); [domain/node.go](../internal/domain/node.go) (поле + `SetDefaults` 86400 + `Validate` [60, 2592000] + [errors.go](../internal/domain/errors.go) `ErrNodeDLQTTLRange`); PG-маппер [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go) (последний столбец, без перенумерации $-параметров); DTO [dto.go](../internal/web/adapter/in/http/dto.go); UI-форма [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (в секундах + подсказка в часах) + [nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts) + i18n; тесты domain + integration round-trip |
| §36.B1.2 Per-node `dlq_retry_delay_seconds` (дефолт 5 мин) | ✅ | миграция [0018_node_dlq_retry_delay](../migrations/0018_node_dlq_retry_delay.up.sql) (`NOT NULL DEFAULT 300` атомарно заполняет существующие узлы); [domain/node.go](../internal/domain/node.go) (`SetDefaults` 300 + `Validate` [1, 86400] + `ErrNodeDLQRetryDelayRange`); PG-маппер (последний столбец); DTO + swagger; UI-форма + `nodeValidation.ts` + i18n + пересборка бандла; min-backoff перед повтором ошибочной доставки (header `next_attempt_at`, §36.4). Дефолт 300с = `reprocess_interval` |
| §36.B2 Sender-репроцессор (sweeper над DLQ) | ✅ | usecase [dlq_reprocess.go](../internal/sender/usecase/dlq_reprocess.go) (`DLQReprocessor.ProcessMessage`: резолв→tombstone→TTL→статус→retry-backoff→breaker→`Send`); адаптер-sweeper [adapter/in/kafka/dlq_reprocessor.go](../internal/sender/adapter/in/kafka/dlq_reprocessor.go) (период. проход, отдельная группа `<group>-dlq-reprocess`); [circuitbreaker.IsOpen](../internal/platform/circuitbreaker/redis.go) (read-only, open∧cooldown); [kafka.NewConsumerWithGroup](../internal/platform/kafka/consumer.go); метрики `nexus_dlq_reprocess_total`/`_duration_seconds` ([metrics.go](../internal/platform/metrics/metrics.go)); unit-тесты |
| §36.B3 Global-конфиг + wiring (`safego.Go` в app.go, Stop) | ✅ | секция [config.go](../internal/platform/config/config.go) `SenderReprocessorConfig` (`disabled`/`interval_sec` 300/`max_scan` 1000) + [defaults.go](../internal/platform/config/defaults.go); wiring [sender/app.go](../internal/sender/app.go) (конструкция под `!disabled`, `safego.Go`, `Stop()`+`Await` done-канала); config-файлы (`config.yml`/`config.example.yml`/`config_debug.yml`); CHANGELOG + DEPLOYMENT |
| §36.B4 UI-подсказка «повторяется до TTL» | ✅ | [QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx) — под заголовком «Неудачные доставки» подсказка `queue.failed.reprocess_hint` (TTL узла в часах + про форс-«Повторить»); `dlq_ttl_seconds`/`dlq_retry_delay_seconds` добавлены в тип `Node` ([client.ts](../web-ui/src/api/client.ts)); i18n en/ru; бандл пересобран. Колонка «попыток» (header `attempts`) — опциональна, отложена |

**Неочевидности (B1/B1.2).**
- **Новый столбец узла — добавлять ПОСЛЕДНИМ** в `node_repo.go` (Create INSERT/VALUES/args, Update SET/args,
  `nodeColumns` SELECT + scan): тогда новый позиционный `$N` — в конце, без перенумерации существующих
  параметров (риск рассинхрона). Проверять обязательно integration `make test-int-pg` (round-trip).
- **`Node.Validate()` безусловно проверяет диапазон новых int-полей** — узлы, собираемые в тестах ВРУЧНУЮ
  (без `SetDefaults`), должны явно задавать `DLQTTLSeconds`/`DLQRetryDelaySeconds`, иначе `Validate` падает на
  нуле (поймано в `receiver/usecase/webhook_signature_test.go` — латентно с B1).

**Неочевидности (B2).**
- **Backoff повтора — `max(reprocess_interval, dlq_retry_delay_seconds)`.** Sweeper тикает раз в интервал;
  `next_attempt_at` лишь не даёт повторить РАНЬШЕ задержки. По умолчанию оба = 5 мин (`interval_sec` 300 =
  `dlq_retry_delay_seconds` 300) → повтор раз в ~5 мин. Per-node задержка «прижимает» паузу снизу, если
  глобальный интервал прохода меньше.
- **`Breaker.IsOpen` (новый) — read-only и учитывает cooldown.** `State()` всегда возвращает `open` пока
  ключ жив (не учитывает истёкший cooldown), `Allow()` расходует half-open-пробу. Для шага 5 нужен именно
  «open И cooldown не истёк», без побочных эффектов — иначе восстановившийся адрес (cooldown прошёл) навсегда
  бы откладывался.
- **Анти-busy-loop `seen[id]`+break — проверять wrap СТРОГО ПОСЛЕ `Commit`.** Sweeper завершает проход,
  встретив id, уже обработанный в этом проходе (догнал свой republish-хвост); копии за хвостом — на следующем
  проходе. **Грабли (поймано стендом, потеря данных):** ранний вариант делал `break` ДО `Commit` — kafka-go
  продвигает курсор за каждое прочитанное, поэтому прерывание без коммита оставляло republish-копию
  незакоммиченной, а коммит последующих сообщений прокатывал offset мимо неё → **сообщение терялось**. Фикс:
  Commit прочитанного → потом seen-check/break; тогда committed-offset = живой republish-копии у хвоста,
  она перечитается следующим проходом. `ReadLag` для bounding НЕ годится — kafka-go возвращает
  `errNotAvailableWithGroup` для consumer-group ридеров. Integration с 1 сообщением баг не ловил — нужен
  мульти-сообщение + цикл fail→republish→recovery (на стенде терялось 1 из 6).
- **`ReprocessRetry` прерывает проход** (не коммитим и не идём дальше): commit в kafka-go — «до и включительно»,
  поэтому коммит следующего сообщения «проглотил» бы offset несохранённого. Перечит — на rebalance/рестарте.
- **Метрика `result=dropped`** добавлена сверх 4 меток ТЗ — для терминальных drop'ов (узел удалён/disabled/
  отменён), которые не `ttl_dropped` и не `skipped`.
- **Новую колонку узла, нужную Sender'у, добавлять В ОБА ридера.** Sender читает узлы НЕ через web-репозиторий
  [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go), а через свой
  [sender/adapter/out/nodepg/reader.go](../internal/sender/adapter/out/nodepg/reader.go) (`selectByPath`).
  Грабли (поймано стендом + integration `TestSender_DLQReprocessor_E2E`): B1/B1.2 добавили `dlq_ttl_seconds`/
  `dlq_retry_delay_seconds` только в web-репозиторий → sender-ридер возвращал `DLQTTLSeconds=0` → в репроцессоре
  `ttl=0` → `now-received_at > 0` истинно всегда → **репроцессор отправлял ВСЁ в `ttl_dropped`, повторной
  доставки не было**. Unit-тесты строят `domain.Node` напрямую с TTL и баг не ловили — нужен integration через
  реальный `nodepg.Reader`.

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
  nexus_spec.md      сводный документ
  IMPLEMENTATION.md  ЭТОТ ФАЙЛ
```

---

## 4. Архитектурные решения и неочевидности

Эти моменты не очевидны из кода без контекста — стоит держать в голове при доработке.

### 4.0 Базовый путь API — `/api/v1` и единый вход через Web

Боевые эндпоинты Receiver живут под `/api/v1/request`, `/api/v1/requestAsync`,
`/api/v1/callback` (раньше было `/v1/...`). Причина: клиенты обращаются к шине через
**единый хост Web Service** (тот же, что отдаёт админку). Web реверс-проксирует
`/api/v1/request|requestAsync|callback` в Receiver
([receiver_proxy.go](../internal/web/adapter/in/http/receiver_proxy.go), регистрируется в
[app.go](../internal/web/app.go) **до** `SPAFallback`). Префикс `/api` критичен: SPA-fallback
отдаёт `index.html` на всё, что **не** начинается с `/api/` — поэтому старый `/v1/request`
возвращал клиенту HTML вместо ответа узла (баг). Грабли при доработке:

- Шаблон gin-роута теперь `/api/v1/request/*path` — это завязано в
  [metrics/gin.go](../internal/platform/metrics/gin.go) (`rootMethodFromPath`) и
  [sentry/middleware.go](../internal/platform/sentry/middleware.go) (`rootMethod`); меняешь путь —
  меняй и там, иначе сломается node-метка и имена спанов.
- Replay-диспетчер ([web/adapter/out/receiver/dispatcher.go](../internal/web/adapter/out/receiver/dispatcher.go))
  и loadtest ([cmd/loadtest/main.go](../cmd/loadtest/main.go)) бьют по `/api/v1/...`.
- `X-Forwarded-For` проставляется стандартным `httputil.ReverseProxy` — Receiver видит реальный
  IP клиента (важно для аудита/логов).

### 4.0.1 Стендовая валидация доработок (#1–#9)

Сквозной прогон на реальном стенде (deps в Docker + сервисы локально + echosrv +
RabbitMQ) подтвердил, см. [docs/STAND_TESTING.md](STAND_TESTING.md):

- **#1/#6** единый вход `POST :8000/api/v1/request/...` → прокси → Receiver →
  Sender → echosrv: ответ = тело+заголовки получателя (JSON + `X-Echo`), не HTML;
  пустое тело узла `/empty` → `Content-Length: 0`.
- **#5** запрос неверным методом → `405`; исходящий метод узла диктует вызов
  (echo.method и колонка `method` в логе совпадают с `outgoing_method`).
- **#7** async-успех `{"result":true,id}`, ошибка → `404` + `{"result":false,"message":"node not found"}`.
- **#8** RabbitMQAsync: 50 опубликованных сообщений вытянуты puller'ом → доставлены
  → 50 строк в ClickHouse (`type=requestAsync`, `status=200`); синхронного
  `result`-ответа нет (ожидаемо).
- **#4** IP в аудите и в ClickHouse-логах = `127.0.0.1` (IPv4), не `::1`.
- CH-шаблон (default «Standard logs») → авто-создание таблицы и запись логов;
  при недоступности CH проваленный батч уходит в Kafka-топик `nexus.logs.retry`
  и дренится обратно после восстановления (§38, заменил NDJSON-fallback).
- Метрики `nexus_requests_total{method,node,status}` растут по узлам (200/404/405).

Грабли локального запуска: `config_debug.yml` должен задавать `web.receiver_url:
http://localhost:8080` (дефолт `http://receiver:8080` — docker-имя, локально не
резолвится), иначе единый вход отдаёт 502.

### 4.0.2 QA-прогон 2026-06: найденные дефекты и фиксы

Сквозной QA-прогон (unit+integration+lint+security, посев стенда, Playwright по UI
под всеми ролями) выявил и закрыл:

- **Атрибуция актёра в аудите.** `actorFromCtx` ставил `UserID`/`TeamID` из сессии, но
  `UserLogin` оставался `"system"` (из `SystemActor`) — все действия писались как
  «system» при верном `user_id`. Фикс: в `domain.Session` добавлено поле `Login`
  (заполняется при входе и для API-токенов), `actorFromCtx` берёт логин из сессии
  (на legacy-сессиях без поля — graceful fallback в «system»). Тест:
  [actor_test.go](../internal/web/adapter/in/http/actor_test.go).
- **Ленивые тела логов/аудита (§7.4.2).** list/stream возвращали полные
  `request`/`response` для всех строк → на больших payload'ах фронт вис. Теперь тела
  тянутся по клику на строку через `GET /api/nodes/{id}/log/{logId}`; аудит
  сериализует JSON только при раскрытии. Тесты: `TestLogs_GetByID_*` в
  [logs_test.go](../internal/web/usecase/logs_test.go).
- **i18n: сырой ключ в UI.** [ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx)
  запрашивал `common.updated_at`, которого не было в namespace `common` (он лежал в
  `settings.common`) → подпись «common.updated_at». Добавлен ключ в `common` обеих
  локалей.
- **RBAC: кнопка «Перенести».** Move узла — admin-only, но кнопка показывалась
  viewer/manager (бэк отвечал 403). Гейт по `useRoleAtLeast("admin")` в
  [Overview.tsx](../web-ui/src/pages/Overview.tsx).
- **Стенд-скрипт под Windows PowerShell 5.1.** `seed_and_test.ps1` (UTF-8 без BOM +
  PS7-конструкция `(if …)`) не парсился штатным PS 5.1 — добавлен BOM, `(if …)`
  заменён на присваивание во временную переменную.

RBAC под Playwright подтверждён по трём ролям: admin (всё), manager (узлы CRUD +
Аудит + Allowed Hosts, без Kafka/Users/Teams), viewer (только просмотр; New/Edit
скрыты, формы редактирования и `/audit` редиректят, Kafka — in-page «только админ»).

### 4.0.2 No-loss проверка loadtest — poll-until-stable, не единичный замер

Проверка «нет потерь» ([cmd/loadtest/noloss.go](../cmd/loadtest/noloss.go))
сверяет число строк `type IN (requestAsync,RabbitMQAsync)` в CH с числом
отправленных async+rmq. **Грабли:** доставка async (Kafka) и rmq
(RMQ→puller→Kafka) — асинхронная at-least-once. После остановки нагрузки sender
ещё разгребает бэклог: на CI-раннере предлагаемая «логируемая» нагрузка
(~105/с при 300 rps) обгоняет скорость дренажа, и к моменту замера в CH ещё не
все строки. Изначальный единичный `count()` после фиксированных 15 с считал
недоставленное «потерей» (ложный `FAIL: message loss`, см. прогон на master
03.06.2026: `ch_rows=8713 < 13909`, при `errors=0`).

Решение: `pollUntilStable` опрашивает CH с интервалом `--ch-flush-grace` и
выходит когда `rows>=expected` (потерь нет, ранний выход), либо count перестал
расти `noLossStableRounds=3` опросов подряд (плато → бэклог разгрёбся, и если
`<expected` — это уже реальная потеря), либо истёк `--ch-noloss-max-wait`.
Так растущий бэклог («ещё дренируется») отличается от настоящей потери.
Job `loadtest` в CI — `allow_failure: true` (early-warning, не gate).

**Грабли-2 (07.06.2026, `ch_rows=12808 < 13993`, `errors=0`):** poll-until-stable
сам по себе не закрыл проблему — `pollOutcome` различает _три_ исхода, но старый
код приравнивал «истёк maxWait, пока count ещё рос» к потере. На самом деле:
- `pollPlateau` ниже expected — **подтверждённая** потеря (`FAIL`);
- `pollMaxWait`/`pollCtxDone` при растущем count — **inconclusive** (`WARN`, не
  `FAIL`): at-least-once + Kafka хранит непрочитанное, сообщения не потеряны, просто
  не успели слиться. Поле `report.no_loss_inconclusive`, `passed()` такой прогон
  не валит.

Первопричина окна: async-consumer ([kafka/consumer.go](../internal/sender/adapter/in/kafka/consumer.go))
обрабатывает сообщения **последовательно** на горутину (`FetchMessage`→`Handle`→
синхронный HTTP ~50мс→`Commit`); при `instances=4` потолок ≈50-70 msg/s, а в async-путь
при `target_rps=300` льётся ~117 msg/s (async ~87 + rmq-republish ~30). За 2 мин
копится бэклог ~7-8k, дренаж ≈145с > дефолтных 120с maxWait → обрезка на растущем
count. Фикс: CI передаёт `--ch-noloss-max-wait 5m` (var `LOADTEST_CH_NOLOSS_MAX_WAIT`),
проверка успевает дойти до `rows>=expected` и даёт чистый PASS; семантика
inconclusive — страховка от любого слишком короткого окна впредь.

**Грабли-3 (22.06.2026, `ch_rows=0 < 13788`, `errors=0`):** при зелёном HTTP-слое
no-loss упал с **нулём** строк. Причина — дрейф схемы: §39 добавил колонку
`http_method` в канон ([RequiredLogColumns](../internal/domain/ch_log_schema.go)),
а CH-таблицу `nexus_default.loadtest` CI создавал **рукописным** `CREATE TABLE` в
`.gitlab-ci.yml` (шаг 2.5), который колонку не получил → каждый batch INSERT
Sender'а падал с `No such column http_method`, §38-retry в Kafka тоже падал
(`Message Size Too Large`, см. 4.0.4) → строки не доходили никуда. **Фикс:** таблицу
теперь создаёт сам `cmd/loadtest` из канонной схемы тем же рендерером, что и
прод-таблицы узлов ([ensure_table.go](../cmd/loadtest/ensure_table.go) →
`domain.CHTemplate.RenderCreateTable`); инлайн-DDL из CI удалён. Единый источник
истины — **не возвращать рукописный DDL в CI**, иначе дрейф повторится.

### 4.0.3 Версия — единый источник истины git (ldflags из `git describe`)

Версия приложения берётся **только** из git и вшивается в бинарь на этапе сборки через
`ldflags -X nexus/internal/platform/build.Version=…`. Дальше она без правок доезжает в
логи старта, `--version`, метрики, Sentry-release и `GET /api/version`/футер SPA. На
сервере и в файлах версию руками не задают — выпуск = новый git-тег + пересборка на
сервере (`docker compose up -d --build`). Источник версии — [DEPLOYMENT.md §9.0](../DEPLOYMENT.md).

> **Образы в CI больше не собираются** (job `release`/GoReleaser удалён — требовал
> DinD/buildx/docker.io-auth на раннере и нестабильно работал, см. [Unreleased] в
> [CHANGELOG.md](../CHANGELOG.md)). Деплой — сборкой из исходников на сервере; версия там
> вшивается из git точно так же (Dockerfile считает `git describe` внутри builder-стейджа).

Рефакторинг закрыл **три бага**, из-за которых git-версия раньше не доезжала вообще:

1. **Молчаливый промах ldflags в GoReleaser.** В `.goreleaser.yaml` путь символа был
   `bus/internal/platform/build.Version`, а модуль — `nexus`. Линкер Go **молча
   игнорирует** `-X` для несуществующего символа (by-design, не ошибка) → даже релиз по
   тегу оставлял `build.Version=""` и падал на fallback `versioninfo.json`. Фикс:
   `bus/` → `nexus/` во всех `-X` (builds receiver/sender/web/rotate-key/loadtest).
2. **Config перекрывал ldflags.** [bootstrap.go](../internal/platform/bootstrap/bootstrap.go)
   ставил `cfg.Build.Version = buildOpt.Version` только `if cfg.Build.Version == ""`, но
   YAML всегда давал `version: ${VERSION:0.1.0}` (непусто) → guard не срабатывал. Фикс:
   приоритет инвертирован — `if buildOpt.Version != "" { cfg.Build.Version = … }`. А
   `buildOpt.Version` теперь **всегда непуст** (минимум `0.0.0-dev` из versioninfo.json),
   значит config-версия всегда перекрывается. Ключ `build.version` убран из
   `config.example.yml`; в `config_debug.yml` он остаётся (файл под git **skip-worktree**,
   правится локально и в коммит не идёт), но игнорируется тем же override.
3. **`/api/version` читал именно `cfg.Build.Version`** (не `buildOpt.Version`) — после
   фикса #2 это та же git-версия.

Неочевидности для будущих доработок:

- **`cmd/*/versioninfo.json` теперь несут `ProductVersion: 0.0.0-dev`** — это fallback-маркер
  «собрано без git/ldflags», а НЕ значение для бампа. `build.NewOption` использует его,
  только если `build.Version` пуст. `web-ui/package.json` (`version`) к `/api/version`
  отношения не имеет (фронт берёт версию из бэкенда).
- **`VERSION` в `.env` больше не используется.** Был селектором тега образа на
  registry-пути, но registry/CI-Docker убраны (деплой — сборкой на сервере, §9.1) —
  строку можно удалить.
- **Docker-сборка из исходников** вычисляет `git describe` **внутри** builder-стейджа —
  значит `.git` обязан попасть в build-контекст. В `.dockerignore` он намеренно **не**
  исключён (см. комментарий в файле). Дата — `%cI` (дата коммита), переносимо Windows/Linux.
- **Ведущий `v` тега срезается** (Makefile `patsubst v%,%`, Dockerfile `${VERSION#v}`):
  `git describe` отдаёт `v0.1.0`, а GoReleaser `{{ .Version }}` — `0.1.0`, и футер SPA
  ([Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)) сам добавляет `v`. Без среза вышло
  бы `vv0.1.0`. Короткий хеш (hex, без тега) на `v` не начинается — остаётся как есть.
- **CI не собирает образы.** Job `release` (GoReleaser → registry) удалён вместе со стадией
  `release` и `GIT_DEPTH` ([.gitlab-ci.yml](../.gitlab-ci.yml)); тег `v*` гоняет
  test/lint/build + loadtest (как `master`). `.goreleaser.yaml`,
  `deploy/docker/release.Dockerfile` и make `release-*` **удалены** (фикс `bus→nexus`
  из бага №1 ушёл вместе с ними — он касался уже неиспользуемого GoReleaser).
  Версионированные образы строит сервер при `docker compose up -d --build` — там `.git`
  нужен в контексте, дата `%cI`, ведущий `v` тега срезается (`${VERSION#v}`/`patsubst`).

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
`nexus.logs` (out-of-scope в v1). SSE отклоняет API-токены через
[RequireSessionOnly](../internal/web/adapter/in/http/api_token_middleware.go) — только UI-сессии.

#### 4.6.1 Узел без `clickhouse_table` — штатное состояние, не 500

`clickhouse_table` опционален: таблица логов провижинится только если задан
`clickhouse_template_id` **и** непустое имя таблицы (см. `NodeUsecase.provisionTable`).
Узел без таблицы логировать не может — это норма, а не сбой. Поэтому
[LogsUsecase.resolveNode](../internal/web/usecase/logs.go) возвращает sentinel
`domain.ErrNodeLogsNotConfigured`, а [logs_handler.go](../internal/web/adapter/in/http/logs_handler.go)
маппит его в `200 {"items":[], "logs_configured":false}` (для `Stream` — SSE-event
`error`) **без ERR-лога** (как `ErrNodeNotFound`). Иначе поллинг UI (раз в 5с) +
SSE спамили `ERR logs list failed ... op=logs.list` и 500-ответами. Фронт
[NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) гасит поллинг/SSE по
`node.clickhouse_table === ""` и показывает баннер `logs.not_configured` со ссылкой
на настройки узла.

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

### 4.9 SPA-ассеты в embed.FS — `go:embed` + явный `/assets` route

Две вещи должны совпадать, иначе при открытии UI получаем белый экран:

1. Директива в [static.go](../internal/web/static/static.go) — `//go:embed index.html assets`
   (не только `index.html`): иначе `assets/*.js|*.css` не попадают в бинарь.
2. [SPAFallback](../internal/web/adapter/in/http/spa.go) регистрирует
   `r.StaticFS("/assets", http.FS(fs.Sub(embedFS, "assets")))` — отдаёт ассеты с
   корректным MIME. Без этого route запросы к `/assets/index-*.js` проваливаются в
   `NoRoute` → возвращается `index.html` с `text/html` → модульный скрипт не исполняется.

`make build-ui` копирует свежий `web-ui/dist/*` в `internal/web/static/` (ассеты
git-tracked — это источник embed; CI job `go-build` их не пересобирает). После правок
во фронте нужно пересобрать UI и закоммитить обновлённый бандл, иначе бинарь отдаёт
старый SPA.

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

**Грабли с языком.** Backend выбирает язык по `Accept-Language` (язык браузера),
а SPA — по `localStorage` (явный селектор). Они могут **не совпадать**: оператор
переключил UI на RU, но браузер шлёт `en` → сырой API-error приходит на английском.
Для ошибок шаблонов CH (§19) это решено гибридом: хендлер
[ch_template_handler.go](../internal/web/adapter/in/http/ch_template_handler.go)
отдаёт `{"error": <localized>, "code": "ch_template.name_format"}` —
SPA переводит по стабильному `code` в языке UI (`t(code, {defaultValue: error})`),
а `error` остаётся fallback'ом для не-UI клиентов и для динамических
live-ошибок ClickHouse в `Verify` (у которых `code` нет). Ключи `ch_template.*`
продублированы в обоих словарях. Этот же паттерн стоит применять к новым
полевым ошибкам валидации, где важно совпадение с языком UI и привязка к полю.

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

### 4.11.2 Metrics API панели — единый источник Prometheus, с деградацией (Phase 21.1 → 29)

Дашборды панели (§21) не считают агрегаты на лету в Go, а тянут готовые из **единого
источника — Prometheus** ([adapter/out/prometheus/client.go](../internal/web/adapter/out/prometheus/client.go)).
Это **query API** сервера Prometheus (`prometheus.url`), а не scrape-эндпоинт `/metrics`. Метка
`node` совпадает с `domain.Node.Path` — поэтому per-node merge на фронте идёт по path.

- Глобальные KPI Overview: incoming = `sum(increase(nexus_requests_total{service="receiver"}[24h]))`,
  outgoing = то же для `service="sender"`, errors = `…,status=~"0|[45].."`; очередь Kafka =
  `sum(nexus_kafka_lag)`; per-node throughput = `sum by (node)(…)`.
- **per-node KPI и ряд графика на странице узла** (`NodeKPI`/`NodeChart`): total/errors через
  `increase(nexus_requests_total)` / `increase(nexus_request_incomplete_total)` (errors = «незавершённые»,
  точный аналог прежнего CH-условия `status>=400 OR status=0 OR done=0`), delivered = total−errors,
  p95/p99 — через `histogram_quantile()` по `nexus_request_duration_seconds_bucket`.

**Почему ушли от ClickHouse (Phase 29).** Раньше per-node KPI/график считались напрямую из
CH-таблицы логов узла ([был] `adapter/out/clickhouse/metrics_reader.go`). Это ломалось, если у узла
выключено `logging_enabled`, CH недоступен или таблица пуста — метрики узла пропадали. Перенос на
Prometheus убрал последнюю зависимость метрик от CH: per-node счётчики Sender'а пишутся **независимо**
от логирования (sync — `grpc/sender_service.go`, async — `usecase/async.go`), поэтому метрики узла
видны всегда, а **ClickHouse остаётся чисто хранилищем логов**. Цена — приблизительные (по бакетам)
перцентили и глубина истории, ограниченная retention Prometheus (env `PROMETHEUS_RETENTION`, дефолт
`90d`). Под это расширены бакеты `nexus_request_duration_seconds` до 300с
([platform/metrics/metrics.go](../internal/platform/metrics/metrics.go)), т.к. `DefBuckets` упираются
в 10с при `timeout_ms` до 300с.

Источник **опционален**: при пустом `prometheus.url` провайдер не создаётся (nil), `MetricsUsecase`
отдаёт нули с `prometheus_available=false` / `chart_available=false`. Ошибка запроса к Prometheus в
`NodeMetrics` тоже **деградирует** (warning + `chart_available=false`), а не 500 — единообразно с
Overview/NodesOverview, чтобы поллинг UI не спамил ошибками
(см. [usecase/metrics.go](../internal/web/usecase/metrics.go)).

**Read-path логов тоже мягко деградирует при недоступности ClickHouse (CH-outage тест).**
Раньше эндпоинты ЧТЕНИЯ логов из CH — `List`/`CountFailed`/`Get`/`Stream` в
[logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) — при лежащем ClickHouse возвращали
**500** и логировали `ERROR`. На вкладках «Логи»/«Очередь» и в виджете «Последние запросы» это давало
вечный спиннер/«—» и, главное, **шторм 500-поллинга** (react-query retry + 12с-поллинг): за пару минут
открытой страницы read-path сгенерировал десятки Sentry-событий `logs list failed`/`logs failed-count
failed`, заваливая реальные write-path ошибки CH (`clickhouse batch insert failed` из Sender) в
соотношении ~9:1. Это подрывало саму цель «ошибки CH видны в Sentry».
Фикс: адаптер [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) классифицирует
ошибку (`classifyCHErr`/`chUnavailable`): сбой **доступности** (net.Error / `context.DeadlineExceeded` /
`Canceled`) помечается sentinel'ом `domain.ErrLogsBackendUnavailable`, а **серверная** ошибка запроса
(`*clickhouse.Exception` — битый SQL, нет таблицы) — нет (остаётся 500, чтобы баги не маскировались).
Хендлеры на sentinel отдают **200 с пустыми данными + `logs_available=false`** и логируют **WARN**
(не ERROR → не флудит Sentry). Фронт по флагу показывает индикатор «Логи временно недоступны
(ClickHouse)» (`logs.unavailable`, [LogsTab](../web-ui/src/components/node/LogsTab.tsx)/
[OverviewTab](../web-ui/src/components/node/OverviewTab.tsx)/[QueueTab](../web-ui/src/components/node/QueueTab.tsx)),
KPI «Неудачные доставки» — «—» (а не вводящий в заблуждение «0»). Проверено на стенде: при остановленном
CH read-path даёт **0** новых Sentry-событий и 0 console-ошибок, тогда как write-path
`clickhouse batch insert failed` продолжает уходить в Sentry, а sync/async-отправка не затрагивается
(durable-retry проваленных батчей — §38, Kafka вместо NDJSON). Единообразно с метриками выше.

**Консистентность метки `node` (in/out merge на дашборде).** Sender пишет метку `node = node.Path`
(без слога команды), а `GinMiddleware` по умолчанию брал сырой URL-параметр Receiver'а, который для
`/api/v1/request/<team>/<path>` включал слог (`default/stand/...`). Из-за этого per-node merge
incoming(Receiver)/outgoing(Sender) на `/api/metrics/nodes` разъезжался — у sender-строк `in`/спарклайн
оказывались нулевыми. Фикс: Receiver-handler кладёт чистый путь узла в контекст под
`metrics.NodeLabelKey` ([handler.go](../internal/receiver/adapter/in/http/handler.go)), а `GinMiddleware`
(`nodeLabel`) предпочитает его сырому параметру ([gin.go](../internal/platform/metrics/gin.go)). Для Web/
Sender ключ не ставится — их поведение не меняется.

### 4.11.3 Редизайн UI под эталон: дизайн-токены + UI-kit + app-shell (Phase 21.2)

Фронтенд приводится к визуальному эталону [specs/nexus_ui.html](nexus_ui.html) (§21). Базис:

- **Дизайн-токены** ([web-ui/src/styles/globals.css](../web-ui/src/styles/globals.css) +
  [tailwind.config.js](../web-ui/tailwind.config.js)) — палитра/радиусы/шрифты эталона как
  CSS-переменные (RGB-тройки для opacity). Тёмная тема — основная, светлая зеркальная.
  Шрифты Inter + JetBrains Mono подключены ссылкой в [index.html](../web-ui/index.html) с
  graceful-fallback на системный стек.
- **UI-kit** [web-ui/src/components/ui/](../web-ui/src/components/ui/) — атомы эталона
  (Button, Input/Select/Textarea/Field, Card, SectionHead, Modal, Chip, Pill, Kpi/KpiRow,
  Seg, Hint, PickGroup, Toggle3). Снимает дублирование инлайн-классов на экранах.
- **App-shell** — постоянный левый сайдбар [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)
  + тонкий топбар [Topbar.tsx](../web-ui/src/components/Topbar.tsx) (крошки, team-switcher,
  переключатель языка и **темы**), композит [AppShell.tsx](../web-ui/src/components/AppShell.tsx).
  В [App.tsx](../web-ui/src/App.tsx) защищённые маршруты идут layout-route'ом через `<Outlet/>`
  (раньше каждый экран рисовал свой `<Topbar/>`). Тема — общий хелпер
  [lib/theme.ts](../web-ui/src/lib/theme.ts).

Экраны перестилизовываются поэтапно (Phase 21.3+); до этого они отрисовываются в новом
shell с обновлённой палитрой. Встроенный SPA в `internal/web/static` пересобирается
(`make build-ui`) в конце, когда UI завершён.

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
  `nexus_l2_cache_hits_total{kind="stale"}` и Warn в логи.
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

### 4.13 Kafka — `apache/kafka` (KRaft), не Bitnami и не Confluent

В августе 2025 Bitnami сняли публичные теги `bitnami/kafka:*` с docker.io
(перевели в `bitnamilegacy/`). Чтобы не садиться на legacy и не плодить
разные образы в dev/CI/integration, везде используется официальный upstream
`apache/kafka:3.9.0` (KRaft, single-broker):

- [deploy/docker-compose.yml](../deploy/docker-compose.yml) — env-переменные
  без префикса `_CFG_` (это был bitnami-wrapper), `CLUSTER_ID` зашит, чтобы
  volume не реинициализировался при пересоздании контейнера, log-dir —
  `/var/lib/kafka/data`, healthcheck — `/opt/kafka/bin/kafka-topics.sh`.
- [tests/integration/receiver_async_test.go](../tests/integration/receiver_async_test.go)
  — `tckafka.Run(ctx, "apache/kafka:3.9.0")`; testcontainers-go v0.42 модуль
  `kafka` поддерживает `apache/kafka:3.7+`.

Если будете обновлять минорку — меняйте все два места разом, иначе CI и
docker-compose-стек начнут тянуть разные образы и расходиться по поведению
(например, дефолтным retention'ам).

### 4.20 Topbar team-switcher вызывает `qc.invalidateQueries()` без аргументов

Phase 10.F.3 ставит `<select>` в [Topbar](../web-ui/src/components/Topbar.tsx).
При смене команды через `POST /api/me/switch-team` (Phase 10.B.1) сервер
переписывает `current_team_id` в Redis-сессии, и **все** последующие
запросы идут в новый scope. Чтобы это сразу же увидел UI, после успешной
мутации вызывается `qc.invalidateQueries()` **без queryKey** — это
сбрасывает все кеши TanStack Query.

Альтернатива (точечный invalidate `["nodes"]`, `["audit"]`, …) была
отвергнута: легко забыть добавить новый key, когда появится новая
страница. Глобальный invalidate — простой и stay-correct по
конструкции.

Cookie сессии не меняется (см. §4.15). UI ничего не редиректит — текущая
страница перерисовывается с новыми данными.

### 4.19 Receiver URL: `team_slug` в первом сегменте path, не в host/subdomain

Phase 10.E.1 расширила URL Receiver'а под multi-tenancy:
`/v1/request/<team_slug>/<node_path>`. Альтернатива — поддомен
(`acme.nexus.local/v1/request/<path>`) — была отвергнута:

- Поддомен требует wildcard-сертификат и DNS-управление на каждой
  команде; URL-сегмент работает на любом deploy без правки инфры.
- `splitTeamSlugAndPath` ([handler.go](../internal/receiver/adapter/in/http/handler.go))
  парсит catch-all `/*path` Gin'а: первый сегмент = `team_slug`,
  остальное = `node_path`. Один сегмент = legacy URL без слога
  (default-team — `domain.DefaultTeamSlug`). Это позволяет
  одновременно обслуживать новые и существующие интеграции.
- В Postgres `nodes` после миграции 0008 имеет `UNIQUE(team_id, path)`,
  не `UNIQUE(path)`. Без team_slug в URL запрос
  `WHERE path = ?` для одинаковых имён узлов в разных командах
  неоднозначен — резолв через JOIN с teams избегает этой проблемы.

Cross-team-изоляция в Receiver — следствие JOIN'а в
[nodecache.Reader.getFromPg](../internal/receiver/adapter/out/nodecache/reader.go):
если slug в URL не совпадает с реальной командой узла, запрос вернёт
`ErrNodeNotFound` (404), а не утечку существования. Фиксируется
unit-тестом `TestSplitTeamSlugAndPath`.

API-токены в Receiver не используются — incoming auth узла (basic /
token / webhook_signature) остаётся ответственным за аутентификацию
клиента, а scope обеспечивается принадлежностью узла команде.

### 4.18 OrphanScanner — allow-list по `teams.ch_database`, не одна БД из конфига

Phase 10.D.2 заменила «одна `chCfg.Database` = единственная сканируемая
БД» на динамический allow-list: при каждом `Scan()` сканер вычитывает
`teams.List()` и проходит по каждой `ch_database`. Это даёт:

- **Multi-team по умолчанию.** Любая команда, созданная через
  `/api/teams` (Phase 10.C.1), автоматически попадает под сканирование
  без рестарта Web.
- **Drop guard сужается до tenant-БД.** Раньше можно было ошибочно
  передать `system.X` или `default.X` — теперь DROP допускается только
  в БД, зарегистрированной в `teams`. `system`, `default`,
  `information_schema` физически невозможно дёрнуть через API.
- **Узлы одной команды не считаются orphan'ами в чужой БД.** Раньше
  `knownTables` фильтровал по `defaultTeamID`, и узлы acme в `nexus_acme`
  могли «утечь» в orphan-список Web'а, смотрящего на default. Теперь
  `knownTables` обходит все команды.

`ch_housekeeping` (Sender) не требует аналогичной правки: он работает
от `nodes.ListForHousekeeping` (без team-фильтра) и `splitDBTable` уже
парсит `db.table` из `nodes.clickhouse_table` — после нормализации в Web
(Phase 10.C.2) это всегда `nexus_<slug>.<table>`.

### 4.17 `nodes.clickhouse_table` хранит полный `db.table`, маршрутизация на стороне Web

Phase 10.C перенесла резолв «в какую CH-БД пишет узел» с runtime-time
(Sender JOIN-ит teams) на write-time (Web нормализует поле при
Create/Update). [NodeUsecase.normalizeCHTable](../internal/web/usecase/node.go)
вызывается до `Validate()` — если в `n.ClickHouseTable` нет точки и
`n.TeamID` известен, поле обогащается префиксом
`<team.ch_database>.<table>`. После этого:

- Sender ([chlog.Writer.Write](../internal/sender/adapter/out/chlog/writer.go))
  передаёт значение в `INSERT INTO %s` без изменений — CH парсит `db.table`.
- [CHHousekeeping.dropPartitionsOlderThan](../internal/sender/usecase/ch_housekeeping.go)
  использует `splitDBTable(name)` — уже умеет.
- Receiver/Web log-read через [LogReaderCH](../internal/web/adapter/out/clickhouse/log_reader.go)
  тоже получает `db.table` и собирает запрос напрямую.

Sender за каждое сообщение НЕ ходит в PG за `teams.ch_database` — это
было бы +1 query на каждый async/sync вызов. Цена за подход: при
переименовании команды (которое запрещено в TeamUsecase.Update) пришлось
бы мигрировать все nodes.clickhouse_table. Поэтому `teams.slug` и
`teams.ch_database` immutable.

Стенд greenfield — узлов со старым/unprefixed форматом нет, поэтому
backfill-миграция не нужна. Бывшая backfill-миграция удалена, а
`ch_templates` переименована 0010 → 0009, чтобы нумерация шла подряд
(БД, где уже была применена старая 0010, нужно пересоздать — данных нет).
Новые узлы через UI всегда получают корректный префикс
`nexus_<slug>.<table>` автоматически (`normalizeCHTable`).

### 4.15 Team-switcher: `current_team_id` в Redis-сессии, не в cookie

Phase 10.B.1 кладёт UUID активной команды в `domain.Session.CurrentTeamID`
и сериализует вместе с сессией в Redis. Cookie `nexus_session` остаётся
неизменной — клиент при switch'е не получает новый токен, и сессия не
инвалидируется. Это сознательное решение:

- Cookie — стабильный идентификатор; UI не должен ребэйнднуть пользователя
  при каждом switch.
- Все живые табы того же юзера моментально получают новый scope (они
  ходят в Redis по тому же session_token).
- TTL сессии не меняется (Touch не вызывается специально из SwitchTeam).

API-токены (`Bearer db_…`) имеют свою «псевдо-сессию», которую собирает
[api_token_middleware](../internal/web/adapter/in/http/api_token_middleware.go) —
там `CurrentTeamID` берётся из `api_tokens.team_id` и переключать его
нельзя ([routes.go](../internal/web/adapter/in/http/routes.go):
`POST /api/me/switch-team` обёрнут `RequireSessionOnly()`).

### 4.16 Cross-team Update узла возвращает ErrPermissionDenied, не 404

Phase 10.B.2 различает два сценария в `NodeUsecase.Update`:

- `old.TeamID != teamID` (узел из чужой команды): `ErrNodeNotFound` (404).
  Скрываем существование чужих узлов — нельзя отличить «нет узла» от
  «узел есть, но в другой команде».
- `n.TeamID != old.TeamID` (попытка перенести узел): `ErrPermissionDenied`
  (403). Этот код срабатывает только если caller сам положил в `n.TeamID`
  чужой UUID; handler [node_handler.go](../internal/web/adapter/in/http/node_handler.go)
  всегда делает `updated.TeamID = existing.TeamID` до вызова, так что
  через UI попасть в этот путь нельзя — но usecase защищён от прямых
  вызовов (CLI, тесты).

Перенос узла между командами — отдельная операция (не часть Update).
Будет в блоке F вместе с UI «Команды».

### 4.14 `defaultTeamID` резолвится в Web-bootstrap, не из конфига

Phase 10.2 ввела `domain.Team` и `TeamRepository`, но существующий
single-team-код продолжает работать благодаря резолву UUID 'default'-team
**один раз при старте Web** ([internal/web/app.go](../internal/web/app.go),
`teamRepo.GetBySlug(ctx, domain.DefaultTeamSlug)`).

Полученный UUID передаётся в конструкторы [NodeUsecase](../internal/web/usecase/node.go)
и [OrphanScanner](../internal/web/usecase/orphan_scanner.go) как
fallback для случаев, когда handler не передал team scope (List без
filter, Create без TeamID). До блока B (team-switcher в сессии) это
единственный источник current_team_id.

Важные следствия:

- Если миграция 0008 не накачена — Web падает на старте с
  `resolve default team: ... (run --migrate-up?)`. Это намеренный
  fail-fast — без default-team весь scope-резолв сломается.
- `domain.Node.SetDefaults` **больше не подставляет literal "default"**
  в TeamID. Подстановка вынесена в usecase (где есть `defaultTeamID`).
  Не возвращайте literal обратно — UUID не совпадёт со slug.
- `users.team_id` в БД называется `default_team_id` (миграция 0008);
  поле в `domain.User` — тоже `DefaultTeamID`. JSON в `userResponse`
  — `default_team_id`. Старое имя `team_id` зарезервировано под
  current_team в блоке B.
- В integration-тестах используйте helper `resolveDefaultTeamID(t, ctx,
  pool)` ([tests/integration/node_repo_test.go](../tests/integration/node_repo_test.go))
  — он читает UUID из БД после применения миграций.

### 4.21 §22 — контроль логирования и единый источник алертов

- **Обрезка по рунам, checksum по полному телу.** `truncateRunes` в
  [send.go](../internal/sender/usecase/send.go) режет `[]rune`, а не байты — иначе многобайтовый
  UTF-8 рвётся и ClickHouse-строка бьётся. `checksum_request/response` считаются из `in.Body`/
  `resp.Body` ДО обрезки: контрольная сумма отражает реальный payload, даже если тело урезано.
- **`logging_enabled=false` обрывает запись на usecase-уровне**, а не в writer'е: guard стоит на
  обоих вызовах `u.logw.Write` (основной и circuit-breaker-open). Так узел не пишет вообще ничего,
  а не «пустую» строку.
- **Дефолт `logging_enabled` через `*bool` в DTO.** Plain `bool` не отличает «не прислано» от
  «false». Указатель: nil → true (старые клиенты и существующие узлы логируют как прежде).
- **`Node.UnmarshalJSON` дефолтит `LoggingEnabled=true`** ([node.go](../internal/domain/node.go)).
  Узел кешируется в Redis как JSON; запись, сериализованная до появления поля (переживший выкат
  L1-кеш ресивера, TTL `Redis.NodeTTLSec`=300с), без этого иначе читалась бы как `false` и на ≤5 мин
  выключила бы логирование узла. Кастомный Unmarshal закрывает окно: отсутствующее поле → `true`.
- **`done=0` ⟺ `!rec.Done` ⟺ всё ClickHouse-условие ошибки.** Поэтому один счётчик
  `nexus_request_incomplete_total` точно воспроизводит прежний `CountErrors`
  (`status>=400 OR status=0 OR done=0`): первые два — подмножества `done=0`. Инкремент в адаптерах
  Sender по `out.StatusCode` не 2xx (метрику в usecase не тащим).
- **Telegram теперь зависит от Prometheus.** `NotificationScheduler` больше не держит `LogReader`;
  ошибки берёт из `PromMetrics.NodeErrors` (один запрос на тик вместо N к ClickHouse). Планировщик
  вынесен из CH-блока в [app.go](../internal/web/app.go) и стартует только при `promMetrics != nil`.
- **p95 для карточек — без новой метрики.** Sender уже пишет `nexus_request_duration_seconds`
  и для sync ([sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go)), и для
  async ([async.go](../internal/sender/usecase/async.go)); карточкам нужен лишь
  `histogram_quantile` по `service="sender"`. Спарклайн — один `query_range` с `by (node)` на весь
  список, не N запросов.

### 4.22 §23 — каталог хостов: денормализованный снимок, Receiver нетронут

- **`nodes.url_allowed_hosts TEXT[]` — это снимок, не источник истины.** Источник — `node_allowed_hosts`
  (M2M). Receiver читает снимок из JSON-кеша узла (Redis) и **не знает про каталог** — горячий путь не
  изменился. При attach/detach Web в одной UoW-транзакции: `Link/Unlink` → `ListByNode` → пересборка
  снимка (`UpdateAllowedHostsSnapshot`) → после commit `nodeCache.Set`. Забыть `cache.Set` = тихий
  рассинхрон; покрыто unit-тестом (`TestHostUC_Attach_RebuildsSnapshotAndCache`).
- **`kind` кодируется в плоском массиве**, чтобы не менять формат кеша: `re:<pattern>` для regex
  (префикс безопасен — hostname не содержит `:`), exact/wildcard как есть. Матчер `domain.HostAllowed`
  распознаёт `re:`. **regex не лоуэркейзится** (иначе ломаются классы `\d`→`\D`); хост уже lower-case.
- **`NodeUsecase.Create/Update` игнорируют allowlist из тела узла** (Create → пусто, Update →
  сохраняет старый снимок). Управление только через каталог. Это сознательное изменение контракта
  `POST/PUT /api/nodes` — curl-клиент больше не сидит allowlist через тело узла.
- **Редактирование паттерна — только при `usage_count = 0`** (плюс FK RESTRICT на удаление). Это и
  гарантирует отсутствие стейл-снимков: используемый паттерн неизменяем.
- **`POST /api/allowed-hosts/preview` отвечает `200` даже на невалидный паттерн** (`{valid:false,
  reason:<i18n-код>}`), а НЕ `400`. Превью — это «что будет», и недописанный/кривой паттерн при живом
  вводе в форме (запрос летит на каждое нажатие клавиши) — нормальный ответ, а не ошибка клиента.
  Раньше отдавали `400` → консоль браузера засорялась красными `Failed to load resource (400)`, хотя
  UI работал. Только malformed JSON-тело по-прежнему `400` (ShouldBindJSON). Не «чини» обратно на
  `400`: [http/host_allowlist_handler.go](../internal/web/adapter/in/http/host_allowlist_handler.go)
  `Preview`, фронт читает флаг `valid` ([AllowedHosts.tsx](../web-ui/src/pages/settings/AllowedHosts.tsx)).
- **Редактирование хоста в UI — `PATCH /allowed-hosts/:id`** (роут — PATCH, не PUT; раньше фронт слал
  PUT → 404). В `api`-клиенте есть метод `patch` ([web-ui/src/api/client.ts](../web-ui/src/api/client.ts)).

### 4.23 §24 — headers_catalog: usage_count on-read, без M2M

- **Отдельной таблицы привязки нет.** Источник — `nodes.forward_headers TEXT[]` (его читает Receiver).
  `usage_count` считается коррелированным подзапросом (`unnest(forward_headers)` + `lower()`), а не
  trigger'ом на массив (тот хрупок). Форма узла шлёт имена (`string[]`), не ID — Receiver нетронут.
- **POST идемпотентен по `lower(name)`**: unique-violation ловится в usecase и резолвится в
  существующую запись (200). Combobox создаёт без диалогов и без гонок.
- **CRUD-управление (Phase 24.6/24.7): guard-при-использовании, а не каскад.** Т.к. узлы ссылаются на
  заголовок по имени (строкой в `forward_headers`), а не по ID, переименование/удаление используемой
  записи «осиротило» бы ссылки. Выбран самый безопасный вариант — блокировка: `usage_count > 0` →
  `ErrHeaderInUse` (409) на rename и delete, по образцу §23 `ErrHostInUse`; описание правится всегда.
  Узлы не трогаются (никакого `UPDATE nodes ...`), Receiver нетронут.
- **RBAC-асимметрия сознательна.** Страница управления и `PATCH`/`DELETE /api/headers/:id` — admin-only
  (`authedAdmin`), но `POST /api/headers` оставлен на manager+ (`authedManager`): тот же эндпоинт
  дёргает combobox формы узла (§24.4), доступный менеджерам — перенос create в admin сломал бы
  inline-создание. Вкладка Настройки → «Заголовки» гейтится `minRole: "admin"` в двух местах
  (`visibleTabs` + условный `<Route>`), как и остальные admin-вкладки.

### 4.25 §27 — RabbitMQAsync: неочевидности

- **`degraded` — runtime, не `node.status`.** Сознательно НЕ расширяли enum `NodeStatus` и его
  CHECK. Смешивать конфиг-статус (что выставил пользователь: enabled/paused/disabled) с health (что
  наблюдает воркер) нельзя — иначе `degraded` затирал бы `paused`, а воркер писал бы в конфиг.
  Health живёт в `domain.RMQHealth`, Receiver публикует снимок в Redis-hash `rmq:health`, Web читает
  его в `NodeResponse.rmq_status`. Метрика `nexus_node_degraded` — отдельный сигнал.
- **Puller в Receiver, не в Sender.** Sender ничего не знает про RabbitMQ — он потребляет из Kafka как
  обычно. Puller только перекладывает RMQ→Kafka, переиспользуя тот же producer и формат `Envelope`
  (плюс блок `rmq`). Это даёт единый конвейер с `requestAsync`.
- **Порядок ack строгий: Kafka `acks=all` → потом `basic.ack`.** Это даёт at-least-once: сбой между
  Kafka-ack и RMQ-ack → дубль (получатель должен быть идемпотентен по `message_id`); сбой до Kafka-ack
  → `basic.nack(requeue)` без потери. Producer уже `RequireAll`, отдельной настройки не нужно.
- **Reconcile из PG, не pub/sub.** Узлового Redis-события на CRUD нет (инвалидация кеша — по path, без
  сообщения). `PullerManager` периодически (`receiver.puller.reconcile_sec`, деф. 15с) сверяет список
  RabbitMQAsync-узлов из PG с запущенными воркерами, перезапуская при изменении `updated_at`. Задержка
  старта нового узла ≤ reconcile_sec — приемлемо для v1.
- **Отдельный PG-листер, не nodecache.Reader.** Reader не тянет `rmq_*`-колонки и расшифровку
  `rmq_password`; для Puller нужен полный конфиг → `rabbitmq.NodeLister`. paused-узлы из поллинга
  исключены (источник останавливается).
- **`basic.get` поллинг, не `basic.consume`.** v1 — простой предсказуемый поллинг с manual ack
  (push-consumer — §27.14 v2). `QueueDeclarePassive` при Connect проверяет существование очереди (404
  → backoff/degraded, не создаём).

### 4.26 §29 — комментарий узла и багфикс ключа node-кеша

- **`comment` — только метаданные UI.** Не участвует в маршрутизации, Receiver его не читает
  (в SELECT `nodecache` не добавлен). Лимит валидируется по **рунам** (`utf8.RuneCountInString`),
  а не байтам, чтобы совпадать с PG-`CHECK length()` (символы) и DTO-binding `max` (validator
  считает руны) — иначе 2000 кириллических символов давали бы расхождение «прошёл binding/PG, но
  отверг домен».
- **Багфикс рассинхронизации ключа node-кеша (Fix.B).** Web писал/инвалидировал Redis-ключ
  `node:<path>` ([redis/node_cache.go](../internal/web/adapter/out/redis/node_cache.go)), а Receiver
  читает `node:<team_slug>:<path>` (формат разошёлся после ввода `team_slug` в Phase 10.1, см. §10.E.1).
  Из-за этого write-through и инвалидация из Web **не доходили** до ключа Receiver, и изменения узла
  вступали в силу только по истечении Redis-TTL Receiver (`Redis.NodeTTLSec`=300с) — до 5 минут.
  Фикс: Web строит ключ через `domain.DefaultTeamSlug` (`node:default:<path>`) — идентично Receiver.
  В v1 команда всегда `default` (§0), поэтому достаточно; **v2 multi-tenancy** потребует резолва
  реального slug команды узла в этом адаптере (образец — `resolveCHDatabase` в `usecase/node.go`).
  После фикса изменения вступают в силу ≤ ~2с (L2 in-memory TTL Receiver).

### 4.27 §30 — паники, request_id и привязка к Sentry-hub через WithContext

- **Логгер v1.7.9 — `WithContext` доводит request-scoped hub до Sentry.** До v1.7.9
  методы логгера вызывали slog без контекста, поэтому `logger.Error` всегда капчурил в
  **глобальный** `sentry.CurrentHub()` — теги запроса (`request_id`/`node`), выставленные
  на склонированный hub в [sentry/middleware.go](../internal/platform/sentry/middleware.go),
  туда не попадали. В v1.7.9 методы логируют через `LogAttrs(l.context(), ...)`, и
  `logger.WithContext(c.Request.Context()).Error(...)` отправляет событие в hub из
  контекста (тот самый клонированный). Поэтому в [recovery-middleware](../internal/platform/recovery/middleware.go)
  не нужен ручной `hub.RecoverWithContext` — он дал бы **дубль** события. Для фоновых
  горутин (нет request-hub) `safego.Recover` логирует без контекста → в глобальный hub,
  что корректно.
- **Порядок middleware: `requestid → otel → sentry → recovery → metrics [→ i18n]`.**
  recovery стоит **после** sentry (иначе при панике `span.Status` не успевал выставиться в
  500 — sentry-middleware ставит его строкой после `c.Next()`), но **раньше** metrics и
  handler'ов (чтобы перехватывать их паники). requestid — строго первым, чтобы id был в
  контексте к моменту работы sentry/recovery. `gin.Recovery()` удалён везде.
- **request_id не перезаписывается.** Если клиент/вышестоящий сервис прислал `X-Request-Id`
  — он сохраняется (сквозная трассировка), иначе генерится UUID v4. Тег ставится и на
  scope hub'а (события), и на транзакцию (трейсы).
- **`safego.Recover` после `wg.Done()` в пулах.** В пулах (kafka consumer, chlog writer,
  puller worker) `defer wg.Done()` регистрируется первым (выполнится последним), а
  `defer safego.Recover` — после него в коде (выполнится первым, LIFO): сначала гасим
  панику, затем отрабатывает `wg.Done`, не оставляя WaitGroup висеть.

### 4.24 §25 — два swagger одним Web-бинарём

- **Изоляция генерации через `--exclude`.** swag сканирует всё дерево от searchDir; без `--exclude`
  web-док подхватил бы `/v1`-маршруты Receiver, а receiver-док — web-handler'ы с cross-package типами
  (`usecase.TestResult` → ошибка). Web исключает `cmd/receiver,internal/receiver`, Receiver —
  `cmd/web,internal/web`.
- **Разные `InstanceName`.** Web-док — default `swagger`, Receiver — `receiver` (`swag --instanceName`).
  Web раздаёт оба через `ginswagger.WrapHandler(..., InstanceName(...))`; blank-импорты обоих
  `docs/*`-пакетов регистрируют их в `swag.Registry`. Receiver — отдельный процесс, swagger UI к нему
  не подключён (мокап §25 это и предписывает).

### 4.28 QA-2026-02 — пакет исправлений по ручному тестированию

ТЗ и таблица всех 19 пунктов чек-листа — в [QA_FIXES_2026-02.md](QA_FIXES_2026-02.md);
отчёт о прогоне на стенде — в [QA_FIXES_2026-02_REPORT.md](QA_FIXES_2026-02_REPORT.md).
Ветка `fix/qa-2026-02`, блочные коммиты `Phase QA.N`. Неочевидности:

- **Replay не восстановит нелогированное тело (П1).** Если узел создан с `LogRequestBody=false`,
  `orig.Request` в логе пуст — replay физически не из чего собрать тело. Поэтому при пустом orig-теле
  и не заданном override возвращается `ErrReplayBodyUnavailable` → 422, а не молчаливая отправка пустого
  body (которая давала 400 «empty body» + circuit breaker 503). См.
  [replay.go](../internal/web/usecase/replay.go).
- **`AppSettingsRepoPg.Update` обязан перечислять ВСЕ секции (П8/П13).** JSON-документ собирается из
  анонимного struct; забытая секция «молчаливо теряется» при записи (так пропал `general`). Тест-страж —
  `TestAppSettingsRepo_GeneralRoundTrip_E2E`.
- **must_change_password — реальный gate (П18).** Флаг живёт в `domain.Session` и проверяется middleware
  `RequirePasswordChanged` (allowlist: `/me/password`, `/auth/me`, `/auth/logout`) → 403
  `password_change_required`; фронт по этому показывает обязательный экран `ForcePasswordChange`.
- **Аудит — manager+ (П6).** Бэкенд не ослаблялся (§26); скрыт пункт меню и роут `/audit` для viewer.
- **Swagger Model (П10).** Все ответы декларируются конкретными DTO (`ErrorResponse` + обёртки в
  [dto_common.go](../internal/web/adapter/in/http/dto_common.go)) вместо `map[string]any`.
- **Модальное подтверждение (П16).** `useConfirm`/`ConfirmProvider` вместо `window.confirm` во всех
  местах удаления; провайдер монтируется один раз в `main.tsx`.

### 4.0.4 §38 durable-retry — выравнивание лимита размера сообщения Kafka

`Message Size Too Large` при produce в `nexus.logs.retry` (всплыло в loadtest
22.06.2026 вместе с грабли-3 §4.0.2). Цепочка: при недоступности CH §38 шлёт
проваленный батч логов в Kafka; батч несёт тела request/response и достигает
неск. МБ. Топик создаётся с `max.message.bytes = kafka.topic.max_message_bytes`
(config.yml = 10 МиБ), и `clogwire.Split` режет под этот же порог — но
**брокер** kafka в [deploy/docker-compose.yml](../deploy/docker-compose.yml) не
задавал `message.max.bytes`, и его **дефолт ~1 МиБ перебивал** per-topic-конфиг
→ сообщение >1 МиБ отвергалось, строки терялись (NDJSON-fallback убран в §38).

Это **прод-баг**, не только CI: тот же брокер-compose в проде, тот же дефолт.
Фикс — две части:
1. **Брокер.** `KAFKA_MESSAGE_MAX_BYTES`/`KAFKA_REPLICA_FETCH_MAX_BYTES = 10485760`
   в compose — брокерский потолок не ниже per-topic лимита.
2. **Запас на фрейминг.** `chunkLimit` ([chlogretry/retrier.go](../internal/sender/adapter/out/chlogretry/retrier.go))
   режет под-батчи на `max.message.bytes − 128 КиБ`: `clogwire.Split` меряет
   только JSON-конверт, а Kafka добавляет обвязку record-batch/ключ/заголовки —
   под-батч ровно на лимите иначе отвергается после фрейминга.

### 4.29 §52 — трёхсостоянье статуса узла: две РАЗНЫЕ кодировки одного исхода

Исход последнего вызова узла (`domain.NodeOutcome`, [node_outcome.go](../internal/domain/node_outcome.go))
хранится в двух сторах с **намеренно разными** кодировками — не унифицировать:

- **Prometheus gauge** `nexus_node_last_request_error`: 0=ok, **1=degraded, 2=down** — нужен
  порядок, чтобы `max by (node)` между репликами Sender выбирал худшее состояние; старые алерты
  `>=1` продолжают ловить любую проблему.
- **Redis** `nexus:node:last_error:<path>`: "0"=ok, **"1"=down, "2"=degraded** — legacy-значение
  `"1"` старого писателя («любой не-2xx») обязано толковаться worst case (down), и старый Web
  (`s == "1"`) при новом Sender обязан продолжать красить реальный down красным. Кодек только в
  [platform/nodestatus](../internal/platform/nodestatus/redis.go) (`EncodeOutcome`/`DecodeOutcome`);
  нераспознанное значение → пропуск узла → fallback на Prometheus.

Прочие неочевидности §52:

- **Первый импорт `platform/{metrics,nodestatus}` → `internal/domain`** — осознанно: NodeOutcome
  и классификатор `OutcomeFromStatusCode` — бизнес-правило (граница `<500` = upstreamHealthy §50.4),
  нужное и Sender'у (запись), и Web'у (разбор Prometheus-fallback). domain остаётся без зависимостей.
- **`SendOutput` не расширялся**: StatusCode детерминирует исход (0=транспорт→down; 503 breaker-open
  и 502 oversize — синтезированы шиной и тоже down: недоставка). Классификация — один вызов в двух
  точках записи (sync [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go),
  async [async.go](../internal/sender/usecase/async.go)).
- **`nexus_request_incomplete_total` и решение ack/DLQ живут от `IsError()`** («любой не-2xx») —
  семантика не менялась, §52 трогает только бейдж.
- **Легаси-толкование gauge**: значение 1 от старого Sender (булев «любой не-2xx») новый Web
  транзиентно читает как degraded — Redis приоритетен, обновится следующим вызовом узла.
- **DLQ-reprocess (§36) по-прежнему НЕ пишет статус узла** ([dlq_reprocess.go](../internal/sender/usecase/dlq_reprocess.go)) —
  существующий пробел, оставлен вне scope §52 (кандидат в follow-up).
- **Полный E2E через gRPC-сервер Sender не делался**: адаптер — тонкий маппинг, закрыт юнит-тестом
  с реальным SendUsecase ([sender_service_test.go](../internal/sender/adapter/in/grpc/sender_service_test.go) —
  первый тест этого адаптера); сценарий инцидента — integration
  [nodestatus_scenario_test.go](../tests/integration/nodestatus_scenario_test.go) (422→degraded,
  500→down, 200→ok через реальные AsyncProcessor+Redis).

### 4.30 §53 — копирование узла: почему не через `u.Create` и почему CH-таблица сохраняется

- **`Copy` не вызывает `Create` внутри**: у Create audit-действие `node.create` зашито в его
  собственный `uow.Execute`, а копии нужны `node.copy` (провенанс `source_node_id`) и клонирование
  ссылок allowlist-хостов **в той же транзакции**, что и INSERT узла. Вместо этого из Create
  извлечён общий пайплайн `prepareNewNode` ([node.go](../internal/web/usecase/node.go)) — дефолты →
  `NormalizeForRootMethod` → `normalizeCHTable` → `Validate` → self-reference → hard-limit →
  `provisionTable`; обе точки входа проходят один и тот же код, дрейф исключён.
- **`ClickHouseTable` копируется как есть, а не выводится из нового path**: несколько узлов на одну
  таблицу — легальная конфигурация (§37 добавил `node_id` именно для этого); `provisionTable`
  идемпотентен (`CREATE TABLE IF NOT EXISTS`), `normalizeCHTable` — no-op на полном имени
  `db.table`. Обнуление таблицы тихо выключило бы логирование копии; автодеривация из path — это
  UI-поведение формы, не usecase.
- **Креды через API не проходят**: `repo.Get` возвращает расшифрованные (carved-in правило —
  шифрование живёт только в pg-адаптере), клон несёт plaintext в памяти, INSERT перешифровывает.
  В `201`-ответе, как всегда, только флаги `*_set`.
- **Копия всегда `paused`**: копия enabled-узла не должна молча принимать трафик; для pull-узла это
  же откладывает конкуренцию двух узлов за одну RMQ-очередь (`rmq_queue` скопирован) до осознанного
  включения. Paused pull-узел Puller'ом не опрашивается.
- **uow=nil fallback (CLI/юнит-тесты) не клонирует host-ссылки** — Link+снимок вне транзакции могли
  бы разъехаться с созданием узла; пропуск виден на debug (§51.9). Production-wiring всегда с UoW.

### 4.32 Стартовая миграция CH-схемы: список таблиц — по ВСЕМ командам (боевой инцидент)

- **Симптом (Sentry 158619, прод 1.12.0):** открытие логов узла команды `vika` → 500,
  `clickhouse search: code: 47, Unknown expression identifier 'request_size' … FROM nexus_vika.dadata`.
- **Корень — не там, где ищется.** ALTER'ы (`EnsureNodeIDColumn` §37 / `EnsureHTTPMethodColumn` §39 /
  `EnsureBodySizeColumns`+`BackfillBodySizes` §42-доп) выполняются на старте Web и Sender — это верно.
  Дефект был в **списке таблиц**: Web брал его как `nodeRepo.List(ctx, ListNodesFilter{TeamID:
  defaultTeamID})`, то есть альтерил только `nexus_default.*`. У Sender'а фильтра нет
  (`nodepg.ListClickHouseTables` = `SELECT DISTINCT clickhouse_table FROM nodes`). На мультикомандной
  установке (весь боевой трафик живёт в `nexus_vika`) Web **не альтерил боевые таблицы никогда** —
  схему чинил только рестарт Sender'а. Окно отказа = «новый Web поднят, новый Sender ещё нет»; если у
  Sender `chMgr == nil` или его ALTER упал в `log+continue` — окно бесконечно.
- **Почему не замечали:** дефект родился в §37 и был скопирован в §39/§42.10 вместе с образцом блока.
  К моменту каждого следующего релиза Sender успевал доальтерить прошлые колонки, и симптом не
  всплывал — до §42.10, когда логи открыли раньше рестарта Sender'а.
- **Фикс (1.13.2):** `NodeRepoPg.ListClickHouseTables` — беcфильтровый, симметричный Sender'у; оба
  сервиса ходят по одному источнику, дрейф исключён. Полное имя `db.table` в `nodes.clickhouse_table`
  делает одно CH-соединение достаточным для любой БД команды.
- **Читатель не деградирует мягко и это осознанно:** `chUnavailable()` ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go))
  возвращает `false` для `*clickhouse.Exception` (code 47) → не `ErrLogsBackendUnavailable`, а 500 в
  Sentry. Значит **любая** будущая рассинхронизация схемы будет видна сразу, а не замаскирована.
  Список колонок `listCols`/`selectCols`/`previewCols` — хардкод (инвариант `domain.RequiredLogColumns`),
  интроспекции схемы нет by design.
- **Грабля для тестов:** `clickhouse_bodysize_backfill_test.go` передаёт таблицы литералом и потому
  дыру не ловил — покрыт был `ensure_schema.go`, а баг жил в `app.go`. Регрессия закрыта
  [node_ch_tables_migration_test.go](../tests/integration/node_ch_tables_migration_test.go): узлы в
  двух командах → в списке обе таблицы, а контрольный `List(TeamID)` видит только default.
- **Документация врала:** `DEPLOYMENT.md` §42.10 утверждал «порядок деплоя не важен, новый Web/Sender
  сами выполняют Ensure» — для не-default команд это было неверно; поправлено врезкой.

### 4.31 §54 — фильтры Overview: почему гибрид URL+зеркало, а не только URL

- **Только URL кейс пользователя не закрывает.** Кнопка «Узлы» в сайдбаре — `NavLink to="/"`
  ([Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)) без query-строки: она уводит на чистый `/`,
  и фильтр слетает, даже если он был в URL. Поэтому URL (истина, шаринг, «Назад», F5) дополнен
  зеркалом в web-storage, которое отрабатывает ровно этот переход. `AuditLog` (фикс П12) обходится
  чистым URL только потому, что на `/audit` из сайдбара ведёт ссылка без параметров и терять нечего.
- **`sessionStorage`, а не `localStorage` — из-за звёздочки §44.B.** Прецеденты в проекте
  расходятся (§44.B выбрал localStorage, §49 явно отверг его в пользу PG), и это не вкусовщина:
  живой период в localStorage **всегда перекрывал бы** пользовательский дефолт, и звёздочка
  «По умолчанию» перестала бы наблюдаться вообще. С sessionStorage новая вкладка стартует с
  дефолта — механизмы не конфликтуют. Второй довод: фильтр — состояние сиюминутной задачи;
  «прилипший» на неделю фильтр даёт непонятно пустой список при следующем заходе.
- **`saveFilters` строго ДО `setParams`** ([Overview.tsx](../web-ui/src/pages/Overview.tsx),
  `updateFilters`). Если зеркалить фильтры эффектом на `[params]`, то при ручной очистке
  restore-эффект в том же коммите увидит «URL уже пуст, зеркало ещё непусто» и воскресит только что
  очищенный фильтр. Замыкает логику то, что при всё-дефолт `saveFilters` делает `removeItem`:
  «очистил» ⇒ «восстанавливать нечего» ⇒ restore no-op. Проверено на стенде.
- **Дефолтный период не сериализуется** (`serializeFilters` сравнивает с `defaultFilters()`, где
  период = `loadDefaultPeriod()`). Отсюда следствие, которое выглядит как баг, но является ценой
  живой звёздочки: ссылка, отправленная с дефолтным периодом отправителя, откроется у получателя с
  **его** дефолтом. Чтобы зафиксировать период в ссылке, нужен не-дефолтный пресет.
- **Debounce поиска — не косметика.** Safari троттлит `history.replaceState` (~100 вызовов/30с,
  дальше `SecurityError`); запись на каждый keystroke в лимит упирается при быстром наборе. Ref
  `committed` нужен, чтобы sync-эффект (внешнее изменение `q` при restore/«Назад») не затирал ввод,
  набранный пользователем уже после коммита.
- **Мусор из URL не попадает в зеркало.** `loadFilters` прогоняет содержимое через
  `parseFilters` → `serializeFilters`, поэтому `?status=banana&range=99h` сохраняется как
  санитизированный набор (проверено на стенде: зеркало = `q=webhook&method=requestAsync`). Сам URL
  при этом не переписывается — `parse` мусор игнорирует, а следующая правка фильтра перезапишет
  адрес начисто.
- **`PeriodPicker` требовал засев И эффект, а не что-то одно** (§54, Phase 3.2). `useState`-засева
  мало: при клике «Узлы» на активной странице компонент **не ремоунтится** — период приезжает
  пропсом уже после монтирования (его возвращает restore-эффект). Эффект синхронизации завязан на
  строки (`toLocalInput(value.from)`), а не на объект `value`, иначе он срабатывал бы на каждый
  `parseFilters` (новый объект на каждый рендер) и затирал бы даты, набранные в полях.

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

   **`go test -race` на Windows.** Требует рабочего gcc (CGO). Если установлены
   и MSYS2 mingw64, и Git for Windows, последний прячет старые DLL
   (`libgcc_s_seh-1.dll`, `libgmp-10.dll`, ...) в `C:\Program Files\Git\mingw64\bin`,
   которые подгружаются раньше MSYS2-версий → `cc1.exe` падает с
   `STATUS_ENTRYPOINT_NOT_FOUND` без сообщения. Лечится приоритетом MSYS2 в PATH —
   Makefile делает это автоматически (`ifeq Windows_NT` + `export PATH := C:\msys64\mingw64\bin;$(PATH)`).
   Если запускаете `go test -race` вручную из PowerShell — сначала
   `$env:PATH = "C:\msys64\mingw64\bin;$env:PATH"`.

2. **swag init нужен go в PATH.** Если CI пытается генерировать docs/, убедитесь,
   что `swag` доступен (`go install github.com/swaggo/swag/cmd/swag@latest`).

3. **testcontainers требует Docker daemon.** На Windows — Docker Desktop запущен и
   расшарен с WSL2. На Linux/macOS — `systemctl start docker`.

4. **Проваленные CH-батчи буферизуются в Kafka (§38), не на диске.** При
   недоступности ClickHouse проваленный лог-батч продьюсится в топик
   `nexus.logs.retry` и дренится обратно отдельным consumer'ом
   (`<consumer_group>-clog-retry`) после восстановления — retry-in-place с
   бэкоффом. Под длительный простой CH рассчитывайте `kafka.topic.retention_ms`
   (объём логов × максимальный простой). Прежний локальный NDJSON-fallback
   (`logs/clickhouse-fallback/`, `clickhouse.fallback_dir`) удалён.
   **Retention всех топиков `nexus.*`:** дефолт `retention.ms=7 дней` +
   `retention.bytes=40 ГиБ`/партицию (`kafka.topic` в `config.yml`). Топики
   создаются через `CreateTopics`, **`AlterConfigs` в коде нет** — правка
   `config.yml` применяется лишь при создании; на живом топике меняйте через
   `kafka-configs --alter` (нагрузочный тест и рекомендации — TESTING.md,
   команда смены — DEPLOYMENT.md §4.3).

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

10. **LogReader.GetByID детерминирован через `ORDER BY date_request DESC LIMIT 1`** (Phase 9.1).
    ClickHouse не enforce'ит PRIMARY KEY uniqueness в MergeTree — два INSERT'а с одним ID
    создают две строки (например, file-fallback restore после восстановления CH, или
    повторный INSERT при batch-retry). UUID v4-коллизий нет, но дубликаты по бизнес-логике
    возможны. Поэтому `GetByID` возвращает самую свежую запись детерминированно.
    Integration-тест `TestClickHouse_GetByID_Deterministic`
    ([tests/integration/clickhouse_test.go](../tests/integration/clickhouse_test.go))
    проверяет контракт «два INSERT'а с одним ID → видим новейший».

11. **Phase AUD (аудит 2026-06): семантика остановки и ожиданий.**
    - `chlog.Writer.Stop` обязан **дренировать канал** `w.ch` после `wg.Wait()`:
      воркеры выходят по `stopCh` через select без приоритета веток, и оставшиеся
      в канале job'ы иначе молча теряются. Финальный flush идёт со **свежим**
      `context.WithTimeout(Background, 10s)` — ctx вызывающего к этому моменту
      может быть почти исчерпан (15s budget из sender/app.go). Тест:
      `TestWriter_Stop_DrainsPendingJobs` ([writer_test.go](../internal/sender/adapter/out/chlog/writer_test.go)).
      **fallback-горутина запускается через `fallbackStore.Start()`** — `wg.Add(1)`
      делается СИНХРОННО до старта горутины. Если `Add` внутри самой горутины (как было),
      он гонится с `Wait()` в `Stop()` — data race на `WaitGroup`. Грабли: первый же
      тест с включённым fallback (`t.TempDir()`) обнажил эту предсуществующую гонку,
      а `go test -short` без `-race` её не видел → CI-job `go-test` (с `-race`) упал
      после merge. Урок: после правок с конкурентностью прогонять `-race`.
    - Ожидание paused-узла в `AsyncProcessor.Handle` — `select(ctx.Done, time.After)`,
      не `time.Sleep`: иначе shutdown Sender'а висит до 30с на каждом paused-сообщении,
      а backlog из них полностью блокирует partition. Тест:
      `TestAsync_NodePaused_CtxCancelInterruptsWait`.
    - **Circuit breaker — single-probe через Lua** ([circuitbreaker/redis.go](../internal/platform/circuitbreaker/redis.go)):
      переход open→half_open и выдача пробного атомарны (`allowProbeScript`),
      в half_open проходит ровно один запрос (HINCRBY probe == 1), остальные
      отбрасываются до RecordSuccess/Failure. Раньше half_open пропускал ВЕСЬ
      трафик, а переход был TOCTOU-гонкой. Если результат пробного не записан
      (крэш процесса) — ключ самоочищается TTL 10 мин. Тесты:
      `TestCircuitBreaker_HalfOpen_SingleProbe`, `_ProbeFailureReopens`.
    - **Shutdown-гигиена фоновых горутин** (Phase AUD.3): все фоновые горутины
      App-уровня (reload-subscriber, housekeeping, kafka-lag reporter,
      notification scheduler) запускаются через `safego.Go` (возвращает
      done-канал) и ожидаются в `Stop()` через `safego.Await` с таймаутом 10s —
      **до** закрытия pg/redis/CH-соединений. Иначе callbacks reload'а могли
      бежать параллельно с закрытием пулов. goleak (`TestMain` +
      `goleak.VerifyTestMain`) включён в пакетах `chlog`, `sender/usecase`,
      `web/usecase`, `receiver/usecase` — регрессия утечки валит весь пакет.
    - **Web security (Phase AUD.4):**
      - анти-брутфорс `/api/auth/login` — `AuthUsecase.WithLoginRateLimit`
        ([auth.go](../internal/web/usecase/auth.go)): две независимые квоты
        (`login:ip:<ip>` и `login:user:<login>`) через общий Redis-лимитер,
        конфиг `web.login_rate_limit_per_min` (дефолт 10, -1 = выключить),
        429 + audit `user.login.failed{reason:rate_limited}`; fail-open при
        сбое Redis (§9.4);
      - CSRF Origin-check ([security_middleware.go](../internal/web/adapter/in/http/security_middleware.go))
        на группе `/api/*`: мутации с чужим/`null` Origin → 403; Bearer-токены
        и запросы без Origin (curl) пропускаются; reverse-proxy `/api/v1/*`
        не затрагивается. Дополнение к SameSite-cookie, не замена;
      - security-заголовки `SecurityHeaders()`: CSP (self + Google Fonts +
        'unsafe-inline' для style), nosniff, X-Frame-Options DENY,
        Referrer-Policy. CSP пропускается для `/swagger/*` (inline-скрипт
        конфигурации Swagger UI);
      - warning при старте, если `session_cookie_samesite=none` без `secure`.
    - **Phase AUD.5:**
      - `trusted_proxies` (receiver/web) — `gin.SetTrustedProxies`: X-Forwarded-For
        принимается только от перечисленных CIDR (дефолт loopback + приватные
        сети, см. `defaultTrustedProxies` в [config/defaults.go](../internal/platform/config/defaults.go)) —
        внешний клиент больше не подделывает IP в аудите/логах;
      - `SessionRepo.Touch` принимает `*domain.Session` и пересохраняет её
        целиком (один SET вместо EXPIRE) — `LastSeenAt` теперь реально
        обновляется на каждый запрос (раньше замораживался на логине);
      - креды узлов в Redis-кеше шифруются тем же AES-256-GCM
        ([nodecache/reader.go](../internal/receiver/adapter/out/nodecache/reader.go)):
        раньше расшифрованный Node маршалился в кеш целиком и plaintext-креды
        лежали в Redis открытыми. Старые plaintext-записи кеша не проходят
        Decrypt и трактуются как cache-miss (перечитываются из PG).
    - **Phase AUD.8 (мелочи):** fallback-файл, который не удалось удалить после
      успешного рестора (Windows-lock), переименовывается в `.done` — иначе
      следующий тик вставлял батч в CH повторно (дубликаты). Метрика
      `nexus_ratelimit_check_errors_total{scope}` (через `ratelimit.WithErrorSink`,
      реализуется `metrics.Metrics`) — единственный сигнал, что fail-open
      лимиты фактически отключены из-за лежащего Redis; алертить при росте.
      UI: единый `<ErrorAlert>` (components/ui), sticky-заголовки таблиц
      Users/ApiTokens, клиентская валидация формы узла
      ([lib/nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts) — зеркало
      `domain.Node.Validate` с теми же i18n-кодами; при изменении лимитов
      backend'а синхронизировать оба места).
    - **nodecache write-back** ([nodecache/reader.go](../internal/receiver/adapter/out/nodecache/reader.go)):
      горутина write-back обёрнута в `safego.Recover` и дедуплицируется по ключу
      (`inflight sync.Map`) — медленный Redis больше не порождает тысячи горутин
      при 500 rps. `isRedisUnavailable` стал реальным детектором (net.Error/
      ErrClosed/deadline) — warn «redis read failed» теперь действительно пишется
      для не-сетевых ошибок (раньше ветка была мёртвой: `err != nil` всегда true).

---

## 6.1 Карта пробелов тестового покрытия (аудит §51.8, 2026-07-15)

Снято `go test -short -cover ./internal/...` (unit-%). Правила чтения карты: низкий unit-% у
адаптеров сам по себе НЕ долг — многие из них покрыты integration-тестами (testcontainers) и
стендом; долгом считается пакет без покрытия и unit, и integration. Тесты в рамках §51 добиты
только по затронутой вертикали (решение пользователя); остальное — долг на будущие итерации.

**Долг (приоритетно, ни unit, ни integration):**

| Пакет | Unit | Чего не хватает | Приоритет |
|---|---|---|---|
| `platform/ratelimit` | 0% | unit на лимитер (окно/ключи/деградация при ошибке Redis — используется на боевом пути Receiver) | **высокий** |
| `web/adapter/in/http` | 21% | unit на непокрытые handler'ы (auth/node/user/token/team — сейчас тесты есть у logs/kafka/team/version/async_queue/app_settings/service_logs) | **высокий** |
| `platform/safego` | 22% | unit на `Go`/`Await` (покрыт только Recover) | средний |
| `platform/kafka` | 6% | unit на producer-опции/EnsureTopics парс-логику (сетевые пути — integration) | средний |
| `sender/adapter/in/kafka` | 14% | consumer-цикл покрыт integration; unit на headersToMap/retry-ветвления | средний |
| `web/adapter/out/receiver` | 0% | reverse-proxy клиент — unit на httptest | средний |
| `web/adapter/out/rabbitmq` | 0% | test-connection клиент — unit на диалер/ошибки | низкий |
| `platform/runner` | 0% | service-host (kardianos) — вручную/стендом; unit малополезен | низкий |
| `platform/logging` | 0% | тонкие re-export'ы вендора; смысла в unit мало | низкий |
| `platform/redis`, `platform/pg` | 0% | фабрики подключений; косвенно гоняются каждым integration | низкий |

**НЕ долг (0% unit, но есть integration/стенд):** `platform/circuitbreaker`
(`TestCircuitBreaker*`, test-int-catalog), `platform/nodestatus` (`TestNodeStatus_*`),
`platform/queuecancel` (`TestQueueCancel*`), `sender/adapter/out/nodepg` (sender-integration),
`receiver/adapter/out/rabbitmq` (`TestRMQPuller*`), `web/adapter/out/postgres` (0.3% unit — но
это самый плотно покрытый integration-слой: node/team/audit/app_settings/favorites),
`web/adapter/out/clickhouse`/`kafkaadmin`/`redis` (integration + §51), `internal/{receiver,sender,web}`
+ `cmd/*` (wiring — нетестируемо by design, проверяется стендом), `*/usecase/port` (интерфейсы),
`web/static` (embed).

**Закрыто в §51:** `platform/bootstrap` (было 0 тестов → logger/overlay/reload-применятель; Must*-хелперы
с `os.Exit` в unit не берутся), `platform/reloader` (22% unit + реальный Redis pub/sub в
integration), `app_settings_handler` (было 0), `platform/logsink`/`sensitive` (новые, 84–100%),
vitest во фронте (было 3 теста без CI-запуска → +2 файла §51 и гейт в ui-build).

---

## 7. Куда копать дальше (Phase 7+)

Если будете расширять — вот логичные следующие шаги, в порядке полезности:

1. **OpenTelemetry distributed tracing** (§16: явно out-of-scope v1, но даст
   корреляцию logs↔traces↔metrics при росте числа сервисов).
2. **Webhook signature verification** (`/v1/callback/`) — §16, для приёма
   входящих webhook'ов от партнёров (Stripe/GitHub/...).
3. **KMS/Vault** интеграция для `ENCRYPTION_KEY` — §16, чтобы убрать секрет
   из env. См. также `make rotate-encryption-key`.
4. **Multi-tenancy v2** — ✅ Phase 10 закрыта (7 блоков, 18 коммитов).
   - Блок A (foundation): миграция 0008 `teams` + `user_teams` + FK во
     всех team-aware таблицах, `domain.Team` + `TeamRepository`, резолв
     `defaultTeamID` в Web-bootstrap.
   - Блок B (session + scope): `Session.CurrentTeamID` в Redis,
     `/api/me/teams` + `/api/me/switch-team`, team-scope в Node CRUD
     и `APITokenUsecase.Create`.
   - Блок C (CH write): `TeamProvisioner` (PG-tx + CH `CREATE DATABASE`
     атомарно), `/api/teams` CRUD + Members, `NodeUsecase` нормализует
     `clickhouse_table` до `<team.ch_database>.<table>` на write-time
     (backfill-миграция не понадобилась — greenfield).
   - Блок D (CH read): scope в `LogsUsecase`/`ReplayUsecase`/`DryRunHandler`,
     `OrphanScanner` на allow-list `teams.ch_database`, `ch_housekeeping`
     работает multi-team автоматически.
   - Блок E (Receiver URL): `NodeReader.Get(teamSlug, path)` через PG
     JOIN, Redis-ключ `node:<team_slug>:<path>`, L2-кеш по
     `<team_slug>/<path>`, URL `/v1/request/<team_slug>/<node_path>`
     (legacy без слога продолжает работать для default-team).
   - Блок F (UI + audit scope): `user_audit.team_id` через `Actor.TeamID`,
     SPA `Settings → Teams` + Topbar team-switcher с
     `qc.invalidateQueries()`.
   - Блок G (regression): integration-тест
     `TestMultiTenancy_Isolation_E2E` (10 свойств, testcontainers PG).

Сделанное в Phase 7.14:

- 7.14 Дополнение unit-test покрытия по 8 ранее непокрытым пакетам / файлам
  (~80 новых тестов). После Phase 7.11–7.13 без покрытия оставались
  middleware-слои, маскирование Sentry, парсинг Accept-Language, домен-методы
  и часть web/usecase. Закрыто это всё.
  · **`internal/domain`** ([enums_test.go](../internal/domain/enums_test.go),
  [api_token_test.go](../internal/domain/api_token_test.go)) — 39 sub-тестов:
  `Valid()` для всех 9 enum-типов (NodeStatus / RootMethod / URLMode /
  AuthType / AuthDynSource / IncomingAuthType / UserRole / UserLang) с
  case-sensitive проверкой и unknown-значениями; `APIToken.IsActive` /
  `HasScope` (revoked beats expiry, empty scopes reject, case-sensitive,
  scope-constants distinct).
  · **`internal/platform/sentry/sentry.go`** ([sentry_test.go](../internal/platform/sentry/sentry_test.go))
  — 12 тестов: `isSensitive` с 30+ кейсами (точные совпадения, case-insensitive
  через ToLower, substring `user_password`/`refresh_token`/`my_api_key`, не-секретные
  `username`/`email`/`x-request-id`); `maskMap` / `maskAny` (mutation +
  nil/empty); `beforeSend` маскирует request.Headers, очищает Cookies/Data/
  QueryString, маскирует Tags и breadcrumb.Data; `beforeBreadcrumb` без
  request.Headers; `Init`/`Reload` no-op при `Use=false`; `sensitiveKeys`
  без uppercase и без пустых.
  · **`internal/platform/i18n/middleware.go`** ([middleware_test.go](../internal/platform/i18n/middleware_test.go))
  — 6 тестов: `GinMiddleware` парсит Accept-Language (ru / ru-RU / en /
  en-US / unknown→default / empty→default) и кладёт значение в gin-context
  и request-context; `FromGin` приоритет gin.Set над request-ctx, fallback на
  ctx при отсутствии gin-значения, на DefaultLang при пустоте, при non-string
  значении в gin.Set.
  · **`internal/platform/metrics/gin.go`** ([gin_test.go](../internal/platform/metrics/gin_test.go))
  — 7 тестов: `rootMethodFromPath` table-driven (V1/non-V1/empty);
  `nodePathFromGin` table-driven (slash-strip/nested/empty); end-to-end
  GinMiddleware: `/v1/request/*path` → `method="request"` + `node="demo/sub"`,
  `/v1/requestAsync/*path` → `method="requestAsync"`, API routes → `method="GET /api/nodes/:id"`
  fallback и `node=""`, `/health`/`/ready`/`/metrics` не учитываются,
  404 (FullPath="") не пишет counter, статус берётся из `c.Writer.Status()`
  после Abort.
  · **`internal/receiver/adapter/in/http/middleware.go`** ([middleware_test.go](../internal/receiver/adapter/in/http/middleware_test.go))
  — 6 тестов: `RateLimitMiddleware` happy-path (handler вызван, key=nodePath
  + limit прокинуты в limiter); denied (429, handler не вызван); fail-open
  при Redis-ошибке (200, handler вызван — §9.4 ТЗ); `limitPerMin=0` no-op
  (limiter не вызывается); empty nodePath (без `:path`-параметра) skip;
  async-path стрипует prefix `/v1/requestAsync/` корректно.
  · **Рефакторинг для тестируемости.** `RateLimitMiddleware` принимал
  конкретный `*ratelimit.Limiter` — заменён на consumer-side interface
  `rateAllower` (§17.4 ТЗ, CLAUDE.md §3 «interface on consumer side»).
  Удовлетворяется *Limiter автоматически, production wiring без изменений.
  Аналогично в Web: `AuthMiddleware` принимал `*usecase.AuthUsecase` —
  заменён на `sessionChecker`; `APITokenAuthMiddleware` — два interface'а
  `apiTokenVerifier` и `tokenRateAllower`. Это **не** меняет publik API
  middleware'ов — только сигнатуры конструкторов.
  · **`internal/web/adapter/in/http`** ([auth_middleware_test.go](../internal/web/adapter/in/http/auth_middleware_test.go),
  [api_token_middleware_test.go](../internal/web/adapter/in/http/api_token_middleware_test.go),
  [errors_test.go](../internal/web/adapter/in/http/errors_test.go)) — 23
  теста: `AuthMiddleware` no-cookie/401, session-expired/401,
  redis-down/503, valid/passes-with-session, already-set (api-token путь)
  skip; `RequireRole` admin/admin ok, viewer/admin 403, viewer/viewer ok,
  no-session 403; `sessionFromCtx` (present / absent / wrong-type);
  `APITokenAuthMiddleware` no-Bearer skip, JWT-prefix skip (не наш),
  invalid/401, backend-error/500, valid сохраняет session+token в ctx,
  rate-limit 429 + Retry-After, rate-limit error путь;
  `RequireSessionOnly` (session ok, api-token 403); `RequireScope` (no-token
  pass, scope present, scope absent 403, nil-token 403);
  `localizedError` (default lang, EN vs RU translation differ, unknown-key
  fallback к самому ключу).
  · **`internal/web/usecase/auth.go`** ([auth_test.go](../internal/web/usecase/auth_test.go))
  — 14 тестов: `Login` (unknown user → ErrUnauthorized + audit reason=`not_found`;
  inactive → ErrUserInactive + audit reason=`inactive`; password
  hash="" → ErrUnauthorized; bad password → ErrUnauthorized + audit
  reason=`bad_password`; happy-path сохраняет сессию с token/userID/
  role/lang, обновляет last_login, пишет audit; get-user error
  оборачивается с `get user:` wrap; session create error оборачивается
  с `create session:`); `Logout` (удаление по token); `Check` (touch +
  return; not-found → ErrSessionNotFound); `Me`; `ChangePassword`
  (короткий пароль → error; happy с MustChange=true, purge сессий
  только владельца, audit; UpdatePassword error пробрасывается).
  Mock-репозитории `authUserRepo` / `memSessionRepo` — гибкие
  in-memory implementations, переиспользуются user_test.go.
  · **`internal/web/usecase/user.go`** ([user_test.go](../internal/web/usecase/user_test.go))
  — 12 тестов: `Get`/`List` делегирование; `Create` (invalid role → error,
  password<8 → error, пустой lang → дефолт UserLangEN, happy hash'ит
  пароль + audit, repo.Create error не пишет audit); `Update` (last-admin
  demote rejected, last-admin disable rejected, role/active change →
  purge sessions + audit, no-change → no purge); `Delete` (self →
  error, last admin → error, happy purge sessions + audit с
  details.login=old.Login, not-found пробрасывает ErrUserNotFound).
  · **`internal/sender/usecase/ch_housekeeping.go`** ([ch_housekeeping_test.go](../internal/sender/usecase/ch_housekeeping_test.go))
  — 10 тестов: `isSensitive` table-driven (digits/alnum/hyphen ok;
  semicolon/quote/space/slash/cyrillic/dot rejected; границы 0/64/65
  длины); `splitDBTable` (happy/no-dot/empty/leading/trailing/двух
  точек — берётся первая); `runOnce` (list-error → wrap `list nodes:`;
  empty nodes → no error; nodes без table/retention=0/-1 → skip без
  обращения к Conn; node с битым table-name → ошибка drop'а
  логируется, цикл не падает); `dropPartitionsOlderThan` (invalid
  table → error; nil-conn → error «conn is nil»); конструктор:
  default period 24h; `Run` cancel ctx → graceful stop в течение 2s,
  один прогон до cancel был выполнен.
  · **`internal/web/adapter/in/http` теперь покрыт.** Раньше там был только
  middleware Sentry-tracing — теперь все три middleware (auth, api-token,
  rate-limit в receiver) + errors.localizedError.
  · Покрытие пакетов: 14 → **16** + middleware-слой полностью.
  Все тесты `go test -short` зелёные на Windows; OOM-линкер на `-race`
  по-прежнему присутствует (CLAUDE.md «грабли» #1) — в CI/Linux race
  проходит штатно.

Сделанное в Phase 9.3 (Расширение integration-тестов):

- 9.3 Четыре новых сценария поверх существующего testcontainers-стэка
  (PG + Redis + CH + Kafka). Цель — покрыть критические бизнес-сценарии,
  которые проходят через несколько adapter'ов и которые легко регрессировать
  одной правкой в usecase. До 9.3 было 11 integration-тестов; после — 15.
  · [tests/integration/sender_dlq_test.go](../tests/integration/sender_dlq_test.go)
  `TestSender_Async_DLQ_E2E` — узел с `retry_count=2` + mock=500. Sender
  делает 3 попытки, после исчерпания публикует исходный envelope в
  `nexus.async.dlq`. Отдельный `kafka-go` reader на DLQ-топике (separate
  consumer-group `nexus-dlq-watcher-it`, чтобы не конкурировать с
  Sender'ом) дожидается сообщения и проверяет headers: `id` соответствует
  envelope id, `node_path`, `orig_topic="nexus.async"`, `reason` содержит
  `status=500 attempts=3`, `last_attempt_at` непуст; value полностью
  соответствует исходному envelope. В capturing log-writer 1 запись со
  Status=500, Done=false, Attempts=3 (а не 3 отдельные записи — `SendUsecase`
  пишет ОДИН лог с aggregated `attempts_details`).
  · [tests/integration/replay_test.go](../tests/integration/replay_test.go)
  `TestReplay_E2E_ClickHouse` — узел в PG + «оригинальный» log в CH
  (`Done=false, Status=500`). `ReplayUsecase` через capturing-dispatcher
  (fake `port.ReceiverDispatcher`, чтобы не поднимать Receiver-HTTP-сервер
  — шов уже покрыт другими тестами) проверяет: маркер `__replay_of=<orig_id>`
  в query, оригинальные query-параметры (`x=1&y=2`) сохранены, body совпадает
  с оригиналом из CH, audit-запись `node.replay` появляется в PG c TargetID=
  оригинальный log_id (НЕ node.id) и UserID актёра.
  · [tests/integration/auth_test.go](../tests/integration/auth_test.go)
  `TestAuth_Login_E2E` — PG `UserRepoPg` + Redis `SessionRepoRedis`.
  Пять сценариев в одном тесте (чтобы не платить за двойной testcontainer-
  bootstrap): wrong password → `ErrUnauthorized` + `user.login.failed`;
  correct → token + `user.login.success`; `Check` валидирует и продлевает
  TTL; `ChangePassword` инвалидирует ВСЕ сессии пользователя (forced
  re-login §7.1) — обе предыдущие сессии возвращают `ErrSessionNotFound`,
  старым паролем больше не зайти, новым — да; `Active=false` → `ErrUserInactive`.
  Audit-журнал проверяется counters по action и `Details["reason"]="inactive"`
  для inactive-попытки.
  · [tests/integration/receiver_incoming_auth_test.go](../tests/integration/receiver_incoming_auth_test.go)
  `TestReceiver_IncomingAuth_E2E` — Postgres + полный Receiver HTTP-стек:
  `gin.New()` + `Handler.Register` + `httptest.NewServer` + реальный
  `http.Client`. Три узла с `incoming_auth_type` = none / basic / token,
  upstream — `httptest.Server` с counter. Покрываемые матрицы: basic
  без заголовка / wrong scheme / wrong creds / correct; token без заголовка
  / wrong scheme / wrong token / correct. Финальная проверка `upstreamHits=3`
  гарантирует, что 401-запросы не доходят до upstream — короткое замыкание
  на стороне Receiver, а не на стороне Sender.
- Helpers `startPostgres` / `startKafka` / `startRedis` / `startClickHouse`
  и `createNodeLogTable` переиспользуются из существующих тестов; новые
  тесты не добавили ни одного testcontainer-helper'а. Helper'ы остаются
  в файлах, где появились первыми (`node_repo_test.go`, `receiver_async_test.go`,
  `redis_test.go`, `clickhouse_test.go`); все остальные тесты импортят
  их через build-tag `integration` и общий package `integration`.

Сделанное в Phase 9.2 (GitLab CI):

- 9.2 GitLab CI pipeline ([.gitlab-ci.yml](../.gitlab-ci.yml)) — аналог
  трёх GitHub workflows (ci/security/release) в одном файле для self-hosted
  GitLab. Все jobs на ноде `srv-d-android-l` через `default: tags`.
  · 9.2a CI stages: `test` (go vet + go test -race -short), `lint`
  (golangci-lint v2.12 + swagger-drift), `build` (go build ./... + Vite SPA
  с artifact'ом web-ui/dist), `integration` (testcontainers с pre-pull
  docker-образов; запуск на master/dev/tag или MR с label `run-integration`).
  · 9.2b Security stage: 3 jobs (govulncheck — единственный gate'ующий с
  call-graph анализом; gosec/trivy — allow_failure=true, alerts only).
  Nancy убран (Sonatype OSS Index возвращает 403 для анонимов; функционально
  дублирует govulncheck — оба ходят в NVD).
  Триггеры через anchor `.security-rules`: push в master/dev, MR при
  изменении deps-файлов, schedule, manual. SARIF artifacts через
  `artifacts.paths` (Premium-фичи Security Dashboard в CE недоступны).
  · 9.2c Release stage: GoReleaser → GitLab Container Registry
  (`$CI_REGISTRY_IMAGE/{receiver,sender,web}`), multi-arch amd64+arm64
  через docker:dind + buildx + tonistiigi/binfmt. Триггер `$CI_COMMIT_TAG =~ /^v[0-9]/`.
  Required CI/CD Variables: `GITLAB_TOKEN` (Project Access Token со
  scope api+write_repository — для release entry).
  · 9.2d Renovate Bot ([renovate.json](../renovate.json)) — self-hosted
  замена Dependabot. 4 ecosystems (gomod, npm в web-ui, dockerfile/compose,
  gitlab-ci/github-actions). Группировка minor+patch в один MR; major —
  отдельный с label major-update; vulnerability alerts создаются немедленно
  (не ждут weekly). Renovate job в pipeline запускается по schedule/web/manual.
  · 9.2e Manual loadtest job (`loadtest:` в [.gitlab-ci.yml](../.gitlab-ci.yml))
  — поднимает изолированный compose-стек (`COMPOSE_PROJECT_NAME=loadtest-<pipe>-<job>`,
  уникальные сети/volumes), bootstrap-задаёт пароль admin'у через
  `web --set-admin-password`, прогоняет `cmd/loadtest` как сервис из
  [deploy/docker/loadtest.Dockerfile](../deploy/docker/loadtest.Dockerfile)
  под compose-профилем `loadtest` (depends_on: receiver/sender/web healthy),
  снимает стек, выкладывает `loadtest-report/{report.json,compose-logs.txt}`
  как artifacts. Mock внешних узлов слушает `0.0.0.0:9999` внутри docker-сети
  (новые флаги `--mock-bind` и `--mock-public-url` в [cmd/loadtest/main.go](../cmd/loadtest/main.go)),
  Receiver/Sender ходят к нему как `http://loadtest:9999`. Required CI variable:
  `LOADTEST_ADMIN_PASSWORD` (masked+protected); опциональные: `LOADTEST_TARGET_RPS`
  (def. 500), `LOADTEST_DURATION` (def. 5m), `LOADTEST_NODES` (def. 50) —
  переопределяются через UI «Run pipeline → Variables».
  · **Грабли team-slug (Phase 10.E.1) → 100% error rate.** Узлы создаются с
  `path=loadtest/node-…` (со слэшем) в команде `default`. Receiver-URL
  `/v1/request/{team_slug}/{node_path}`: `splitTeamSlugAndPath`
  ([receiver handler.go](../internal/receiver/adapter/in/http/handler.go))
  всегда трактует первый сегмент как team_slug. Поэтому legacy-URL без слога
  `/v1/request/loadtest/node-X` парсится как `team=loadtest, path=node-X` →
  узел не найден → **404 на каждом запросе** (error rate 100%, p50≈1.6мс).
  Фикс: `cmd/loadtest` адресует узлы с явным слогом
  (`/v1/request/<team_slug>/<path>`, флаг `--team-slug`, def. `default`).
  Это ортогонально `--use-aliases` в [.gitlab-ci.yml](../.gitlab-ci.yml): тот
  тоже **необходим** (без него `docker compose run` не даёт one-off-контейнеру
  network-alias `loadtest`, и Sender не дозвонится до mock'а — это даёт 502,
  а не 404). Оба нужны одновременно. **Диагностический блок в loadtest job
  запускается ПОСЛЕ завершения `run` (mock жив только во время самого прогона,
  т.к. это процесс loadtest-бинаря) — связность с mock'ом им проверить нельзя,
  502 там ОЖИДАЕМО.** Блок переработан: проверяет именно МАРШРУТИЗАЦИЮ Receiver'а
  (probe корректного URL `/v1/request/default/<path>` ждёт НЕ 404; legacy-URL
  без слога показывает 404 как регресс-сигнатуру team_slug-парсинга), а логи
  самого run-контейнера снимаются в `loadtest-report/loadtest-container.txt`
  ДО `docker rm`. In-test ошибки ищи в `report.json`/`loadtest-container.txt`/
  `compose-logs.txt`, а не по коду пост-прогонного probe.
- 9.2 `.goreleaser.yaml` под GitLab CI:
  · Реестр через `{{ .Env.DOCKER_REGISTRY_BASE }}` — задаётся `$CI_REGISTRY_IMAGE`
  в [.gitlab-ci.yml](../.gitlab-ci.yml) release job (30 строк в `dockers:` /
  `docker_manifests:`).
  · Footer compare-URL через `{{ .Env.COMPARE_URL_BASE }}` —
  `$CI_PROJECT_URL/-/compare`.
  · `release.gitlab:` блок не указан — GoReleaser автодетектит платформу
  по `GITLAB_TOKEN`.

Сделанное в Phase 9.1:

- 9.1 Дотягивание v1: детерминированный `LogReader.GetByID`.
  · [internal/web/adapter/out/clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)
  `GetByID`: `SELECT ... WHERE ID = ? LIMIT 1` → `... ORDER BY date_request DESC LIMIT 1`.
  Причина: ClickHouse MergeTree не enforce'ит PRIMARY KEY uniqueness, два
  INSERT'а с одним ID создают две строки (file-fallback restore после
  восстановления CH, повторный INSERT при batch-retry, дубликат в NDJSON
  при крэше до удаления файла). До этого `LIMIT 1` без ORDER BY выдавал
  произвольную строку из двух — UI «открыл по id, вижу старый ответ»
  выглядел как баг рейс-кондишн.
  · Integration-тест `TestClickHouse_GetByID_Deterministic`
  ([tests/integration/clickhouse_test.go](../tests/integration/clickhouse_test.go))
  пишет два INSERT'а одного ID с разными `date_request`, ждёт `count()=2`,
  затем `GetByID` — проверяет что вернулся самый свежий.

Сделанное в Phase 6:

- 6.1 Prometheus метрики (`nexus_requests_total`, latency, kafka_lag, CH-метрики).
- 6.2 Async end-to-end integration через Kafka.
- 6.3 app_settings + hot-reload Sentry/ClickHouse + test connection.
- 6.4 Settings → Users полный CRUD.
- 6.5 Live-tail UI улучшения (подсветка, авто-прокрутка, баннер).
- 6.6 CSV-экспорт audit log.
- 6.7 ClickHouse orphan-tables (сканер + DROP с подтверждением).
- 6.8 Расширенные фильтры live-tail (period/IP/Host/full-text).
- 6.9 Audit log: diff-двухколоночный для `node.update`.

Сделанное в Phase 8.4:

- 8.4 Kafka-headers OTel propagation для async-пути. До 8.4 async-сообщения
  теряли trace-id на границе publish/consume — Sender-consumer стартовал
  свой root-span, никак не связанный с входящим /v1/requestAsync. Теперь
  trace пробрасывается через Kafka headers (W3C traceparent + baggage).
  · [internal/platform/otel/kafka.go](../internal/platform/otel/kafka.go):
  `stringMapCarrier` — `TextMapCarrier` поверх `map[string]string` (наш
  `kafka.Producer.Produce` принимает headers как map; внутри сам конвертит
  в `[]kafka.Header`). Helper'ы `InjectKafkaHeaders(ctx, headers)`,
  `ExtractKafkaHeaders(ctx, headers) → ctx`, `StartKafkaProducerSpan(ctx,
  topic) → (ctx, finish)` (semconv `messaging.system=kafka` +
  `messaging.destination.name` + `messaging.operation.type=publish`),
  аналогичный `StartKafkaConsumerSpan` с `operation.type=process`.
  · Producer-side: [route_async.go](../internal/receiver/usecase/route_async.go)
  перед `producer.Produce` открывает producer-span и инжектит traceparent
  в headers (рядом с уже существующими `id` / `node_path` / `attempt`).
  При выключенном tracing — оба helper'а no-op.
  · Consumer-side: [async.go](../internal/sender/usecase/async.go)
  `AsyncProcessor.Handle` принял новый параметр `msgHeaders map[string]string`,
  делает `ExtractKafkaHeaders(ctx, msgHeaders)` + `StartKafkaConsumerSpan` —
  envelope-обработка теперь дочерний span к producer-span'у Receiver'а.
  · [adapter/in/kafka/consumer.go](../internal/sender/adapter/in/kafka/consumer.go)
  собирает `map[string]string` из `msg.Headers` (берёт первое значение на
  ключ, исключая ситуацию когда Kafka даёт несколько значений — для
  propagator-keys это не релевантно) и передаёт в `Handle`.
  · Существующие async-unit-тесты ([async_test.go](../internal/sender/usecase/async_test.go))
  обновлены под новую сигнатуру (`nil` для headers — extract пустых
  безопасен, тест-cases без изменения).
  · 7 unit-тестов [kafka_test.go](../internal/platform/otel/kafka_test.go):
  stringMapCarrier Get/Set/Keys + видимость через ref'ом, Inject c nil-map
  и пустой map (no-panic), Extract пустых = тот же ctx, Extract с keys не
  паникует, StartKafkaProducerSpan finish с/без err, end-to-end roundtrip
  Inject→Extract→StartConsumerSpan.
  · Это завершает OTel-историю: span'ы соединены через все 4 транспорта
  шины — HTTP server-side (Phase 8.2), HTTP outbound (8.3a), gRPC unary
  (8.3b), Kafka publish/consume (8.4).

Сделанное в Phase 8.3:

- 8.3 OpenTelemetry end-to-end propagation (продолжение Phase 8.2). После
  8.2 span'ы жили только в одном сервисе; теперь trace связывается через
  всю шину: UI → Web → Receiver → gRPC → Sender → внешний URL.
  · 8.3a HTTP outbound + traceparent injection
  ([http_client.go](../internal/platform/otel/http_client.go)):
  · `InjectHTTPHeaders(ctx, h http.Header)` — простая обёртка над
  `otel.GetTextMapPropagator().Inject(...)` через `propagation.HeaderCarrier`;
  при no-op propagator'е ничего не вставляет.
  · `StartHTTPClientSpan(ctx, method, url) → (ctx, finish)` — открывает
  client-span с semconv-атрибутами `http.request.method` / `url.full`
  (через `sanitizeURL` query вырезается, чтобы PII / токены не утекали в
  бэкенд трейсинга). finish() пишет `http.response.status_code` и
  `codes.Error` на 5xx или err != nil.
  · Подключено в двух outbound-местах:
  [httpclient.Client.Do](../internal/sender/adapter/out/httpclient/client.go)
  (Sender → внешний узел) и
  [HTTPDispatcher.Dispatch](../internal/web/adapter/out/receiver/dispatcher.go)
  (Web → Receiver для replay). Оба теперь делают: открыть client-span →
  собрать запрос → положить traceparent в header → выполнить → закрыть
  span со статусом/ошибкой.
  · 5 unit-тестов [http_client_test.go](../internal/platform/otel/http_client_test.go):
  Inject без TP не паникует; finish с err и со status>=500 не паникуют;
  sanitizeURL table-driven (с query / без / только query / пустой).
  · 8.3b gRPC unary interceptor'ы
  ([grpc.go](../internal/platform/otel/grpc.go)):
  · `metadataCarrier` — `propagation.TextMapCarrier` поверх
  `grpc/metadata.MD`. gRPC lowercase'ит ключи, метод Set через `md.Set(k, v)`
  работает совместимо.
  · `UnaryClientInterceptor()` — открывает client-span с
  `rpc.system=grpc`/`rpc.method`, копирует существующий outgoing MD
  (`md.Copy()`), инжектит propagator-keys, передаёт в invoker; при ошибке
  записывает `rpc.grpc.status_code` (если err — это grpc/status.Status) и
  `codes.Error`.
  · `UnaryServerInterceptor()` — экстракт propagator-keys из incoming MD,
  затем server-span; те же атрибуты и обработка ошибок.
  · Подключено в Receiver
  [grpcsender/client.go](../internal/receiver/adapter/out/grpcsender/client.go)
  через `grpc.WithUnaryInterceptor(otelpf.UnaryClientInterceptor())` и в
  Sender [app.go](../internal/sender/app.go) через
  `grpc.UnaryInterceptor(otelpf.UnaryServerInterceptor())` при создании
  `grpc.NewServer`.
  · 8 unit-тестов [grpc_test.go](../internal/platform/otel/grpc_test.go):
  metadataCarrier Get/Set/Keys, server-interceptor happy-path / handler
  error / grpc-status error / без incoming metadata, client-interceptor
  invoker happy-path / caller MD не затирается / invoker error пропагается.
  E2E через bufconn убран — Invoke без registered service возвращает
  UNIMPLEMENTED и invoker не дозванивается до handler'а, тест становился
  нестабильным; чистый unit с mock-invoker'ом надёжнее.
  · Зависимости — НЕ менялись (всё через уже подключённые в 8.2
  `go.opentelemetry.io/otel`, `propagation`, `semconv v1.27.0`,
  `google.golang.org/grpc`).
  · Не сделано (отдельный блок): Kafka-headers propagation для async-пути
  (Receiver → Kafka → Sender). Сейчас async теряет trace-id на границе
  publish/consume. Нужно: при publish писать traceparent в Kafka
  message.Headers; при consume — Extract в обработчике перед обработкой
  envelope.

Сделанное в Phase 8.2:

- 8.2 OpenTelemetry distributed tracing (§16 ТЗ). Минимальная имплементация
  без external `opentelemetry-go-contrib`, чтобы не валить Windows-линкер
  (CLAUDE.md грабли #1):
  · Новые direct-deps: `go.opentelemetry.io/otel/sdk` и
  `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`.
  Транзитивно `otel` обновился с v1.41.0 до v1.43.0.
  · Пакет [internal/platform/otel](../internal/platform/otel/):
  · [otel.go](../internal/platform/otel/otel.go) `Init(ctx, cfg, serviceName, logger)`
  — единая точка bootstrap'а: при `cfg.Otel.Enable=false` (или `cfg=nil`)
  возвращает noop-shutdown и nil-error без побочных эффектов; при включённом —
  собирает OTLP/HTTP exporter (`otlptracehttp.NewClient`), resource с
  `service.name`/`deployment.environment` (semconv v1.27), head-based
  `ParentBased(TraceIDRatioBased(SampleRate))` sampler с дефолтом 0.1.
  Устанавливает global TracerProvider + TextMapPropagator
  (TraceContext+Baggage). Возвращает shutdown с 5-секундным timeout'ом.
  Init НЕ делает network round-trip — exporter лениво коннектится при первом
  батче, поэтому временно недоступный collector не блокирует старт сервиса.
  · [gin.go](../internal/platform/otel/gin.go) `GinMiddleware(serviceName)`
  — handcrafted, не otelgin: пропуск exhaust большого графа `contrib`-deps;
  делает ровно то, что нужно — extract traceparent из header'а, открывает
  server-span с http.method/url.path/http.route/user_agent/http.client_ip,
  пишет http.response.status_code в конце и codes.Error на 5xx; .End()
  в defer'е переживает panic. При `Enable=false` global TracerProvider —
  no-op, middleware превращается в пару дешёвых allocation'ов без сети.
  · 5 unit-тестов [otel_test.go](../internal/platform/otel/otel_test.go):
  Init c Enable=false → noop-shutdown; Init с nil-cfg (defence-in-depth) →
  noop-shutdown; GinMiddleware пропускает запрос без TracerProvider'а;
  middleware пробрасывает обновлённый ctx в handler; Init с заведомо битым
  endpoint не падает (правильное поведение — exporter ленивый), shutdown
  без паники.
  · Конфиг: добавил `OtelSection` в [platform/config/config.go](../internal/platform/config/config.go)
  (Enable, OtlpEndpoint, OtlpInsecure, SampleRate, ServiceName, Environment)
  и [config.example.yml](../config/config.example.yml) — с env-overrides
  `OTEL_ENABLE`, `OTEL_OTLP_ENDPOINT`, `OTEL_OTLP_INSECURE`,
  `OTEL_SAMPLE_RATE`, `OTEL_SERVICE_NAME`, `OTEL_ENVIRONMENT`.
  · Bootstrap [bootstrap.go](../internal/platform/bootstrap/bootstrap.go)
  `MustOtel(ctx, cfg, projectName, logger) otelpf.ShutdownFunc` — единая
  точка инициализации, при ошибке Init'а пишет warning и возвращает noop
  (не валит сервис из-за «opcollect недоступен»).
  · Подключение в трёх сервисах: `New(...)` каждого App принимает
  `otelpf.ShutdownFunc` параметром (Constructor Injection, как и остальные
  cross-cutting deps); `App.Stop` вызывает shutdown с 5-секундным таймаутом
  и логирует ошибки. Gin-router инициализируется с
  `otelpf.GinMiddleware("<service>")` ПЕРЕД sentry/metrics — span охватывает
  весь pipeline. `cmd/{receiver,sender,web}/main.go` вызывают
  `bootstrap.MustOtel` и пробрасывают shutdown в App-конструктор.
  · Sentry tracing (§14.3, Phase 5) НЕ заменяется: Sentry оставлен для
  error-correlation, OTel — для distributed tracing. Параллельная работа
  поддерживается, оба middleware установлены на router.

Сделанное в Phase 8.1b (frontend):

- 8.1b SPA UI для webhook_signature: в форме узла
  ([pages/NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx))
  расширил secret-блок:
  · `incoming_auth_type` теперь принимает `webhook_signature` (4-й
  option).
  · При выборе `webhook_signature` лейбл поля `incoming_auth_credentials`
  меняется на `webhook_secret` (UX — пользователь не должен путать
  webhook-секрет с api-токеном).
  · Появляются два технических поля: `webhook_signature_header` (default
  "X-Hub-Signature-256") и `webhook_signature_prefix` (default "sha256=")
  — оба моноширинные, чтобы было видно регистр (`X-Hub-...` vs `x-hub-...`).
  · Хелп-блок снизу: «Receive webhooks at POST /v1/callback/{path}. The
  signature is HMAC-SHA256 of the raw request body using the secret
  above, hex-encoded, optionally prefixed (e.g. "sha256=") and placed in
  the header.» Переведён в [locales/en.json](../web-ui/src/locales/en.json)
  и [ru.json](../web-ui/src/locales/ru.json) под ключом `node.webhook.hint`.
  · SPA пересобран (`make build-ui`), bundle обновлён в
  `internal/web/static/`. Старые `index-*.css/.js` удалены (Vite использует
  content-hash в именах, Makefile-`cp` не удаляет устаревшие).

Сделанное в Phase 8.1 (backend):

- 8.1 Webhook callback endpoint с HMAC-SHA256 подписью (§16 ТЗ —
  «Webhook signature verification`/v1/callback/`»). Архитектурное
  решение: НЕ создаём отдельный путь маршрутизации, а добавляем новый
  `IncomingAuthType = "webhook_signature"`. Webhook secret хранится в
  существующей колонке `incoming_auth_credentials` (уже шифруется
  AES-256-GCM, шифрование/расшифровка — единственное место в
  `adapter/out/postgres`). Endpoint `/v1/callback/{path}` — alias
  `/v1/requestAsync/{path}` с дополнительной проверкой
  `IncomingAuthType=webhook_signature` (иначе 400 `ErrCallbackNotAllowed`).
  · Миграция [0007_webhook_signature.up.sql](../migrations/0007_webhook_signature.up.sql):
  добавляет колонки `webhook_signature_header` (VARCHAR(128), default '')
  и `webhook_signature_prefix` (VARCHAR(64), default 'sha256='), расширяет
  CHECK `nodes_inc_auth_check` (drop+add — Postgres не умеет ALTER CONSTRAINT)
  до `(none, basic, token, webhook_signature)`, добавляет CHECK
  `nodes_webhook_sig_header_required`: header не может быть пустым для
  webhook_signature.
  · Domain: новый enum-value `IncomingAuthTypeWebhookSignature`,
  поля Node `WebhookSignatureHeader` / `WebhookSignaturePrefix`,
  валидация в `Node.Validate` (header/secret required + length ≤128/≤64),
  4 новых sentinel-ошибки в [errors.go](../internal/domain/errors.go) и
  одна handler-маппится в 400 (`ErrCallbackNotAllowed`).
  · Верификация подписи: [webhook_signature.go](../internal/receiver/usecase/webhook_signature.go)
  `VerifyWebhookSignature(node, h, body)`: берёт значение header'а,
  отрезает prefix (если задан), hex-decode → HMAC-SHA256(body, secret) →
  `subtle.ConstantTimeCompare`. Любая ошибка форматирования — 401
  `ErrAuthHeaderMalformed`; mismatch — `ErrUnauthorized`. Этот же путь
  реиспользуется в `CheckIncomingAuth` для всех роутов (sync/async/callback)
  — расширил сигнатуру `CheckIncomingAuth(node, h, body []byte)`, body=nil
  игнорируется для non-webhook кейсов. Обновлены 3 вызова в route.go,
  route_async.go, dry_run.go.
  · Receiver handler [handler.go](../internal/receiver/adapter/in/http/handler.go):
  `POST /v1/callback/*path` → `handleCallback` → `handleAsyncFromInput`
  с флагом `RequireCallback=true`. `RouteAsync` отбрасывает запрос с
  `ErrCallbackNotAllowed`, если у узла другой `IncomingAuthType`.
  Корневой 404 ловит `/callback/*` без `/v1/` префикса.
  · Web DTO: `incoming_auth_type` в `binding:oneof` принимает
  `webhook_signature`; новые поля `webhook_signature_header` /
  `webhook_signature_prefix` в `CreateNodeRequest` и `NodeResponse`;
  маппинг через `reqToDomain` и `nodeToResponse`. Audit diff
  ([node.go](../internal/web/usecase/node.go) `diffNodes`) дополнен
  тремя ключами: `incoming_auth_type`, `webhook_signature_header`,
  `webhook_signature_prefix` — UI получит compact-таблицу before/after
  (Phase 6.9 уже умеет рендерить такой формат).
  · Чтение Node везде расширено новыми колонками: NodeRepoPg (Web),
  nodecache.Reader (Receiver, через PG-fallback), nodepg.Reader (Sender)
  — SELECT/scan/INSERT/UPDATE.
  · Unit-тесты [webhook_signature_test.go](../internal/receiver/usecase/webhook_signature_test.go)
  — 10 тестов: happy-path (real HMAC), mismatch, wrong secret, missing
  header, malformed prefix (md5= вместо sha256=), non-hex value, пустой
  header name (defence-in-depth), без prefix (всё значение — hex), пустое
  тело (Stripe-style ping), CheckIncomingAuth-делегирование, валидация
  Node (header/secret required для webhook_signature). Существующие
  тесты в [auth_test.go](../internal/receiver/usecase/auth_test.go)
  обновлены под новую сигнатуру `CheckIncomingAuth(_, _, nil)`.
  · Swagger перегенерирован — `webhook_signature_header`/`_prefix` в
  Node-DTO, `incoming_auth_type=webhook_signature` в enum.
  · Frontend (NodeSettings.tsx + i18n + локали SPA) — Phase 8.1b
  (отдельный коммит).

Сделанное в Phase 7.13:

- 7.13 Unit-тесты для трёх ранее непокрытых пакетов: `platform/build`,
  `sender/adapter/out/httpclient`, плюс integration-тесты для
  `platform/circuitbreaker` (требует Redis — testcontainers helper уже есть в
  [tests/integration/redis_test.go](../tests/integration/redis_test.go), новой
  зависимости не вносим).
  · **`internal/platform/build`** ([build_test.go](../internal/platform/build/build_test.go))
  — 6 тестов: `NewOption` парсит `versioninfo.json` (StringFileInfo.ProductVersion,
  FixedFileInfo, IconPath, ProjectName, WorkingDir); ldflags-vars (`Version`/`Commit`/
  `BuildDate`) переопределяют значение из JSON для release-сборки (см. Phase 7.6);
  fallback к JSON при пустых ldflags;
  `parse versioninfo.json` error на битом JSON; пустой `{}` → пустая версия,
  Option не-nil; `WorkingDir()` в test-среде возвращает непустой путь
  (cwd при `service.Interactive()=true`). Helper `resetLdflagsVars` через
  `t.Cleanup` восстанавливает глобалы — единственное место, где приходится
  работать с пакетным var'ом (это сами release-флаги, изолировать иначе нельзя).
  · **`internal/sender/adapter/out/httpclient`** ([client_test.go](../internal/sender/adapter/out/httpclient/client_test.go))
  — 8 тестов через `httptest.Server`: happy-path 200 с проверкой что headers/body
  пробрасываются туда-обратно; 5xx статус не превращается в Go error, а отдаётся
  в `resp.StatusCode`; per-request timeout (`req.TimeoutMs=50ms`, сервер спит
  200ms → `deadline exceeded`); default timeout 30s при `TimeoutMs<=0`; connection
  refused на `127.0.0.1:1`; невалидный `Method` (`BAD METHOD`) → `build http
  request` error; `context.Cancel()` родительского ctx прерывает Do; большое
  тело ответа (64 KB) целиком прочитано через `io.ReadAll`. Все `t.Parallel()`.
  · **`internal/platform/circuitbreaker`** ([tests/integration/circuitbreaker_test.go](../tests/integration/circuitbreaker_test.go))
  — 6 integration-тестов через testcontainers Redis (helper `startRedis`
  переиспользуется из `redis_test.go`): свежий ключ — `closed`/Allow=true;
  после `threshold` failures — `open`, Allow=false пока cooldown активен;
  по истечении cooldown первый `Allow` → `half_open` + true (пробный запрос);
  `RecordSuccess` в любом состоянии возвращает в `closed` и сбрасывает failures
  (Allow=true немедленно, без cooldown); изоляция ключей (`node-A` open, `node-B`
  остаётся closed); `State()` на неизвестном ключе → `closed` (а не пустая
  строка / Redis-Nil error). Tests c sleep пропускаются в `-short`.
  · Все unit-тесты проходят `go test -short`; integration — под build-tag
  `integration` (запуск `make test-integration` с Docker).
  · Покрытие unit-тестами: 12 → **14 пакетов** + `circuitbreaker` теперь под
  integration-сетапом.

Сделанное в Phase 7.12:

- 7.12 Расширение unit-test покрытия на ранее непокрытые usecase-пакеты (~38
  новых тестов, продолжение линии Phase 7.11):
  · **`internal/receiver/usecase/envelope.go`** ([envelope_test.go](../internal/receiver/usecase/envelope_test.go))
  — 5 тестов на `BuildEnvelope`: happy-path (ID/NodePath/Method/TargetURL/AuthHeader/
  ClientIP/Body копируются; ForwardHeaders + Content-Type — единственные заголовки,
  что попадают в envelope; ReceivedAt свежий и в UTC); ForwardHeaders=nil + только
  Content-Type автодобавляется (Authorization/Cookie из исходных headers НЕ утекают);
  отсутствие Content-Type → ничего лишнего; merge query c существующим URL'ом;
  case-insensitive ForwardHeaders → нормализация через CanonicalHeaderKey.
  · **`internal/web/usecase/orphan_scanner.go`** ([orphan_scanner_test.go](../internal/web/usecase/orphan_scanner_test.go))
  — 24 теста (18 table-driven для `isSafeTableNameLocal` + 6 на Scan/Drop):
  валидные форматы `db.table` (digits, underscores, mixed case), отказы на SQL-инъекции
  (semicolon/quote/space/backtick), кириллице, hyphen, без точки, ведущая/завершающая
  точка, две точки, длина >128, граничный 128 = ok; для `Drop` — отказ на
  невалидное имя, отказ на чужую БД, отказ если таблица всё ещё в use узла
  (case-insensitive match через known-set), nil-conn после прохождения guard'ов.
  Тестируется через `OrphanScannerConnProvider`-интерфейс (`nilConnProvider`),
  чтобы не тащить полный `chdriver.Conn` mock.
  · **`internal/web/usecase/api_token.go`** ([api_token_test.go](../internal/web/usecase/api_token_test.go))
  — 9 тестов: `generateToken` (префикс `db_`, длина = prefix+43 символа base64-url,
  два подряд токена различны = энтропия есть); `hashToken` (SHA-256 hex детерминистичен,
  ровно 64 lowercase-hex символа); `Create` happy-path (в БД лежит SHA-256, не plain;
  Prefix = первые 8 символов, audit-запись с `ActionAPITokenCreate`); `Create` без
  name → ошибка `name is required`; `Verify` happy-path с асинхронным `TouchLastUsed`
  (через `assert.Eventually`); `Verify` table-driven на 5 невалидных форматов
  (`not an api token`); неизвестный токен → `ErrUnauthorized` (а не `ErrNotFound` —
  не утекаем существование); просроченный токен → `ErrUnauthorized`; revoked токен →
  `ErrUnauthorized`; inactive user → `ErrUserInactive`. Шифрование/scopes —
  через `inMemAPITokenRepo` и `userRepoStub` (новые, локальные для теста, чтобы не
  конфликтовать с уже существующим `stubNodeRepo` в `replay_test.go`).
  · **`internal/sender/usecase/send.go`** ([send_test.go](../internal/sender/usecase/send_test.go))
  — 8 тестов: `md5hex` (детерминистичный, lowercase hex 32 символа, MD5("") =
  `d41d8cd98f00b204e9800998ecf8427e`); `extractQuery` table-driven (с query/без/пустой
  marker/broken URL); `Send` happy-path 200 (запись лога с done=true, LogResponseBody
  отрабатывает, `RecordSuccess` на breaker, attempts_details пустой для 1 попытки);
  circuit breaker open → 503 без HTTP-вызова, reason=`circuit_breaker_open`;
  4xx ответ не ретраится даже при RetryCount=3 → 1 попытка, breaker `RecordFailure`;
  5xx → 5xx → 2xx — retry с экспоненциальным backoff до успеха, в attempts_details
  лежит JSON всех попыток; все попытки в conn refused → status=0, error из последней
  попытки, breaker.RecordFailure; LogRequestBody=true сохраняет request body, query
  из TargetURL парсится в `rec.Parameters`; nil-breaker заменяется на `noopBreaker`
  (Allow всегда true).
  · **`internal/sender/usecase/async.go`** ([async_test.go](../internal/sender/usecase/async_test.go))
  — 8 тестов на `AsyncProcessor.Handle` (Kafka-обработчик, §3.6): битый JSON envelope →
  Ack (не повторяем); неизвестный узел (`ErrNodeNotFound`) → Ack; временная ошибка
  чтения узла → Retry (без commit'а offset'а); disabled-узел → Ack (дроп); paused-узел →
  Retry + sleep `pausedRetryAfter` (для теста сокращён до 5ms); enabled+2xx → Ack
  без DLQ; enabled+5xx → DLQ с headers (`reason="status=502 ..."`, `last_attempt_at`);
  DLQ produce failed → Retry (не теряем сообщение).
  · **Windows OOM при `go test -p N ./...`** — компиляция test-binary для двух
  тяжёлых пакетов (`sender/usecase` + `web/usecase`) параллельно валит линкер с
  `VirtualAlloc errno=1455`. Решается прогоном `go test -p 1 ./internal/...` или
  по одному пакету. Это известная Windows-специфика (CLAUDE.md «грабли» #1), не
  регрессия — в CI/Linux всё стандартно.
  · Покрытие в IMPLEMENTATION.md обновлено: 11 → **12 пакетов**
  (+ `sender/usecase`; для `web/usecase` и `receiver/usecase` пакеты не новые,
  но покрытие в них существенно расширено новыми файлами тестов).

Сделанное в Phase 7.11:

- 7.11 Unit-тесты для 3-х ранее непокрытых пакетов (чистая логика, без deps):
  · **`internal/platform/healthcheck`** ([healthcheck_test.go](../internal/platform/healthcheck/healthcheck_test.go))
  — 5 тестов: `Live` всегда 200; `Ready` 4 кейса (all_up / required_down→503 /
  optional_down→degraded:true / required_takes_precedence) + no_checkers;
  таймаут-propagation в `Check(ctx)` — handler не зависает дольше Timeout;
  `CheckerFunc` адаптер.
  · **`internal/platform/config`** ([load_test.go](../internal/platform/config/load_test.go))
  — 8 тестов / 25+ подтестов: `expandEnv` (10 граничных кейсов включая
  `${VAR}`/`${VAR:default}`/пустой default/невалидное имя/$5.99 не expand'ится),
  `resolveConfigPath` (приоритет flag > env > debug > default), `applyDefaults`
  (не затирает ненулевые значения, заполняет zero defaults), `Validate` (10
  подтестов на missing-обязательных + bad samesite + sentry-use-no-dsn), `Load`
  end-to-end (YAML + env-substitution + applyDefaults + validate), error-кейсы
  (отсутствующий файл, невалидный YAML, validate-fail).
  · **`internal/web/usecase/audit`** ([audit_test.go](../internal/web/usecase/audit_test.go))
  — 6 тестов: `SystemActor()` поля; `Log` сохраняет все поля + CreatedAt в UTC;
  nil-details нормализуется в пустой map (защита downstream JSON-маршаллинга);
  `errAuditRepo` инжектит ошибку — `Log` её не пробрасывает (§7.13 «сбой
  аудита не ломает бизнес-операцию»); `List` делегирует в repo; фабрика
  `auditEntry` всегда выдаёт UTC + non-nil Details.
  · Известное ограничение Go: `t.Parallel()` несовместим с `t.Setenv` —
  где нужны env-моки, parallel выключен (закомментировано в тестах).
  · Покрытие в IMPLEMENTATION.md обновлено: 8 → **11 пакетов** (с учётом
  ранее добавленных в Phase 7 reloader/clickhouse/nodecache).

Сделанное в Phase 7.10:

- 7.10 CHANGELOG.md — [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) формат.
  · Единая секция `[Unreleased]` с подзаголовками по фазам — нужная история
  есть, но `[x.y.z]`-заголовки появятся вместе с первым git-тегом `v*` (после
  чего release-workflow поедет в нормальный SemVer-режим).
  · Содержит **15 фаз** (Phase 0 → 7.9), сгруппированы по Added/Changed
  (для каждой); ссылки на ключевые файлы / handlers / endpoints.
  · GoReleaser archives дополнены `CHANGELOG.md` + `CONTRIBUTING.md`;
  `release.header` теперь ссылается на CHANGELOG как первый источник истины
  (вторым — IMPLEMENTATION.md, третьим — README).
  · README дополнен разделом «Changelog» со ссылкой.

Сделанное в Phase 7.9:

- 7.9 Pre-commit hooks через [lefthook](https://github.com/evilmartians/lefthook):
  · [`lefthook.yml`](../lefthook.yml) — три ивента:
    - **pre-commit** (parallel, на staged-файлах): gofmt → goimports `-local bus`
      → go vet → golangci-lint с `--new-from-rev=HEAD~ --fast` (только новые
      строки, не весь проект — полный прогон у CI).
    - **pre-push** (sequential): `go test -short` + swag drift-check (если
      менялись `internal/web/adapter/in/http/**/*.go`), который сам себя
      откатывает через `git checkout -- docs/` после диагностики.
    - **commit-msg**: regex-проверка формата `Phase N.M: ...` или conventional
      commits (`feat:`, `fix:`, `chore:`, ...).
  · Make-цели `make install-hooks` (auto-install через `go install` если
  `lefthook` нет в PATH) и `make uninstall-hooks` + `make hooks-run`
  (прогон pre-commit вручную, без коммита).
  · CONTRIBUTING.md дополнен разделом «Git hooks» с подробностями ивентов.
  · YAML-syntax валидирован через `gopkg.in/yaml.v3` Unmarshal (локальный
  `lefthook validate` не запускается из-за git 2.24 < 2.31, ограничение
  Windows-окружения автора; в CI/Linux с современным git'ом всё работает).

Сделанное в Phase 7.7:

- 7.7 Security scanning: jobs в [.gitlab-ci.yml](../.gitlab-ci.yml) stage
  `security`. Триггеры: push в master/dev, weekly schedule, manual.
  · **govulncheck** (`golang.org/x/vuln`) — официальный сканер CVE с call-graph
  анализом (не просто проверка версий, а сопоставление с фактически вызываемым
  кодом). Падает на vuln, гейтит pipeline — это разумно, так как call-graph
  отсеивает ложные срабатывания.
  · **gosec** (securego/gosec@latest) — статический анализатор OWASP/CWE
  правил (G-серии). `allow_failure: true` — SARIF в артефакты, чтобы pipeline
  не блокировался шумом. Исключены `web-ui/` и `docs/`.
  · **Trivy fs** (aquasec/trivy:0.55.0) — vuln (включая npm в web-ui) + secret
  scanning + Dockerfile/YAML misconfig. severity CRITICAL|HIGH|MEDIUM,
  `--ignore-unfixed`. SARIF в артефакты, `--exit-code 0` (не блокирует).
  · Nancy убран: Sonatype OSS Index возвращает 403 для анонимных запросов,
  без API token скан не работает. Покрытие CVE Go-модулей сохраняется через
  govulncheck (тоже NVD, плюс call-graph анализ).
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
  · 6 docker-образов через `dockers:` (receiver/sender/web × amd64+arm64) →
  GitLab Container Registry через [release.Dockerfile](../deploy/docker/release.Dockerfile)
  (использует pre-built бинарь, не пересобирает). `docker_manifests:` склеивают
  arch-варианты в multi-arch теги `:{Version}` и `:latest`.
  · [.gitlab-ci.yml](../.gitlab-ci.yml) job `release` — триггер на тег `v*`;
  QEMU + Buildx + login в `$CI_REGISTRY` через `$CI_REGISTRY_PASSWORD`.
  · Makefile цели `make release-check` (синтаксис `.goreleaser.yaml`) и
  `make release-snapshot` (локальный snapshot в `dist/` без публикации).
  · Локальная верификация ldflags: `go build -ldflags "-X .../build.Version=v1.2.3 ..."`
  + `./binary --version` печатает `v1.2.3` вместо `0.1.0` из versioninfo.json.

Сделанное в Phase 7:

- 7.1 Swagger 100% endpoints + UI handler (`/swagger/index.html`):
  допокрыты аннотациями auth (logout/me), nodes (Update/Delete), users
  (все 6 handlers), tokens (все 4), audit (List/ExportCSV); подключён
  `ginswagger.WrapHandler` в [internal/web/app.go](../internal/web/app.go),
  blank-import `_ "nexus/docs/web"` регистрирует генеренный docTemplate
  в `swag.Registry`. Добавлены deps `github.com/swaggo/gin-swagger` и
  `github.com/swaggo/files`. UI открывается по адресу `/swagger/index.html` на Web Service (по умолчанию `:8081`).
- 7.2 L2 in-memory LRU-кеш узлов в Receiver (§9.2):
  декоратор поверх `nodecache.Reader`, реализует тот же `port.NodeReader`.
  При `Enabled=false` декоратор отдаёт inner как есть. Stale-fallback по
  `StaleTTL` (§9.4 крайний случай: одновременно лежат Redis и PG — на
  горячем наборе узлов сервис продолжает отвечать с протухшего слепка).
  `ErrNodeNotFound` НЕ кешируется. Метрики `nexus_l2_cache_hits_total{kind}`,
  `_misses_total`, `_evictions_total`, `_size`. Unit-тесты на детерминированных
  `Clock` (без real sleep).
- 7.5 Grafana dashboard + Prometheus alert rules:
  · `deploy/grafana/nexus.json` — 10 панелей: RPS, error rate (%),
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
- 7.4 CI pipeline в [.gitlab-ci.yml](../.gitlab-ci.yml) — параллельные jobs
  `go-test` (race -short), `go-build` (`go build ./...`), `go-lint`
  (`golangci-lint v2.12`), `swagger-drift` (regen `swag init` → `git diff`),
  `ui-build` (Node 20 + `npm ci` + `npm run lint --if-present` + `vite build`),
  `integration` (testcontainers, по MR-label `run-integration` или master/dev/tag).
  `.golangci.yml` с набором bodyclose/rowserrcheck/errcheck/govet/revive/staticcheck.
  Авто-апдейты зависимостей — [renovate.json](../renovate.json) (weekly
  schedule), группировка minor/patch в один MR.
- 7.3 Integration suite: Redis + ClickHouse через testcontainers.
  Generic-контейнер (`testcontainers.GenericContainer`) — без отдельных
  модулей `modules/redis`/`modules/clickhouse`. CH: native-handshake
  готовится позже `ForListeningPort`, поэтому в helper'е активный retry-ping
  до 60 сек. Покрытие: SessionRepoRedis (CRUD + DeleteByUser + TTL expire),
  NodeCacheRedis (Set/GetByPath/Invalidate), chlog.Writer → реальная
  MergeTree-таблица → LogReaderCH (`GetByID`, `Search` с фильтрами
  status/IP/Done/full-text, защита от SQL-инъекции в имени таблицы).
  Весь интеграционный набор (PG + Kafka + Redis + CH) проходит за ~200s.
