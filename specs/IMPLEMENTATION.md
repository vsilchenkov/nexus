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
| Лимиты полей (path 1-255, timeout 100-600000 ms, ...) | ✅ | [domain/node.go](../internal/domain/node.go) `Validate()` + DB-constraints в [migrations/0002](../migrations/0002_nodes_methods_users.up.sql), CHECK таймаута поднят в [0024](../migrations/0024_nodes_timeout_600s.up.sql) |
| Soft/hard лимит узлов | ✅ | `NodeUsecase.Create` — `nodesHardLimit` → `ErrLimitReached` |

### §4 Sender Service

| Пункт | Статус | Где |
|---|---|---|
| gRPC `SenderService.Send` | ✅ | [proto/sender/v1/sender.proto](../proto/sender/v1/sender.proto), [adapter/in/grpc/sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) |
| HTTP-клиент с keep-alive + retry/backoff | ✅ | [adapter/out/httpclient/client.go](../internal/sender/adapter/out/httpclient/client.go) |
| Kafka-consumer + DLQ + paused-pacing | ✅ | [adapter/in/kafka/consumer.go](../internal/sender/adapter/in/kafka/consumer.go), [usecase/async.go](../internal/sender/usecase/async.go) |
| Offset коммитится только после успешной доставки | ✅ | `enable_auto_commit: false` + ручной `Commit` после терминального исхода |
| **Retry-in-place для HandleRetry (PG-сбой/DLQ недоступен): сообщение не теряется** | ✅ Phase 2 (fix) | [consumer.go](../internal/sender/adapter/in/kafka/consumer.go) `processWithRetry`; см. 4.37 |
| **Delay-топик paused-узлов (§3.6): пауза не тормозит соседей по партиции** | ✅ Phase 6 | [paused_sweeper.go](../internal/sender/adapter/in/kafka/paused_sweeper.go), [async.go](../internal/sender/usecase/async.go) `handlePaused`; конфиг `kafka.paused_topic` + `sender.paused_sweep`; см. 4.38 |
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
| **Replay GET без тела + поле «Параметры» (`params_override`)** (§7.4.1) | ✅ | [replay.go](../internal/web/usecase/replay.go) (GET не требует `orig.Request`; `ParamsOverride`: nil=из лога, ""=без параметров, кривая строка→`ErrReplayBadParams`→400 `replay.bad_params`; `__replay_of` всегда поверх), [ReplayDialog.tsx](../web-ui/src/components/ReplayDialog.tsx) (prefill из детали лога, `__replay_of` вычищается; эффективный метод зеркалит бэкенд: incoming, ANY→глагол лога). Бонус: массовый ReplayFailed GET-узлов без тел не падает построчно |
| **Basic-креды: Логин/Пароль в UI + `auth_login`/`incoming_auth_login` в API** (§41.7) | ✅ | [dto.go](../internal/web/adapter/in/http/dto.go) `basicLogin` (до первого `:`, только basic; логин — не секрет, пароль наружу не отдаётся; `sensitiveKeys` намеренно не трогали), [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) `buildPayload` (склейка `login:password`; пустой пароль = не менять), [nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts) (`:` в логине запрещён; смена логина требует пароль заново — решение пользователя), [ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx) (входящая/исходящая auth отдельными строками + логин; там же строка «Таймаут / повторы») |
| **SSE live-tail `/api/nodes/{id}/logs/stream`** (§7.4) с heartbeat | ✅ Phase 5 | [usecase/logs.go](../internal/web/usecase/logs.go) `Subscribe`, [http/logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `Stream` |
| **Ленивые тела логов (§7.4.2): list/stream без `request`/`response`, тела по клику** | ✅ Phase QA.2026-06 | `GET /api/nodes/{id}/log/{logId}` ([logs_handler.go](../internal/web/adapter/in/http/logs_handler.go) `Get`, [logs.go](../internal/web/usecase/logs.go) `GetByID`, route в [routes.go](../internal/web/adapter/in/http/routes.go)); `toLogDTO(r, includeBodies)` режет тела для списков/SSE. UI: раскрытие строки в [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) (`LogBodies` грузит тело лениво); аудит — ленивый `<pre>` по `onToggle` в [AuditDetailsCell.tsx](../web-ui/src/components/AuditDetailsCell.tsx) |
| Settings → API Tokens | ✅ Phase 5.1 | [pages/settings/ApiTokens.tsx](../web-ui/src/pages/settings/ApiTokens.tsx) |
| Settings → Language / Theme | ✅ Phase 5.1 | [pages/settings/Language.tsx](../web-ui/src/pages/settings/Language.tsx), [Theme.tsx](../web-ui/src/pages/settings/Theme.tsx) |
| Audit log страница | ✅ Phase 5.1 | [pages/AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx) |
| **i18n / Accept-Language (en/ru)** на стороне backend и SPA | ✅ Phase 5 | [platform/i18n/](../internal/platform/i18n/), [web-ui/src/locales/](../web-ui/src/locales/), [web-ui/src/i18n.ts](../web-ui/src/i18n.ts) |
| Auth: users CRUD, sessions Redis, RBAC, must_change_password | ✅ | [web/usecase/auth.go](../internal/web/usecase/auth.go), [adapter/out/redis/session_repo.go](../internal/web/adapter/out/redis/session_repo.go) |
| **Роль `manager` + иерархия рангов (RBAC, §26)** | ✅ Phase A | `viewer<operator<manager<admin` (четвёртая роль — §87) через [domain.UserRole.Rank/AtLeast](../internal/domain/enums.go), middleware `RequireMinRole` ([auth_middleware.go](../internal/web/adapter/in/http/auth_middleware.go)), группа `authedManager` ([routes.go](../internal/web/adapter/in/http/routes.go)), миграция [0013_user_role_manager](../migrations/0013_user_role_manager.up.sql). Менеджер: узлы CRUD/dry-run, каталоги Allowed Hosts/Headers, чтение Audit; не трогает users/teams/общие настройки/CH-шаблоны/move. UI-гейтинг по `minRole` ([web-ui/src/lib/roles.ts](../web-ui/src/lib/roles.ts), [Settings.tsx](../web-ui/src/pages/Settings.tsx)) |
| **Self-service смена своего пароля `POST /api/me/password` (§26.4)** | ✅ Phase A | `AuthUsecase.ChangeOwnPassword` (подтверждение текущего пароля, инвалидация всех сессий) [auth.go](../internal/web/usecase/auth.go), handler [auth_handler.go](../internal/web/adapter/in/http/auth_handler.go), UI [pages/settings/Password.tsx](../web-ui/src/pages/settings/Password.tsx) |
| API-токены: `db_<base64>` префикс, SHA-256 hash, scopes, audit | ✅ | [web/usecase/api_token.go](../internal/web/usecase/api_token.go), [http/api_token_middleware.go](../internal/web/adapter/in/http/api_token_middleware.go) |
| API-токены: **rotate** (перевыпуск значения, `POST /api/tokens/:id/rotate`) | ✅ | `APITokenUsecase.Rotate` + `APITokenRepo.Rotate` (только активный токен: `revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, иначе 404); сохраняет id/name/scopes/team/expires, сбрасывает `last_used_at`; audit `api_token.rotate`; plain один раз. UI — кнопка «Rotate» в [ApiTokens.tsx](../web-ui/src/pages/settings/ApiTokens.tsx) (переиспользует баннер `copy_now`) |
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
| **Семантика offset'ов async-consumer'а: изоляция paused, at-least-once, FIFO, multi-instance** | ✅ (Phases 2–6, см. 4.37/4.38) | [sender_async_redelivery_test.go](../tests/integration/sender_async_redelivery_test.go) (перенос в delay-топик; сосед-enabled НЕ ждёт paused; бэклог уезжает после unpause; переживает рестарт sweeper'а), [sender_async_atleastonce_test.go](../tests/integration/sender_async_atleastonce_test.go) (падение до commit'а → ровно один дубль), [sender_async_fifo_test.go](../tests/integration/sender_async_fifo_test.go) (порядок [1..5]; ретраи головы → [1,1,2,3,4,5] + DLQ), [sender_async_multiinstance_test.go](../tests/integration/sender_async_multiinstance_test.go) (partitions=2/instances=2). Юниты — [consumer_test.go](../internal/sender/adapter/in/kafka/consumer_test.go) (маппинг HandleResult→Commit) и [paused_sweeper_test.go](../internal/sender/adapter/in/kafka/paused_sweeper_test.go) (проход, wrap-detect, commit-before-break) |
| **Replay-сценарий через ClickHouse** | ✅ Phase 9.3 | [tests/integration/replay_test.go](../tests/integration/replay_test.go) — PG+CH; `ReplayUsecase` поверх реального `LogReaderCH` проверяет маркер `__replay_of=<orig_id>` в query, сохранение исходных query-параметров, тело из CH-записи и audit-запись `node.replay` |
| **Auth E2E (login + session + role)** | ✅ Phase 9.3 | [tests/integration/auth_test.go](../tests/integration/auth_test.go) — PG `UserRepoPg` + Redis `SessionRepoRedis`; happy/bad-password/inactive, `Check` продлевает TTL, `ChangePassword` инвалидирует все сессии, audit `user.login.*` / `user.password.change` |
| **Receiver incoming auth через реальный HTTP** | ✅ Phase 9.3 | [tests/integration/receiver_incoming_auth_test.go](../tests/integration/receiver_incoming_auth_test.go) — Gin + `httptest.NewServer`; узлы none/basic/token, проверка 401/200 для отсутствующего/малформенного/неверного/верного `Authorization`, гарантия что 401 не достигает upstream |

### §11 Swagger / OpenAPI

| Пункт | Статус | Где |
|---|---|---|
| Аннотации `@Summary/@Param/...` на ключевых handlers | ✅ Phase 5 | login, nodes (List/Get/Create), dry-run, replay, logs (List/Stream) |
| `make swagger` (через `swag init -g cmd/web/main.go`) | ✅ | [Makefile](../Makefile) |
| `make swagger-drift-check` для CI | ✅ Phase 5 | сравнивает `git diff --exit-code docs/` после регенерации |
| **Полные аннотации на 100% endpoints** | ✅ Phase 7.1 | auth (login/logout/me), nodes (List/Get/Create/Update/Delete), users (List/Get/Create/Update/Delete/ChangePassword), tokens (List/Create/Revoke/Rotate/Delete), audit (List/ExportCSV), dry-run, replay, logs (List/Stream), settings/app (Get/Update/TestClickHouse/TestSentry), settings/clickhouse/orphans (List/Drop) |
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
  - ✅ UX членств (2026-07-24): `MembersDialog` — пикер пользователя заменён на searchable-combobox `UserCombobox` (Popover + cmdk, встроенный фильтр по `value`=«имя логин email»; login в value гарантирует уникальность у тёзок), над таблицей участников — client-side фильтр по имени/логину/email (отвечает «есть ли он уже в команде»), таблица в скролл-обёртке `max-h-[50vh]` + sticky thead, у локального `Modal` — страховочный `max-h-[calc(100vh-4rem)]` (при 15+ участниках окно вылезало за экран). Грабли: Esc в открытом popover доходил до window-слушателя `Modal` и закрывал весь диалог — на `PopoverContent` нужен `onEscapeKeyDown={e => e.stopPropagation()}`; z-index попапа поднят до `z-[60]` (Modal — `z-50`). Обратная сторона: в `Settings → Users` кнопка 👥 в строке открывает `UserTeamsDialog` (текущие членства read-only + добавление в команду через командо-центричный `POST /api/teams/{id}/members` — user-центричного эндпоинта нет и не нужно); в state хранится `teamsTargetId` (не снапшот User), чтобы после инвалидации `["users"]` открытый диалог получал свежий `user.teams`. Follow-up (по просьбе пользователя): чипы «Текущих команд» активны — × на чипе удаляет из команды через `useConfirm()`-диалог (`DELETE /api/teams/{id}/members/{user_id}`, те же инвалидации, что у add; confirm-модалка `ConfirmProvider` рендерится в корне App ПОСЛЕ дерева, поэтому рисуется поверх локального Modal при одинаковом `z-50`); на странице `Settings → Teams` — client-side поиск по slug/названию/БД + скролл-обёртка таблицы `max-h-[65vh]` со sticky thead (паттерн Users); звёздочка на чипе кликабельна — смена default-команды (§45, тот же `PUT /api/users/{id}/default-team`, что у чипов колонки «Команды»). Файлы: [Teams.tsx](../web-ui/src/pages/settings/Teams.tsx), [Users.tsx](../web-ui/src/pages/settings/Users.tsx).
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
| **C.1: предупреждение «настройки схемы CH не применяются к существующей таблице»** | ✅ | UI-only. Ловушка: таблица создаётся один раз (`CREATE TABLE IF NOT EXISTS` в `RenderCreateTable`+`provisionTable`), синхронизации схемы с шаблоном нет — **нет** `ALTER … MODIFY CODEC`/`ADD INDEX`/`MODIFY TTL`. Смена `template_id` при том же имени таблицы → `provisionTable` вызовется, но это no-op. Исключение — retention: `ClickHouseRetentionDays` применяется housekeeping'ом (`ALTER … DROP PARTITION`, [ch_housekeeping.go](../internal/sender/usecase/ch_housekeeping.go)). Предупреждаем в двух местах: смена шаблона у существующего узла с тем же именем таблицы ([NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx), хелпер [chSchema.ts](../web-ui/src/lib/chSchema.ts)) и правка существующего шаблона ([CHTemplatesPanel.tsx](../web-ui/src/components/CHTemplatesPanel.tsx)). Реальное применение схемы к существующей таблице — отдельная фича C.2 (`ALTER`). |

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
| **§57: гарантированная инвалидация конфига узла в Receiver** | ✅ §57 | ТЗ [57-node-cache-invalidation.md](sections/57-node-cache-invalidation.md), ветка `feature/node-cache-invalidation`. Продолжение §50: write-through в Redis из Web молча становится no-op при провале резолва team-slug (`resolveCacheTeamSlug`=""), и старый конфиг (в т.ч. авторизация) живёт до TTL (до 300 с); L1-кеш Receiver'а (`L2Reader`) Web вообще не достаёт. **Решение** — выделенный pub/sub канал [platform/nodeevents](../internal/platform/nodeevents/nodeevents.go) `nexus:nodes:invalidate` (`Event{team_id,path,old_path}`; Publisher nil-safe + Subscriber). **Web** ([node.go](../internal/web/usecase/node.go)) публикует событие после commit в Create/Update/Delete/SetStatus/Move (`publishInvalidate`, инъекция сеттером `SetInvalidationPublisher` — позиционный параметр затронул бы ~16 вызовов; wiring [app.go](../internal/web/app.go)); при переименовании — `old_path`, при Move — исходная+целевая команда. **Receiver** ([app.go](../internal/receiver/app.go)) подписан: `nodeInvalidateHandler` резолвит `team_id`→slug (`teamSlugByID`, PG) и выселяет узел из L1 (`L2Reader.Invalidate`) + Redis (`Reader.Invalidate`=`DEL`), следующий Get читает свежий конфиг из PG. Публикация по `team_id`, а не slug: провал резолва slug на Web (тот же сбой, что ломает write-through) не блокирует инвалидацию — slug резолвит Receiver (§57.3). Best-effort с обеих сторон; остаточное окно — только полный отказ Redis в момент publish. Тесты: nodeevents (nil-safe/JSON/omitempty), nodecache (`L2Reader.Invalidate` выселяет L1+делегирует; `Reader.Invalidate` nil-redis no-op), node usecase (SetStatus публикует, no-op — нет). Кросс-сервисный прогон через реальный Redis pub/sub — integration/стенд-гейт |
| **§58: поделиться узлом — ссылка на страницу узла + авто-переключение команды** | ✅ §58 | ТЗ [58-node-share.md](sections/58-node-share.md), ветка `feature/node-share`. **58.1 (backend-резолвер)**: `NodeUsecase.ResolveTeam(userID, nodeID)` ([node.go](../internal/web/usecase/node.go)) — узел без team-скоупа + команда узла среди членств (`teams.ListUserTeams`, `teams` уже проброшен — правки конструктора не нужны); узла нет ИЛИ не член → единый `ErrNodeNotFound` (no-leak, как Get). Handler `NodeHandler.ResolveTeam`+`ResolveTeamResponse` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go)); маршрут `GET /api/nodes/:id/team` (`RequireSessionOnly`, [routes.go](../internal/web/adapter/in/http/routes.go)); unit [node_resolve_team_test.go](../internal/web/usecase/node_resolve_team_test.go). **58.2 (авто-переключение)**: [lib/nodeShare.ts](../web-ui/src/lib/nodeShare.ts) — `useNodeTeam`+`useEnsureNodeTeam` (статус loading/switching/ready/unavailable; переключение РОВНО ОДИН РАЗ на пару «узел + его команда» — guard по `${id}:${target}`, ready залипает `latchedKey`, чтобы ручная смена команды не сбрасывала UI; решение принимается только по свежему ответу `isFetchedAfterMount` + `refetchOnMount:"always"` — иначе после переноса узла сессию уводило в старую команду, §4.51), регрессия — [nodeShare.test.tsx](../web-ui/src/lib/nodeShare.test.tsx); [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx)/[NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) грузят узел только при ready, unavailable → `node.unavailable`. **58.3 (кнопка «Поделиться»)**: [lib/clipboard.ts](../web-ui/src/lib/clipboard.ts) (единый `copyToClipboard`, CopyButton отрефакторен на него), [ShareNodeButton.tsx](../web-ui/src/components/node/ShareNodeButton.tsx) — копирует `${origin}/nodes/{id}`, доступна всем ролям; в шапке просмотра и формы правки (только `!isNew`). **58.4 (RBAC-фикс Replay)**: `POST /logs/:id/replay` перенесён из `authed` в `authedManager`+`RequireSessionOnly` (viewer → 403; replay = сайд-эффект на внешнюю цель); кнопки Replay в [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx)/[QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx) гейтятся `useRoleAtLeast("manager")` (viewer — disabled, `common.no_permission`). Аудит остальных кнопок узла — уже соответствовали RBAC. i18n `node.actions.share`/`node.unavailable`/`common.no_permission` (ru+en); swagger web перегенерирован. Неочевидности — §4.36. Гейты `-race`/`make test-integration`/браузерный стенд — финальный прогон |
| **§52: трёхсостоянье статуса узла OK / Degraded / Down** | ✅ §52 | ТЗ [52-node-degraded-status.md](sections/52-node-degraded-status.md), ветка `feature/node-degraded-status`. Продолжение §50.4: серия 422 красила живой узел в «Down» (флаг был булев «любой не-2xx»). **Phase 52.2**: [domain/node_outcome.go](../internal/domain/node_outcome.go) — `NodeOutcome` (ok/degraded/down), `OutcomeFromStatusCode` (2xx→ok; <=0 или >=500→down, вкл. 503 breaker-open/502 oversize; иначе degraded — граница = upstreamHealthy §50.4), `IsError()` (прежняя семантика), `GaugeValue`/`OutcomeFromGaugeValue` (0/1/2). **Phase 52.3 (писатель)**: gauge `SetNodeLastRequestOutcome` 0=ok/1=degraded/2=down ([platform/metrics](../internal/platform/metrics/metrics.go)); Redis-кодек `EncodeOutcome`/`DecodeOutcome` — «свап» "1"=down/"2"=degraded ради legacy/rolling (см. 4.29; [platform/nodestatus](../internal/platform/nodestatus/redis.go)); порт `NodeStatusWriter.SetLastOutcome`; обе точки записи ([sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) + [async.go](../internal/sender/usecase/async.go)); `incomplete_total`/ack/DLQ не менялись. **Первый тест gRPC-адаптера** [sender_service_test.go](../internal/sender/adapter/in/grpc/sender_service_test.go) (реальный SendUsecase, таблица 200/302/422/500/транспорт). **Phase 52.4 (читатель)**: порт `NodeStatusReader.GetLastOutcomes`; reader MGET+декод ([node_status.go](../internal/web/adapter/out/redis/node_status.go)); `NodeThroughputRow.LastOutcome` + `applyLastOutcomes` (Redis приоритет → Prom `OutcomeFromGaugeValue` → ok; [metrics.go](../internal/web/usecase/metrics.go)); API `last_outcome` аддитивно + `last_error` back-compat (= IsError; [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go)); попутно устранён swagger-дрейф `NodesMetricsResponse` (не было `totals`). **Phase 52.5 (UI)**: Variant `degraded` (отдельный от `warn`=Queue), Pill tone warn «Degraded» (латиницей в обеих локалях), сортировка err→degraded→queue, опция фильтра, `Throughput.lastOutcome` ([Overview.tsx](../web-ui/src/pages/Overview.tsx)); бандл пересобран. **Phase 52.6 (integration)**: round-trip всех исходов + legacy "1"→down + мусор→fallback ([nodestatus_test.go](../tests/integration/nodestatus_test.go)); сценарий инцидента 422×3→degraded, 500→down, 200→ok через реальные AsyncProcessor+Redis ([nodestatus_scenario_test.go](../tests/integration/nodestatus_scenario_test.go)). Полный gRPC-E2E сознательно не делался (тонкий маппинг закрыт юнитом). Неочевидности — §4.29 |
| **§53: копирование узла — кнопка «Скопировать узел»** | ✅ §53 | ТЗ [53-copy-node.md](sections/53-copy-node.md), ветка `feature/copy-node`. **Phase 1.1 (backend)**: `POST /api/nodes/:id/copy` (manager+, [routes.go](../internal/web/adapter/in/http/routes.go)) → `NodeHandler.Copy` + `CopyNodeRequest` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go), ошибки через общий `replyDomainError`: 404/409/400, новых i18n-ключей не потребовалось); usecase [node_copy.go](../internal/web/usecase/node_copy.go) — `Copy` (team-scope как Get, клон `cloneNodeForCopy`: сброс ID/таймстемпов, **всегда paused**, `slices.Clone` для ForwardHeaders; креды копируются plaintext-в-памяти → re-encrypt в pg-адаптере), клонирование ссылок allowlist-хостов `copyHostLinks` в той же UoW-транзакции + пересборка снимка, аудит `domain.ActionNodeCopy` ([audit.go](../internal/domain/audit.go)) с `source_node_id`/`source_path`/`allowed_hosts_cloned`; из `Create` извлечён общий пайплайн `prepareNewNode` ([node.go](../internal/web/usecase/node.go)) — поведение Create не изменено; CH-таблица копируется как есть (§37). Unit [node_copy_test.go](../internal/web/usecase/node_copy_test.go) (happy/scope/конфликт path — `memNodeRepo` научен UNIQUE(team_id,path)/невалидный path/hard-limit/host-links через `fakeUow`/идемпотентный провижининг). **Phase 1.2**: integration `TestNodeUC_Copy_E2E` ([node_copy_test.go](../tests/integration/node_copy_test.go), группа test-int-pg) — decrypt→re-encrypt round-trip кредов, paused, клон ссылок+снимка, аудит, 409, team-scope. **Phase 2.1 (UI)**: кнопка «Копировать» в шапке [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) (manager+) + `CopyNodeDialog` (префилл `<path>-copy`, `validateNodePath` из [nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts), POST → invalidate `["nodes"]` → navigate на копию); i18n `node.actions.copy`+`node.copy.*` en/ru; `node.copy` в фильтре [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx); бандл пересобран → `internal/web/static/` |
| **§54: сохранение фильтров рабочего стола (Overview)** | ✅ §54 | ТЗ [54-overview-filter-persistence.md](sections/54-overview-filter-persistence.md), ветка `feature/overview-filter-persistence`. **Баг-репорт**: фильтр на рабочем столе сбрасывался при открытии узла и возврате — «Назад» браузера ИЛИ кнопка «Узлы». Причина: `search`/`method`/`statusFilter`/живой `period` в чистом `useState`, а Overview — дочерний `Outlet` (при переходе на `/nodes/:id` размонтируется). Кнопка «Узлы» — `NavLink to="/"` ([Sidebar.tsx](../web-ui/src/components/Sidebar.tsx)) **без query**, поэтому одного URL мало → гибрид. **Phase 2.1**: чистый модуль [lib/overviewFilters.ts](../web-ui/src/lib/overviewFilters.ts) по образцу [lib/period.ts](../web-ui/src/lib/period.ts) — `parseFilters` (толерантен: junk → дефолт по-полю независимо, `range` приоритетнее `from`/`to`), `serializeFilters` (**только отличия от дефолта** → чистый URL при дефолтных фильтрах), `applyFilters` (не трогает чужие query-ключи), `saveFilters`/`loadFilters` (зеркало; всё-дефолт → `removeItem`), `FILTERS_STORAGE` — одна константа выбора хранилища. Канонический формат в URL и зеркале один — сериализованная query-строка, поэтому restore = `parse → serialize → setParams` и заодно санитизирует мусор из хранилища. Тесты [overviewFilters.test.ts](../web-ui/src/lib/overviewFilters.test.ts) (42 кейса). **Phase 3.1**: [Overview.tsx](../web-ui/src/pages/Overview.tsx) — 4 `useState` → состояние из `useSearchParams` (конвенция [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx), фикс П12) + зеркало `sessionStorage`; `updateFilters` (единая точка записи), restore/mirror-эффект, debounce поиска 300мс. Звёздочка §44.B, `view`, `autoRefresh` не тронуты. **Phase 3.2** (найдено стендом): [PeriodPicker.tsx](../web-ui/src/components/ui/PeriodPicker.tsx) держал `from`/`to` в своём `useState("")` и не инициализировал их из `value` → восстановленный произвольный период был применён, но поля календаря пустые. Дефект предсуществующий (раньше custom-период не переживал уход со страницы) — §54 сделал кейс регулярным. Фикс: `toLocalInput` (RFC3339 → datetime-local в локальной зоне) + засев `useState` + эффект синхронизации (период приходит ПОСЛЕ монтирования, без ремоунта — клик «Узлы» на активной странице); тесты [PeriodPicker.test.tsx](../web-ui/src/components/ui/PeriodPicker.test.tsx) (4 кейса, 2 падают на старом коде — проверено откатом). Затрагивает 5 страниц (Overview/KafkaMonitor/вкладки узла) — везде показанный период теперь соответствует `value`. Бандл пересобран → `internal/web/static/`. **Гейты сдачи**: vitest 104 ✅, lint `--max-warnings=0` ✅, `golangci-lint` 0 issues ✅, полный `-race` в контейнере golang:1.26 ✅, `make test-integration` ✅, браузерный прогон на стенде `services` (Playwright) ✅ — оба кейса баг-репорта («Назад» и «Узлы»), клик «Узлы» на активной странице, F5, дип-линк с мусором (`status=banana&range=99h` → дефолты по-полю, **зеркало санитизировано**), ручная очистка (зеркало `removeItem`, фильтр не воскресает), чистая сессия → период от звёздочки 14д, custom-период в полях `01.07.2026 03:00`. Неочевидности — §4.31 |
| **§55: dry-run — реальный вызов target и полный диалог теста** | ✅ §55 | ТЗ [55-dry-run-real-call.md](sections/55-dry-run-real-call.md), ветка `feature/dry-run-real-call`. **Инцидент-триггер**: боевой узел `vika_task` отдавал `context deadline exceeded` 30с, оператор открыл dry-run и упёрся в `auth.incoming: authorization header missing` — подставить заголовок было нечем (диалог реализовывал 2 поля из 5), а с `use_mock=false` шаг «Response» отдавал `skipped` («real outbound mode is not supported in v1»). **Phase 2 (proto+Sender)**: `bool dry_run = 30` в [sender.proto](../proto/sender/v1/sender.proto) (аддитивно); гейты в [send.go](../internal/sender/usecase/send.go) — breaker целиком пропускается (`if !in.DryRun`), CH под двойным гейтом `LoggingEnabled && !DryRun`; [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) — ранний return до метрик/nodeStatus. **Phase 3 (Web)**: порт [port/sender.go](../internal/web/usecase/port/sender.go); gRPC-клиент переехал `receiver/adapter/out/grpcsender` → [platform/grpcsender](../internal/platform/grpcsender/client.go) (общий для Receiver и Web, `git mv`); конфиг `web.sender_grpc` ([config.go](../internal/platform/config/config.go), `addr` намеренно без дефолта → пусто = реальный режим выключен, шаг `skipped`); [dry_run.go](../internal/web/usecase/dry_run.go) — `realCall` через Sender, `node_path = __dryrun_<path>` (страховка на забытый гейт), self-reference §32.2 (в dry-run её не было вовсе), маскирование auth в эхо-заголовках. **Phase 3b (§55.6)**: креды наружу не отдаются (`nodeToResponse` даёт лишь `*_credentials_set`) → тест сохранённого узла ушёл бы с пустым токеном → 401 → ложный вывод «сломана авторизация». Решение — конвенция §5.5 (как `PUT /api/nodes/{id}`): `node_id` в запросе, пустой кред = взять сохранённый; узел читается в скоупе `currentTeamID` (чужой → 404); зависимость узким портом `nodeCredsLoader` (1 метод, ISP). **Phase 4 (UI + §55.9)**: [DryRunDialog.tsx](../web-ui/src/components/DryRunDialog.tsx) — chips ([dryrun/KeyValueChips.tsx](../web-ui/src/components/dryrun/KeyValueChips.tsx): `HeadersField` не подошёл — он комбобокс ИМЁН из каталога §24), подсказка ([lib/dryRunHint.ts](../web-ui/src/lib/dryRunHint.ts) — зеркалит `incomingAuthValue` Receiver'а с его дефолтами), тумблер mock (вкл. по умолчанию) + предупреждение с реальным target, кнопка «Тестовый запрос» в шапке [NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) (manager+). **§55.9** (найдено при разборе диалога): dry-run расходился с боевым pipeline — не проверял входящий метод, не вычислял исходящий (`outgoing_method=PUT` тестировался POST'ом) и не клеил хвост §39 (passthrough-узел тестировался по БАЗОВОМУ адресу). Фикс — переиспользование того же кода Receiver'а: `MethodMatches`/`EffectiveOutgoingMethod`/`AppendPathSuffix` экспортированы. **Phase 5**: integration [dry_run_no_traces_test.go](../tests/integration/dry_run_no_traces_test.go) на реальных CH+Redis. **Гейты сдачи**: `golangci-lint` 0 issues ✅, `make test` ✅, vitest 130 ✅, lint `--max-warnings=0` ✅, полный `-race` в контейнере golang:1.26 ✅, `make test-integration` ✅, браузерный прогон на стенде ✅ (узел с токеном → 200 с подмешанными кредами; passthrough → echosrv принял `/noauth/echo/v1/orders/42`; побочка не тронута: CH 3844→3844, `last_error` без изменений, метрик по боевому `node_path` — ноль). Неочевидности — §4.35 |
| **§56: синхронизация схемы CH существующей таблицы (ALTER)** | ✅ §56 | ТЗ [56-ch-schema-sync.md](sections/56-ch-schema-sync.md), ветка `feature/ch-schema-sync`. Закрывает ловушку C.1 (смена шаблона не применялась к созданной таблице — `CREATE TABLE IF NOT EXISTS` no-op). Модель **preview + explicit apply** (ALTER'ы тяжёлые/частично необратимы, не побочка Update). **Ядро** — чистый `PlanSchemaSync` ([domain/ch_schema_sync.go](../internal/domain/ch_schema_sync.go)): диффит целевой `CHTemplateSpec` против снимка таблицы → ALTER'ы (CODEC / ADD·DROP INDEX / MODIFY·REMOVE TTL, порядок CODEC→DROP→ADD→TTL) + rejections (движок/ORDER BY/PARTITION BY → пересоздача). **Интроспекция** — [adapter/out/clickhouse/ch_schema_inspector.go](../internal/web/adapter/out/clickhouse/ch_schema_inspector.go): `system.tables`(engine/sorting_key/partition_key) + `system.columns`(compression_codec) + `system.data_skipping_indices` + native-TTL из `SHOW CREATE` (regex распознаёт обе формы записи интервала — `INTERVAL N DAY` и нормализованную ClickHouse `toIntervalDay(N)`). **Usecase** [ch_schema_sync.go](../internal/web/usecase/ch_schema_sync.go) Plan/Apply (резолв шаблона как provisionTable; нет таблицы → TableMissing; inspector==nil → ErrCHUnavailable/503; аудит `node.ch_schema_sync`). **API** `POST /api/nodes/:id/ch-schema/plan\|apply` (manager+, RequireSessionOnly). **UI** [CHSchemaSyncDialog.tsx](../web-ui/src/components/CHSchemaSyncDialog.tsx) (план при открытии, список DDL + rejections, «Применить») + кнопка в CH-секции [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx). Неочевидности: нормализация CODEC best-effort (`ZSTD` vs `ZSTD(1)` → возможен идемпотентный «ложный» MODIFY, DDL виден в предпросмотре); `ADD INDEX` без авто-`MATERIALIZE` (историю оператор материализует сам). Исполнение по реальному CH — integration-гейт. **Багфикс (стенд, узел cdsac):** наш `MODIFY TTL … INTERVAL N DAY` ClickHouse хранит и отдаёт в `SHOW CREATE` как `toIntervalDay(N)`; интроспектор ловил только форму `INTERVAL N DAY` → `HasTTL=false` → планировщик предлагал тот же `MODIFY TTL` по кругу (Apply срабатывал, но retention «не применялся»). `ttlDaysRe` расширен на обе формы, вынесен хелпер `ttlDaysFromCreate` + тест `TestTTLDaysFromCreate` ([ch_schema_inspector.go](../internal/web/adapter/out/clickhouse/ch_schema_inspector.go)). Заодно предупреждение C.1 в форме узла теперь отсылает к кнопке «Синхронизировать схему», а не к «ALTER вручную» ([locales](../web-ui/src/locales/ru.json)). **UX save-first:** план строится по СОХРАНЁННОМУ узлу (эндпоинт `/plan` берёт только id, форма туда не уходит), поэтому при несохранённых CH-правках (шаблон/таблица/retention) кнопка синхронизации сперва предлагает «Сохранить и синхронизировать» (`chSyncFormDirty` + `useConfirm`; согласие → save без navigate → открыть диалог; отказ → не продолжать). Тест `chSyncFormDirty` + браузерный прогон обоих путей |
| **§59: мини-графики в таблице узлов, «Перенести» в форме узла, команда+заголовки в конфиге, копирование URL лога** | ✅ §59 | ТЗ [59-nodes-ui-charts-config.md](sections/59-nodes-ui-charts-config.md), ветка `feature/nodes-ui-charts-config` (frontend-only, Go-код не менялся). **59.1**: последняя колонка таблицы узлов (кнопка «Перенести») заменена мини-графиком — тот же `Sparkline`, что в карточках, на тех же данных `spark` из `/api/metrics/nodes` (**новых запросов нет**), с тултипом по бакету и кликом → логи узла за окно бакета; колонка фиксирована `w-[140px]` (бары `flex-1` иначе съедали бы остаток строки); i18n `overview.table.traffic` ([Overview.tsx](../web-ui/src/pages/Overview.tsx)). **59.2**: кнопка «Перенести» убрана из обоих представлений списка и переехала в форму правки узла между «Отмена» и «Поделиться» (admin-гейт `useRoleAtLeast("admin")` сохранён — эндпоинт `/move` admin-only); локальный `MoveNodeDialog` **извлечён** из Overview.tsx в [components/node/MoveNodeDialog.tsx](../web-ui/src/components/node/MoveNodeDialog.tsx) с новым пропом `onMoved` — после переноса узел уходит в чужую команду и форма правки становится 404, поэтому `navigate("/")` ([NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)). Кеш узла чистится **`removeQueries`, а не `invalidateQueries`**: инвалидация запускала рефетч по team-scoped GET, который заведомо возвращал 404 (лишний запрос + ошибка в консоли — видно на стенде). **59.3 (проверка, кода нет)**: чтение логов при закрытии консоли/уходе со страницы — утечки нет: `refetchInterval` react-query привязан к живым наблюдателям, `refetchIntervalInBackground` в проекте не используется нигде, страница `/logs` и `{tab === "logs" && <LogsTab/>}` размонтируются, SSE-`EventSource` закрывается в cleanup ([Logs.tsx](../web-ui/src/pages/Logs.tsx), [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx)). Подтверждено замером на стенде (перехват сетевых запросов Playwright): вкладка логов узла — 4 запроса за 12 с, после ухода на «Конфиг» — **0 за 14 с**; страница `/logs` — 4 запроса за 9 с, после ухода на «Узлы» — **0 за 12 с**. **59.4/59.5**: на read-only вкладку «Конфиг узла» добавлены команда-владелец и проброс заголовков ([ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx)); `team_id` бэкенд отдавал давно (`NodeResponse`), но фронтовый тип `Node` его не объявлял — добавлен в [client.ts](../web-ui/src/api/client.ts), **имя команды резолвится по членствам** `useMyTeams()` (эндпоинт узла отдаёт только UUID; запрос общий с шапкой — react-query дедуплицирует ключ `me-teams`), не найдено (чужая команда у админа) → показываем сам id; заголовки — чипами, пустой список → «—»; i18n `node.fields.team`. **59.6**: URL в таблице логов обрезается по ширине колонки, а нативный `title` не давал скопировать полный адрес → компонент `LogUrlCell` ([LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx)): при наведении всплывает полный URL + `CopyButton` (`label` = «Скопировать»). **Грабли №1 (первая реализация была неверной, поймано стендом):** на Radix-`Tooltip` фича не работает — строки плотные, ячейка URL есть у каждой, по дороге к кнопке курсор проходит над соседней строкой, та перехватывает hover, тултип закрывается и клик приходится по строке (проверено движением мыши через Playwright: запись раскрывалась `rowsAfter=51`, буфер пустой). Переделано на **управляемый `Popover`**: `open` в стейте, `onMouseEnter` открывает, `onMouseLeave` гасит через `URL_POPOVER_CLOSE_DELAY_MS=250` (хватает пройти зазор `sideOffset=4`), соседние строки его не закрывают; `PopoverAnchor`, **а не `PopoverTrigger`** — у trigger свой `onClick`, который перехватил бы раскрытие записи; `onOpenAutoFocus`/`onCloseAutoFocus` → `preventDefault` (hover не должен уводить фокус и дёргать скролл длинного списка); таймер чистится в `useEffect`-cleanup. **Грабли №2:** Radix рендерит контент в портал, но **React-события всплывают по React-дереву**, поэтому без `stopPropagation` клик по кнопке доходил бы до `onClick` строки. После фикса проверено: буфер = полный URL, `rowsBefore == rowsAfter` (строка не раскрылась), клик по самому URL по-прежнему раскрывает и сворачивает запись. Бандл пересобран → `internal/web/static/` (старые хешированные ассеты удалены) |
| **§60: перенос узла не утаскивает общую таблицу логов** | ✅ §60 | ТЗ [60-node-move-shared-table.md](sections/60-node-move-shared-table.md), ветка `feature/nodes-ui-charts-config`. **Дефект (найден пользователем на стенде):** `Move` звал `RENAME TABLE` безусловно ([node.go](../internal/web/usecase/node.go) `Move`), хотя `clickhouse_table` штатно делит семейство узлов сервиса → перенос ОДНОГО узла уносил общую таблицу в БД целевой команды, остальные оставались со ссылкой на несуществующую и **логирование у них ломалось молча** (наружу только фоновое `code: 60, Unknown table expression identifier`). Масштаб на стенде: перенос 1 узла из 176 на `nexus_default.loadtest` обезглавил 175. **Порт** [port.NodeTableUsage](../internal/web/usecase/port/node_repo.go) — отдельный малый интерфейс (ISP: расширение `NodeRepo` сломало бы все стабы unit-тестов), реализация `CountByCHTable` на `NodeRepoPg` ([node_repo.go](../internal/web/adapter/out/postgres/node_repo.go)) — **без team-scope**: «соседи» после переносов закономерно в разных командах. Инъекция сеттером `SetTableUsage` (образец `SetInvalidationPublisher` — позиционный параметр затронул бы ~16 вызовов `NewNodeUsecase`), wiring в [app.go](../internal/web/app.go). **Логика** вынесена в `relocateCHTable`: общая таблица остаётся на месте (история принадлежит всему семейству), переехавшему узлу создаётся своя через существующий `provisionTable`; личная — переезжает `RENAME`, как раньше. Попутно `ErrTargetTableExists` ([team_provisioner.go](../internal/web/adapter/out/clickhouse/team_provisioner.go) — симметричная проверка цели): занятое имя в целевой БД роняло `RENAME`, теперь мягкий пропуск. **Деградация:** порт не подключён / запрос упал → прежнее поведение (`RENAME`), т.к. оставить узел без таблицы хуже; сбой создания новой таблицы не отменяет перенос (PG авторитетна). **Тесты:** таблица `TestNodeUC_Move_CHTable` (6 кейсов: личная/общая/нет источника/занято имя/сбой подсчёта/нет порта) + `TestNodeUC_Move_SharedTable_ProvisionFails` ([node_move_test.go](../internal/web/usecase/node_move_test.go)) — тестов на `Move` до этого не было вовсе; red-green проверен заглушкой `countCHTableSiblings→0` (кейс общей таблицы падает на старом поведении). Integration `TestNodeRepoCountByCHTable_E2E` ([node_table_usage_test.go](../tests/integration/node_table_usage_test.go)) — 5 кейсов + неизвестный UUID в exclude (`::uuid`-каст). **UI:** hint диалога переноса врал («таблица переедет») — переписан под фактическое правило ([locales](../web-ui/src/locales/ru.json) `overview.move.hint`). **Восстановление стенда** прошло через сам фикс: первый узел (таблицу делил ещё один) → «shared, keeping it in place» + новая таблица; второй → «target CH table exists, skip rename»; 176 узлов снова согласованы, стартовый WARN исчез. История 623 972 строк осталась в `nexus_rtt.loadtest` (orphan — виден в разделе UI «Orphans») |
| **§61: атрибуция записей на общей таблице логов + предпросмотр переноса** | ✅ §61 | ТЗ [61-log-attribution-and-move-preview.md](sections/61-log-attribution-and-move-preview.md), ветка `feature/nodes-ui-charts-config`. Продолжение §60 — два следствия общей таблицы, найденные пользователем на стенде. **61.1 (изоляция):** фильтр `node_id = <id> OR node_id = ''` ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)) засчитывал записи без идентификатора ЛЮБОМУ узлу таблицы → после переноса у узла «появились» 590 201 чужая строка (из них своих 1611). Теперь `nodeFilter` — метод: на личной таблице послабление остаётся (legacy-записи неразличимы, но других владельцев нет), на ОБЩЕЙ — строго `node_id = ?`; статус неизвестен → прежнее мягкое правило (скрыть свои логи хуже, чем показать лишние). **Правка 1.22.4:** §70.4 добавил второе основание для строгости («таблица не наша»), и оно спрятало ВСЕ логи внешней таблицы §64 — у неё маркера владения быть не может, а весь её поток идёт с пустым `node_id`. Теперь на `external_table` отсутствие владения строгость не включает, общая таблица остаётся строгой; кеш из карты счётчиков стал единым снимком `tableFacts` (счётчики + внешние таблицы, один цикл, применяется целиком). **Правка 1.22.5:** послабление снято совсем — фильтр строгий везде, кроме одиночной внешней таблицы §64; владение на выбор фильтра не влияет (`ownsTable` удалён, `OwnsTable` ушёл из интерфейса `Ownership` веб-слоя по ISP). Основание — замер: мягкий и строгий варианты читают одинаковый объём. Разбор и числа — §4.62. Свободная `failedConds` стала методом по той же причине. **61.2 (цена — отдельно проверялась):** источник — `CountsByCHTable` (один `GROUP BY` на инсталляцию) + кеш в адаптере: `usageCacheTTL=1m`, обновление **вне мьютекса** и только в одной горутине (`usageInflight`) — иначе 12 параллельных `NodeKPI` дашборда встали бы в очередь на время PG-запроса; `usageRetryInterval=15s` после неудачи — **без него лежащий PostgreSQL получал бы запрос на КАЖДОЕ обращение к логам** (сбой усиливался бы трафиком UI) плюс строку в лог; `context.WithoutCancel` + `usageQueryTimeout=3s` — иначе короткие запросы UI отменяли бы обновление карты. Тесты цены: `TestNodeFilter_CacheHitCost` (200 параллельных вызовов → 1 запрос), `TestNodeFilter_FailureDoesNotStorm` (50 вызовов при лежащей БД → 1 запрос), `TestNodeFilter_SingleFlight` (вызов не блокируется на время запроса), `TestNodeFilter_SurvivesCallerCancel` ([log_reader_attribution_test.go](../internal/web/adapter/out/clickhouse/log_reader_attribution_test.go)). **61.3 (предпросмотр):** `GET /api/nodes/:id/move-preview?target_team_slug=` (admin, [node_handler.go](../internal/web/adapter/in/http/node_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go)) → `usecase.MovePreview` ([node.go](../internal/web/usecase/node.go)): `table_shared`/`shared_with` + `target_table_exists`/`target_table`; проверки best-effort (CH недоступен → диалог всё равно открывается). Порт `TeamProvisioner.TableExists` ([team_provisioner.go](../internal/web/adapter/out/clickhouse/team_provisioner.go)) — заодно переиспользован в `RenameTable` вместо двух дублей запроса. Сам Move пишет Warn, когда узел подключился к существующей таблице (`warnIfTargetTableExists`) — раньше это был молчаливый no-op `CREATE TABLE IF NOT EXISTS`, из-за которого и «появились счётчики». UI: [MoveNodeDialog.tsx](../web-ui/src/components/node/MoveNodeDialog.tsx) запрашивает предпросмотр после выбора команды и показывает предупреждение только для неочевидных исходов; ключи `overview.move.preview.*` (ru/en). Тесты: `TestNodeUC_MovePreview` (3 кейса + «предпросмотр ничего не меняет»), `TestNodeUC_MovePreview_SameTeam`, `TestNodeUC_Move_SharedTable_TargetExists`. Swagger перегенерирован, бандл пересобран |
| **§62: глобальный поиск узлов + история поиска** | ✅ §62 | ТЗ [62-global-node-search.md](sections/62-global-node-search.md), ветка `feature/global-node-search`. **62.1 (история, PG):** миграция [0025_user_search_history](../migrations/0025_user_search_history.up.sql) (per-user, `PK (user_id, query)`, FK на `users` ON DELETE CASCADE, `CHECK` длины 1..200); порт [search_history_repo.go](../internal/web/usecase/port/search_history_repo.go) (малый ISP, образец §49 `FavoriteTeamRepo`), импл на `UserRepoPg` ([user_search_history.go](../internal/web/adapter/out/postgres/user_search_history.go)) — `SaveSearchQuery` = upsert (`ON CONFLICT DO UPDATE searched_at`) + обрезка до keep свежих в одной транзакции; integration [search_history_repo_test.go](../tests/integration/search_history_repo_test.go) (upsert/cap 10/изоляция per-user/clear/каскад). **62.2 (usecase+API истории):** `AuthUsecase.WithSearchHistory` + `SearchHistory`/`RecordSearch`/`ClearSearchHistory` ([auth.go](../internal/web/usecase/auth.go)) — чтение никогда не ошибка (деградация в `[]`, как `FavoriteTeamIDs`), запись нормализует (trim, границы 2..200 рун) и молча отсеивает мусор (no-op, не ошибка); handlers ([auth_handler.go](../internal/web/adapter/in/http/auth_handler.go)) + маршруты `GET/POST/DELETE /api/me/search-history` (`RequireSessionOnly`, `user_id` из сессии, [routes.go](../internal/web/adapter/in/http/routes.go)); wiring [app.go](../internal/web/app.go); unit [auth_search_history_test.go](../internal/web/usecase/auth_search_history_test.go). **62.3 (кросс-командный поиск):** `ListNodesFilter.TeamIDs` → `NodeRepo.List` фильтрует `team_id = ANY($1)` при непустом наборе (интерфейс `NodeRepo` не меняется — стабы целы), [port/node_repo.go](../internal/web/usecase/port/node_repo.go)/[node_repo.go](../internal/web/adapter/out/postgres/node_repo.go); `NodeUsecase.SearchAcrossTeams` ([node.go](../internal/web/usecase/node.go)) — обход членств (как §58 `ResolveTeam`), ILIKE по `path`/`target_url`, обогащение имён команд из `ListUserTeams` (без лишних запросов), лимит 20/max 50, `q < 2` рун → пусто; handler + DTO `NodeSearchItem` ([node_handler.go](../internal/web/adapter/in/http/node_handler.go)), маршрут `GET /api/search/nodes` (`RequireSessionOnly`; отдельный префикс из-за конфликта static-сегмента с wildcard `:id` в gin); unit [node_search_test.go](../internal/web/usecase/node_search_test.go) + integration [node_search_test.go](../tests/integration/node_search_test.go) (A/B/C, член A+B → только A+B, ILIKE по path и target_url, лимит). **62.4 (GlobalSearch):** [lib/searchHistory.ts](../web-ui/src/lib/searchHistory.ts) (хуки истории/поиска + persistence `q` в `sessionStorage` `nexus.globalsearch.q`); [GlobalSearch.tsx](../web-ui/src/components/GlobalSearch.tsx) (поле в шапке — cmdk-в-popover через `PopoverAnchor`, debounce 300мс, история при пустом вводе / результаты с бейджем команды; выбор → `navigate(/nodes/:id)`, команду переключает `useEnsureNodeTeam` §58; `q` не очищается; хоткей `/` или `Ctrl/⌘+K`); врезка в [Topbar.tsx](../web-ui/src/components/Topbar.tsx); `node-search`/`search-history` в `TEAM_INDEPENDENT_KEYS` ([teams.ts](../web-ui/src/lib/teams.ts)); i18n namespace `search` (en/ru). **62.5 (история в поле Overview):** [SearchHistoryList.tsx](../web-ui/src/components/SearchHistoryList.tsx) (простые кнопки, общий источник с шапкой), поле «Поиск» обёрнуто в `Popover`/`PopoverAnchor` ([Overview.tsx](../web-ui/src/pages/Overview.tsx)) — открытие по фокусу при непустой истории, выбор → `setSearchInput` (debounce §54 коммитит в URL, `saveFilters`→`setParams` не тронут); запись по Enter и blur непустого (`pickingHistory` гасит запись на blur при клике по пункту). Неочевидности — §4.40. Swagger перегенерирован; бандл пересобран → `internal/web/static/`. Гейты `-race`/`make test-integration`/браузерный стенд — финальный прогон |
| **§63: автор создания и последнего изменения узла** | ✅ §63 | ТЗ [63-node-author.md](sections/63-node-author.md), ветка `feature/global-node-search`. **63.1 (модель+repo):** миграция [0026_node_author](../migrations/0026_node_author.up.sql) — колонки `nodes.created_by`/`updated_by` (`VARCHAR(255) NOT NULL DEFAULT ''`, образец `node_allowed_hosts.created_by`, без FK/джойна); поля `domain.Node.CreatedBy`/`UpdatedBy`; [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go) — `nodeColumns`/`scan`/`Create` INSERT/`Update` SET (+ `updated_by = $50`), сигнатура `UpdateAllowedHostsSnapshot` расширена `updatedBy` (**интерфейсная правка** `port.NodeRepo` → 5 тестовых стабов). Проброс `Actor.UserLogin` во всех путях: `Create`→`created_by`, `Update`/`SetStatus`/`Move`→`updated_by`, `Copy`→клон.`created_by`=копировщик ([node.go](../internal/web/usecase/node.go), [node_copy.go](../internal/web/usecase/node_copy.go): `cloneNodeForCopy` сбрасывает автора источника), allowlist-snapshot ([host_allowlist.go](../internal/web/usecase/host_allowlist.go) `mutateLink`). Integration [node_author_test.go](../tests/integration/node_author_test.go) (create/update/status/copy) + unit [node_author_test.go](../internal/web/usecase/node_author_test.go). **63.2 (API):** `created_by`/`updated_by` в `NodeResponse` + `nodeToResponse` ([dto.go](../internal/web/adapter/in/http/dto.go)); swagger. **63.3 (UI):** тип `Node.created_by`/`updated_by` ([client.ts](../web-ui/src/api/client.ts)); [ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx) — строка «Создано» (всегда) + «Обновлено» (только `updated_at !== created_at`), хелпер `DateWithAuthor`, автор моно, пустой автор → подпись «Автор» не выводится (только дата); i18n `common.created_at`/`common.author` (ru/en). Неочевидности — §4.41. Бандл пересобран → `internal/web/static/`. Гейты `-race`/`make test-integration`/браузерный стенд — финальный прогон |
| **§64: ручная (внешняя) таблица логов ClickHouse** | ✅ §64 | ТЗ [64-manual-external-table.md](sections/64-manual-external-table.md), ветка `feature/manual-external-table`. Сценарий «Nexus как фронт логирования»: в CH-таблицу пишет посторонний сервис, Nexus только читает. **64.1 (модель):** миграция [0027_node_external_table](../migrations/0027_node_external_table.up.sql) — колонка `nodes.external_table` (`BOOLEAN NOT NULL DEFAULT false`) + разовый UPDATE, проставляющий дефолтный шаблон legacy-узлам с пустым `clickhouse_template_id` (иначе смена семантики молча выключила бы им retention; down не реверсит — до-миграционное состояние невосстановимо); поле `domain.Node.ExternalTable` + валидация несовместимости с `ClickHouseTemplateID` (`ErrNodeExternalTableTemplateConflict` → i18n `node.validation.external_table_conflict`), `ErrNodeExternalTable` для операций управления таблицей; [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go) (`nodeColumns`/`scan`/INSERT/UPDATE), `external_table` в `CreateNodeRequest`/`NodeResponse` ([dto.go](../internal/web/adapter/in/http/dto.go)) и в `diffNodes` (аудит). Копия узла наследует флаг (`clone := *src`) — тест `TestNodeUC_Copy_KeepsExternalTable`. **64.2 (гейты, 4 точки; пятая — «чтение не гейтится владением», добавлена в 1.22.4 по боевому инциденту, см. §4.62):** `provisionTable` ([node.go](../internal/web/usecase/node.go)) — ранний no-op + Debug (кроет Create/Update/Copy/Move); **перенос узла НЕ ребейзит имя внешней таблицы** — новый хелпер `targetCHTable` вместо `rebaseCHTable` в `Move` и `MovePreview` (ребейз увёл бы узел на несуществующее имя в БД целевой команды, а таблица осталась бы без читателя; red-green проверен); `AND NOT external_table` в трёх SQL — `ListClickHouseTables` Sender'а и Web'а (стартовые ALTER'ы/backfill) и `ListForHousekeeping` ([nodepg/reader.go](../internal/sender/adapter/out/nodepg/reader.go), [node_repo.go](../internal/web/adapter/out/postgres/node_repo.go)); фильтр на уровне УЗЛА, а не таблицы — общая таблица остаётся под управлением, если на неё ссылается хотя бы один не-внешний узел; §56 `plan()` ([ch_schema_sync.go](../internal/web/usecase/ch_schema_sync.go)) → `ErrNodeExternalTable` → 409 `ch_sync.external_table_forbidden` ([ch_schema_handler.go](../internal/web/adapter/in/http/ch_schema_handler.go)). Тесты: `TestNodeUC_{Create,Update}_ExternalTable_SkipsProvision`, `TestNodeUC_Move_ExternalTable`, `TestCHSchemaSync_ExternalTable_Rejected` (все красные с отключённым гейтом), integration `TestNodeRepo_ListClickHouseTables_AllTeams_E2E` дополнен внешним узлом и смешанной таблицей. **64.3 (дефолт лимита тела):** конфиг-ключ `web.node_default_max_body_size` (дефолт 50000, валидация 1..10⁷ — те же границы, что у `domain.Node.MaxBodySize`, иначе форма подставила бы непроходящее значение) — [config.go](../internal/platform/config/config.go)/[defaults.go](../internal/platform/config/defaults.go)/[validate.go](../internal/platform/config/validate.go) + `config.example.yml`/`config_debug.yml`. Фронту отдаётся через УЖЕ существующий `GET /api/settings/public` (`node_default_max_body_size` в `PublicSettingsResponse`, [app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go)): эндпоинт авторизованный, фронт кэширует его под team-независимым ключом `public-settings` — новый роут не понадобился. Значение из конфига, а не из `app_settings`: это параметр развёртывания, а не переключатель UI. Тест `TestAppSettingsHandler_GetPublic_NodeDefaultMaxBodySize` (значение 12345 ≠ дефолта — ловит захардкоженное). **64.4 (кнопка «Проверить»):** `POST /api/ch-tables/verify` `{table}` (manager+, `RequireSessionOnly`) — по ИМЕНИ таблицы, а не по id узла, чтобы кнопка работала в форме ещё не сохранённого узла. Чистое ядро — [domain/ch_log_verify.go](../internal/domain/ch_log_verify.go) `VerifyLogTableColumns` против `RequiredLogColumns`: **лишние колонки допускаются** (INSERT/SELECT перечисляют колонки явно, чужие поля писателя не мешают), нормализация типов убирает пробелы и параметр таймзоны `DateTime('UTC')`→`DateTime` (бинарно совместимы), а `Nullable(...)`/`LowCardinality(...)`/`DateTime64` — честное расхождение с показом want/got. Новый метод адаптера `ReadTableColumns` (`system.columns` + `type`, отдельно от `ReadTableSchema` §56, которому нужны CODEC/индексы/TTL) — [ch_schema_inspector.go](../internal/web/adapter/out/clickhouse/ch_schema_inspector.go); usecase [ch_table_verify.go](../internal/web/usecase/ch_table_verify.go) с узким consumer-side интерфейсом `tableColumnsReader` (порт §56 не расширяли — ISP). Отсутствие таблицы и расхождения — **200 с `ok=false`**, а не ошибка запроса; 400 на кривое имя (до ClickHouse не доходит), 503 без CH. Wiring: инспектор создаётся один раз и отдаётся обоим usecase; ветка `else` передаёт явный nil — **typed-nil дал бы панику вместо 503**. Тесты: 10 табличных кейсов домена, 5 unit'ов usecase, integration `TestCHTableVerify_E2E` (6 сценариев на реальном CH, включая таблицу в чужой БД). **64.5 (UI):** [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) — три дефолта формы СОЗДАНИЯ подставляются асинхронно (шаблон `is_default`, редактируемый префикс `<ch_database>.` из [useCurrentTeamCHDatabase](../web-ui/src/lib/teams.ts), включённый лимит тела из [useNodeFormDefaults](../web-ui/src/lib/nodeDefaults.ts)), каждый через guard-ref «один раз и только пока поле в исходном значении» — **медленный ответ иначе затёр бы ввод оператора**; выбор шаблона снимает `external_table`, возврат к «(нет / ручная таблица)» ставит его обратно (типовой сценарий ручной таблицы — посторонний писатель); чекбокс «Внешняя таблица» виден только при пустом шаблоне; Retention скрывается при `external_table` (housekeeping для неё отключён — поле обещало бы несуществующее поведение); кнопка §56 дополнительно гейтится `!external_table`; кнопка «Проверить» у поля таблицы (mutation → `/api/ch-tables/verify`, результат сбрасывается при правке имени/шаблона). Текст результата — чистая [buildVerifyMessage](../web-ui/src/lib/chTableVerify.ts) (ошибка вызова важнее результата; missing и mismatched показываются вместе, тип как `got → want`). `external_table` добавлен в `chSyncFormDirty` (переключение галки — несохранённая CH-правка) и в клиентскую валидацию конфликта с шаблоном. i18n ru+en: `node.verify.*`, `node.fields.external_table`, `node.help.external_table`, `node.validation.external_table_conflict`, `ch_sync.external_table_forbidden`. Тесты: 7 кейсов `buildVerifyMessage`, +3 `nodeValidation`, +1 `chSyncFormDirty`; vitest 168 ✅, lint `--max-warnings=0` ✅. Бандл пересобран → `internal/web/static/` (старые хешированные ассеты удалены) |
| **§65: команда узла на форме + представление «только имя»** | ✅ §65 | ТЗ [65-node-form-team-display.md](sections/65-node-form-team-display.md), ветка `feature/ui-team-display-and-fixes`. Форма узла ([NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)) — read-only строка `Команда: <имя>` вверху правой колонки: правка — `useNodeTeam(id).team_name` (резолвер §58, ключ уже закеширован `useEnsureNodeTeam` — второго запроса нет), создание — имя текущей команды из `useMyTeams()`. Вкладка «Конфиг» ([ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx)) — только имя (убраны `(slug)` и UUID-фолбэк; недоступно → «—», фолбэк членства → резолвер §58). Ключ i18n `node.fields.team` существовал. Неочевидности — §4.45. Бандл пересобран |
| **§66: отображаемое имя пользователя** | ✅ §66 | ТЗ [66-user-display-name.md](sections/66-user-display-name.md), ветка `feature/ui-team-display-and-fixes`. Миграция [0028_user_name](../migrations/0028_user_name.up.sql) (`users.name` + backfill `name = login`); `domain.User.Name`/`Session.Name` + `DisplayName()` (фолбэк логин); [user_repo.go](../internal/web/adapter/out/postgres/user_repo.go) (cols/scan/insert/update + ILIKE по name, страховка `COALESCE(NULLIF(name,''), login)`); `name` обязателен в create/update DTO ([user_handler.go](../internal/web/adapter/in/http/user_handler.go)), отдаётся в `userResponse`/`meResponse` ([auth_handler.go](../internal/web/adapter/in/http/auth_handler.go))/`teamMemberResponse` ([team_handler.go](../internal/web/adapter/in/http/team_handler.go), JOIN в [team_repo.go](../internal/web/adapter/out/postgres/team_repo.go)); `Actor.UserLogin = Session.DisplayName()` в `actorFromCtx`/`userActor` — снапшоты §63 и аудит пишутся именем (старые записи = логин = имени на момент миграции); след переименования в аудите (`name`+`prev_name`). UI: [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx) (чип), [Users.tsx](../web-ui/src/pages/settings/Users.tsx) (имя основное + логин вторично, поле «Имя», toggleActive шлёт name — PUT без него 400), [Teams.tsx](../web-ui/src/pages/settings/Teams.tsx) (участники/кандидаты). Тесты: `TestSession_DisplayName`/`TestUser_DisplayName` (domain), `TestUserUC_Update_Rename_AuditTrail`. Неочевидности — §4.46. Swagger перегенерирован, бандл пересобран |
| **§67: хост клиента в логах (reverse-DNS) + фильтр + счётчик «Всего»** | ✅ §67 | ТЗ [67-client-host-rdns.md](sections/67-client-host-rdns.md), ветка `feature/client-host-rdns`. **Колонка** `client_host String` **AFTER IP** в [RequiredLogColumns](../internal/domain/ch_log_schema.go) → CREATE новых таблиц/verify §64 автоматически; существующие — `EnsureClientHostColumn` §42.10 ([ensure_schema.go](../internal/platform/clickhouse/ensure_schema.go), вызовы в обоих app.go); insertSQL+Append писателя ([chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go)), selectCols/listCols/previewCols+сканы читателя ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)); clogwire прокидывает поле без правок (JSON по именам). **Резолвер** [platform/rdns](../internal/platform/rdns/resolver.go): порт `HostResolver` в sender/usecase (консьюмер-сайд, как CircuitBreaker) через functional option `WithHostResolver` — **45 колл-сайтов NewSendUsecase не тронуты**; `Lookup` = ноль I/O (L1 map+mutex, TTL ≤ 5 мин), промах → фон (safego + pending-дедуп + singleflight): Redis `nexus:rdns:<ip>` (`""` = негативный маркер, отличим от промаха по redis.Nil) → PTR-lookup с таймаутом → SET EX; fail-open всюду; `net.ParseIP` отсекает `rabbitmq://…` §27.10; `WithLookupFunc` — инъекция DNS для тестов. Сознательно НЕТ кросс-репличного дедупа и Prometheus-метрик (только Debug §51.9). Конфиг `sender.rdns` (Disabled-конвенция). DLQ `logTTLExpired` переиспользует резолвер через `r.send.hosts`. **Фильтр**: facet `GET /nodes/:id/logs/client-hosts` (копия Methods; кап 200), `LogQuery.ClientHost` в Search/Count/`matchLogFilter`; **`isMissingColumnErr`** (CH-коды 10/16/47 + имя колонки) — внешняя таблица §64 без колонки даёт пустой facet + Debug вместо 500/Sentry; **деплой-окно (грабля, поймана стендом):** первоначальное решение «List/Search не обрабатываем» давало ПОЛЛИНГ-флуд 500/Sentry раз в ~5с до ручного ALTER — исправлено в `classifyCHErr`: отсутствие ИМЕННО `client_host` трактуется как `ErrLogsBackendUnavailable` → штатный баннер «Логи временно недоступны» на всех read-путях (другие missing-column остаются 500 — DDL-поломки не маскируются); INSERT буферизуется в `nexus.logs.retry` §38; verify → Missing — integration-тест с проверкой позиции AFTER IP по `system.columns`; полный цикл владельца (баннер → ALTER → чтение восстановилось без рестартов) проверен на стенде. UI [LogClientHostFilter.tsx](../web-ui/src/components/node/LogClientHostFilter.tsx) + ряд 2 фильтров на flex («Дата с»/«Дата по» с метками, ширина по контенту, кнопки в том же ряду — эскиз утверждён). **Счётчик «Показано N из M»**: `searchConds` вынесен из Search — единственный источник WHERE для Search и нового `Count` (BeforeID игнорируется, `countTimeout=10s` → ErrLogsBackendUnavailable); `GET /nodes/:id/logs/count` (400 на плохой q, writeFacetError-деградации); UI — отдельный useQuery (queryKey списка + быстрые ok/err/done-фильтры), пересчёт со снапшотом, в Live скрыт, недоступен → прежнее «Показано N». DTO записи лога и колонка таблицы UI — сознательно НЕ добавлены. Swagger перегенерирован, бандл пересобран |
| **§68: поддержка multipart/form-data** | ✅ §68 | ТЗ [68-multipart-form-data.md](sections/68-multipart-form-data.md), ветка `feature/multipart-form-data`. **Проброска (кода нет)** — тело шина везде несёт как непрозрачные `[]byte`, `Content-Type` c `boundary` пробрасывается дословно (sync gRPC, async JSON-конверт с base64-телом, RabbitMQAsync-puller, dry-run); зафиксировано матрицей §68.4 и тестами. **Размер (кода нет)** — `request_size`/`response_size` §42.10 уже по полному телу до усечения (вложения учтены). **68.1 (домен)** — [domain/multipart.go](../internal/domain/multipart.go): `IsMultipartMediaType` (`mime.ParseMediaType`+prefix), `MultipartLogPlaceholder(ct, body)` (первая строка media type, сводка частей потоково `io.Copy(io.Discard)` — имя/`filename`/тип/размер БЕЗ содержимого; потолки `multipartMaxListedParts=50`/`multipartScanCap=1000`; fallback при неразобранном теле, первой строкой ВСЕГДА валидный multipart media type — на него опирается детект), `IsMultipartLogPlaceholder` (структурный детект: 1-я строка multipart/*, 2-я начинается с `- part` или `[`); table-тесты + `FuzzMultipartLogPlaceholder`. **68.2 (Sender)** — [send.go](../internal/sender/usecase/send.go): приватный `logBodyCopy(kind, contentType, body, in)` — multipart → плейсхолдер (без `truncateRunes`, debug §51.9), иначе truncate как раньше; подключён к запросу (по `in.Headers`) и ответу (по `resp.Headers`, симметрия §68); `headerGet` — регистронезависимый lookup; checksum/размеры/тело к узлу/ответ клиенту не тронуты; гейты логирования сохранены; §38 Kafka-retry получает плейсхолдер автоматически (запечён в LogRecord). **Схема CH и миграции НЕ трогаются.** Тесты `send_test.go` (плейсхолдер+полный размер/checksum+проброска байт-в-байт; освобождение от `max_body_size`; `LogRequestBody=false`→пусто; lowercase заголовок; multipart-ответ; регресс не-multipart; `stubHTTPCaller` дополнен захватом тела). **68.3 (replay)** — [replay.go](../internal/web/usecase/replay.go) `ErrReplayBodyMultipart` + проверка в `replayOne` (nil-override + `IsMultipartLogPlaceholder` → отказ до диспатча); [replay_handler.go](../internal/web/adapter/in/http/replay_handler.go) → 422 `replay.multipart_unavailable`; [i18n.go](../internal/platform/i18n/i18n.go) en+ru; ручной `BodyOverride` разрешён; тесты блокировка/override. **68.4 (UI)** — [ReplayDialog.tsx](../web-ui/src/components/ReplayDialog.tsx): TS-зеркало детекта → warn-подсказка `replay.multipart_warning` (en+ru) + дизейбл кнопки пока тело пусто; бандл пересобран → `internal/web/static/`. **Решения:** multipart освобождён от `max_body_size` (тело в CH не хранится; действуют транспортные капы `receiver.max_body_bytes` 32 МиБ / Kafka 64 МиБ base64 +33%); правило симметрично для ответов `multipart/*`. **Ограничение:** записи до §68 (сырое тело) детект не ловит. Неочевидности — §4.49. Гейты `-race`/`make test-integration`/браузерный стенд — финальный прогон |
| **§69: вкладка «Очередь» для всех узлов, схема target_url, пересборка адреса доставки** | ✅ §69 | ТЗ [69-queue-tab-and-target-url.md](sections/69-queue-tab-and-target-url.md), ветка `feature/queue-tab-all-nodes`. Разбор боевого инцидента 2026-07-27 (узел `telephony/lenobl/stat`, команда vika: `url_mode=static` + `target_url` без схемы). **69.1/69.2 (валидация, бэкенд+фронт):** новый хелпер [domain/url.go](../internal/domain/url.go) `AbsoluteHTTPURL` — единая проверка «схема http(s) + непустой host» (`url.Parse` БЕЗ схемы ошибки не возвращает, кладёт всю строку в `Path` — проверять `Scheme`/`Host` обязательно); `Validate()` ([node.go](../internal/domain/node.go)) отвергает битый `target_url` **только при `url_mode=static`** (при `from_request` поле скрыто на форме — ошибка на невидимом поле стала бы тупиком; pull-узлы нормализуются в static, покрыты), `SetDefaults()` делает `TrimSpace`; `ErrNodeTargetURLScheme` → таблица [node_validation.go](../internal/web/adapter/in/http/node_validation.go) → 400 + `code`/`field` (страж `TestNodeValidationCode_AllMapped` требует i18n на обоих языках); дедупликация — `ValidatePublicBaseURL` ([app_settings.go](../internal/domain/app_settings.go)) и ветка `from_request` резолвера ([urlresolver.go](../internal/receiver/usecase/urlresolver.go), где требование http(s) действовало и раньше — асимметрия со static закрыта) переведены на хелпер. Фронт — зеркало в [nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts) (`TARGET_URL_RE`, та же привязка к режиму) + усиленная подсказка `node.help.target_url` («адрес без схемы не сохранится»). **69.3 (адрес доставки):** `resolveTargetURL` в [async_envelope.go](../internal/sender/usecase/async_envelope.go) — для static собирает адрес заново по свежему конфигу (база без своей query + хвост `RequestPath` + query конверта) и вызывается из `buildSendInput`, поэтому чинит **все три пути разом**: основной consumer, redelivery delay-топика §3.6 и DLQ-репроцессор §36. Query берётся из конверта (там она уже слита с query запроса) → **ограничение: правка query внутри `target_url` на застрявшие сообщения не действует**, правка схемы/хоста/пути — действует; `from_request` не пересобирается (исходный url-параметр вырезан на приёме, `urlresolver.go`); любой сбой разбора → адрес из конверта (паттерн обратной совместимости `ReceivedAt`). Debug `logRebuiltTarget` при расхождении (URL без query). **69.4 (ранние выходы):** `usesAsyncQueue` в [async_queue.go](../internal/web/usecase/async_queue.go) — `List`/`purge` для sync-узла возвращают пустой результат без скана обоих топиков до `peekCap`, `PurgeFailed` пропускает tombstone'ы (DLQ-репроцессора у sync нет), `DeleteFailed` в CH не меняется. **69.1 (UI):** гейт вкладки снят ([NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx) — вкладка у всех типов), [QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx) — `isAsync = root_method !== "request"`: секция «Ожидают отправки» и peek-запрос скрыты для sync (иначе скан Kafka на каждое открытие), KPI-плитка pending не рендерится (сетка остаётся `cols=2`, чтобы одинокая плитка не растягивалась), sync-тексты баннера/подсказки/подтверждений (`banner.explain_sync`, `banner.state_paused_sync`, `failed.no_reprocess_hint`, `purge_failed_{all,period}_confirm_sync`). Секция «Неудачные доставки» (список, replay, «Повторить все сейчас», «Очистить») работает у всех типов — она про ClickHouse `done=0`, не про Kafka. Тесты: `TestNode_Validate_TargetURLScheme` (боевой случай + `//host`/`ftp`/относительный/пустой хост, остаточный target при from_request), `TestNode_SetDefaults_TrimsTargetURL`, `TestResolveTargetURL` (10 кейсов, включая «`../` не выводит за пределы базы» и отсутствие дублей query), `TestReprocess_RebuiltURL_DeliversAfterNodeFix` (регрессия инцидента целиком), `TestAsyncQueue_{List,Purge}_SyncNode_SkipsKafka`, `TestAsyncQueue_PurgeFailed_SyncNode_DeletesWithoutTombstones`, vitest §69.2. Без миграций и новых конфиг-ключей. Неочевидности — §4.50. Бандл пересобран → `internal/web/static/` |
| **§70: несколько нод Nexus на одном ClickHouse** | ✅ §70 | ТЗ — [70-multi-instance-clickhouse.md](sections/70-multi-instance-clickhouse.md), ветка `feature/multi-instance-clickhouse`. **Именование:** [domain/instance.go](../internal/domain/instance.go) (`InstanceID.CHDatabase` — единственная точка сборки имени БД; `CHDatabaseForSlug` оставлен обёрткой для пустого идентификатора), секция конфига `instance` ([config.go](../internal/platform/config/config.go)), `teamCHDatabasePattern` и CHECK `teams_ch_database_format` расширены до `{0,40}` (миграция [0029](../migrations/0029_instance_identity.up.sql)) — суффикс ноды съедает бюджет длины слага. **Идентичность:** [bootstrap/instance.go](../internal/platform/bootstrap/instance.go) — заявка `INSERT … ON CONFLICT DO NOTHING` (гонка трёх сервисов безопасна), расхождение с конфигом валит старт; признак первого запуска — `FreshPG` (нет узлов, команд не больше сидированной) **И** `ch_claimed=false` (миграция [0030](../migrations/0030_instance_ch_claimed.up.sql)). **Гейт живёт в [bootstrap/ch_ownership.go](../internal/platform/bootstrap/ch_ownership.go) и вызывается из [cmd/web/main.go](../cmd/web/main.go), а не из `App.Start`** — ошибку старта сервис-обёртка гасит логом, и в неинтерактивном режиме процесс остался бы жить. **Владение:** [platform/clickhouse/owner.go](../internal/platform/clickhouse/owner.go) — маркер `__nexus_owner` (`TinyLog`), атомарный захват `CREATE TABLE` без `IF NOT EXISTS` (код 57 у проигравшего), кеш вердиктов 5 мин / 30 с, сброс через `Manager.OnReload`. **Гейты:** провижинер, `ApplyAlter` §56, `DeleteFailed` и `nodeFilter` ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)), orphan-сканер, housekeeping Sender'а, стартовые ALTER'ы обоих сервисов, сохранение узла с чужой таблицей (400, кроме `external_table`). **Старт Web:** ребейз БД сидированной команды + `EnsureAll` ([web/app.go](../internal/web/app.go)). **Наблюдаемость:** тег Sentry `instance`, метка `nexus_instance`, атрибут логов, подпись Telegram. **UI:** `instance` в `/api/version`, `ch_database_prefix` в `/api/settings/public`, чип в шапке ([Topbar.tsx](../web-ui/src/components/Topbar.tsx)), хук [instance.ts](../web-ui/src/lib/instance.ts). Совместимость: при пустом `instance.id` не меняется ничего, кроме появления маркера в своих БД. Развёртывание — DEPLOYMENT.md §5А, неочевидности — §4.51. **Правка 1.22.4:** гейт в `nodeFilter` оказался слишком широк — на внешней таблице §64 (маркера владения у неё быть не может) он спрятал всё содержимое; там строгость больше не включается, разрушающие операции гейт держит по-прежнему. **Правка 1.22.5:** гейт вообще ушёл из read-path — атрибуция строгая по умолчанию, `OwnsTable` удалён из интерфейса `Ownership` веб-слоя (§4.62) |
| **§71: персональные предпочтения — дефолтный период рабочего стола per-team** | ✅ §71 | ТЗ — [71-user-preferences.md](sections/71-user-preferences.md), ветка `feature/user-preferences`. **Схема:** миграция [0031](../migrations/0031_user_preferences.up.sql) — `user_preferences(user_id, team_id NULL, key, value JSONB, updated_at)`, уникальный индекс по выражению `COALESCE(team_id, …)` (НЕ `UNIQUE NULLS NOT DISTINCT` — он требует PostgreSQL 15, а бой на 12) + два FK (составной на `user_teams` для инварианта «преф ⊆ членство», прямой на `users` для глобальных строк, которые составной FK по MATCH SIMPLE не трогает). **Домен:** [domain/user_preference.go](../internal/domain/user_preference.go) — `Validate()` проверяет только формат ключа и валидность/размер значения; семантику значения домен не знает намеренно (generic-хранилище). **Порт/репозиторий:** [port/user_preference_repo.go](../internal/web/usecase/port/user_preference_repo.go), [postgres/user_preferences.go](../internal/web/adapter/out/postgres/user_preferences.go) — upsert одним запросом с атомарной проверкой потолка (200/пользователь), `IS NOT DISTINCT FROM` для глобальных строк, `ON CONFLICT` по тому же выражению, что и индекс. **Usecase:** [usecase/preferences.go](../internal/web/usecase/preferences.go) — отдельный `PreferenceUsecase` с конструкторной инъекцией (не builder на `AuthUsecase`), чтение НИКОГДА не возвращает ошибку. **HTTP:** [preference_handler.go](../internal/web/adapter/in/http/preference_handler.go), `GET/PUT /api/me/prefs` с `RequireSessionOnly`. **Фронт:** [lib/prefs.ts](../web-ui/src/lib/prefs.ts) (`useTeamDefaultPeriod`, `useSetPref`, миграция старого ключа), [lib/period.ts](../web-ui/src/lib/period.ts) (`parsePeriodPref` вместо `load/saveDefaultPeriod`), [lib/overviewFilters.ts](../web-ui/src/lib/overviewFilters.ts) (дефолт параметром во всех функциях), [Overview.tsx](../web-ui/src/pages/Overview.tsx) (`periodReady` в трёх местах). Тесты: [prefs.test.tsx](../web-ui/src/lib/prefs.test.tsx), [Overview.period.test.tsx](../web-ui/src/pages/Overview.period.test.tsx) (регресс «после переключения команды — дефолт новой»), [user_prefs_repo_test.go](../tests/integration/user_prefs_repo_test.go). Развёртывание — DEPLOYMENT.md §8, неочевидности — §4.52 |
| **§72: серверная фильтрация логов узла** | ✅ §72 | ТЗ — [72-logs-server-side-filter.md](sections/72-logs-server-side-filter.md), ветка `feature/logs-server-side-filter`. Боевой инцидент 2026-07-29 (узел `task/task_vika`): фильтр «Ошибки» показывал «Показано 3 из 33», скролл не шёл — быстрые фильтры применялись в браузере к уже загруженной странице, счётчик «из M» считался сервером по всей таблице. **72.1 (семантика):** `condLogOK`/`condLogErr` в [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) (`ok = done=1 AND 200<=status<400`, `err = NOT ok` — полное дополнение), зеркало `logRecordOK` в [usecase/logs.go](../internal/web/usecase/logs.go), контракт `LogQuery.Status` в [port/log_reader.go](../internal/web/usecase/port/log_reader.go), `@Param status` + `make swagger`; до §72.1 запись с 3xx не проходила НИ ОДИН фильтр, а незавершённая с 2xx числилась успехом. **72.2 (фронт):** [lib/logsQuery.ts](../web-ui/src/lib/logsQuery.ts) — единственный сборщик параметров для `/logs`, `/logs/count` и SSE-стрима (+`isLogOK` для подсветки строк в [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx) и [OverviewTab.tsx](../web-ui/src/components/node/OverviewTab.tsx)); `status`/`done` в `queryKey`, клиентский пост-фильтр удалён, live-буфер чистится при смене фильтра. **72.3 (очередь):** общий хук [lib/useInfiniteLogs.ts](../web-ui/src/lib/useInfiniteLogs.ts) (курсор `to`+`before_id`, дедуп, потолок §44.K, догрузка по скроллу и при недоборе высоты) — им же питается секция «Неудачные доставки» [QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx), где стоял жёсткий `limit=50` без пагинации. **72.4 (ClickHouse):** `dateCreateConds`/`dayUTC` — то же окно по колонке PARTITION BY с запасом ±1 день, гейт `LogQuery.DateCreateAligned` (выключен для внешних таблиц §64), выдаётся в `applyDateCreateAligned`; замер 200 000 строк — 200 000 прочитанных строк и 3 части против 8 192 и 1 части. **72.5:** возврат к верху схлопывает накопленные страницы. Тесты: `TestSearchConds_{StatusDone,StatusComplement,TimeWindow}`, `TestMatchLogFilter_StatusComplement`, `TestLogs_DateCreateAligned_GatedByExternalTable`, [log_status_filter_test.go](../tests/integration/log_status_filter_test.go), [log_partition_pruning_test.go](../tests/integration/log_partition_pruning_test.go), [logsQuery.test.ts](../web-ui/src/lib/logsQuery.test.ts), [LogsTab.filter.test.tsx](../web-ui/src/components/node/LogsTab.filter.test.tsx), [QueueTab.failed.test.tsx](../web-ui/src/components/node/QueueTab.failed.test.tsx). Без миграций, ENV-ключей и i18n-ключей. Неочевидности — §4.53. Бандл пересобран → `internal/web/static/` |
| **§73: реестр инстансов в интерфейсе** | ✅ §73 | ТЗ — [73-instances-registry.md](sections/73-instances-registry.md), ветка `feature/instances-registry`. §70 ввёл понятие инстанса, но оставил его невидимым: нода знает только себя (`instance_identity` — синглтон). Вкладка **Настройки → Инстансы** (`/settings/instances`, admin-only) показывает соседние развёртывания: название, адрес-гиперссылка (`target=_blank` + `rel=noopener noreferrer`), код инстанса, версия, статус. **Токен не нужен и на удалённой стороне менять нечего** — версия и активность снимаются с уже публичных `GET /api/version` и `GET /ready`, опрашиваемых параллельно; опрашивает сервер, а не браузер (CORS не настроен, CSP `connect-src 'self'`, плюс снимается mixed-content). Таблица `peer_instances` (миграция 0032) — отдельно от `instance_identity`, без `team_id`, уникальность по `lower(base_url)`, кеш последней пробы для мгновенной отрисовки. Семь admin-only маршрутов под session-cookie; недоступность соседа — его статус, а не ошибка запроса (200). Файлы: [domain/peer_instance.go](../internal/domain/peer_instance.go), [usecase/peer_instance.go](../internal/web/usecase/peer_instance.go), [adapter/out/instanceprobe/prober.go](../internal/web/adapter/out/instanceprobe/prober.go), [adapter/out/postgres/peer_instance_repo.go](../internal/web/adapter/out/postgres/peer_instance_repo.go), [adapter/in/http/peer_instance_handler.go](../internal/web/adapter/in/http/peer_instance_handler.go), [web-ui/src/pages/settings/Instances.tsx](../web-ui/src/pages/settings/Instances.tsx). Два конфиг-ключа: `web.instance_probe_timeout_ms` (3000), `web.instance_probe_rate_limit_per_min` (30) |
| **§74: безопасный откат на предыдущую версию** | ✅ §74 | ТЗ — [74-safe-rollback.md](sections/74-safe-rollback.md), ветка `feature/safe-rollback`. Откат кода упирался в стартовый гейт `golang-migrate`: `Up()` требует, чтобы версия из `schema_migrations` существовала в каталоге миграций образа, поэтому откаченный Web/Receiver падал с `no migration found for version N` и уходил в crash-loop (эмпирически проверено на PostgreSQL 12: схема 32 + каталог до 0028), а `--migrate-down` старым образом блокировался тем же гейтом — оператор, успевший пересобрать старый тег, оставался без инструмента отката. Теперь [pg.Migrator.EnsureUp](../internal/platform/pg/migrate.go) сравнивает версию в БД с `MaxLocalVersion()` **до** `Up()`: схема новее → `State.Ahead`, миграции не применяются, старт продолжается; `dirty` → `ErrDirtySchema` и `exit 1`, как раньше. [bootstrap.AutoMigrate](../internal/platform/bootstrap/bootstrap.go) возвращает `SchemaState`, при `Ahead` пишет запись уровня **error** (уровень выбран ради Sentry: порог `sentry.level` = error, `warn` туда не доедет) и отдаёт состояние в `web.New`/`receiver.New` → метрики `nexus_pg_schema_version` / `nexus_pg_schema_ahead` ([metrics.go](../internal/platform/metrics/metrics.go)) + алерт `NexusSchemaAheadOfBinary` ([prometheus.alerts.yml](../deploy/prometheus.alerts.yml)). Плюс `--migrate-force <N>` (выход из `dirty` без psql; SQL не выполняет, номер сверяется с каталогом), [scripts/deploy/images.sh](../scripts/deploy/images.sh) (`tag`/`list`/`rollback` — `up --build` перетирает `nexus-*:latest`, и без сохранённого тега откат означал пересборку трёх образов) и [scripts/release/rollback_info.py](../scripts/release/rollback_info.py) (строка «Откат» для CHANGELOG). Процедуры оператора — [DEPLOYMENT.md §10](../DEPLOYMENT.md). Без миграций и новых конфиг-ключей |
| **§75: размер топиков Kafka на экране мониторинга** | ✅ §75 | ТЗ — [75-topic-size-jmx.md](sections/75-topic-size-jmx.md), ветка `feature/topic-size-jmx`. Колонка «Размер» в таблице «Топики» §31 показывала прочерк с самого появления раздела: `SizeBytes` не заполнялся никогда — `segmentio/kafka-go` не реализует `DescribeLogDirs` (в v0.4.51 нет ни метода, ни сообщения в `protocol/`). `kafka_exporter` тут не помогает вовсе — размера среди его метрик нет. Источник — JMX брокера (`kafka.log:type=Log,name=Size`): [deploy/docker/kafka.Dockerfile](../deploy/docker/kafka.Dockerfile) (`apache/kafka` + `jmx_prometheus_javaagent`; jar лежит в [deploy/vendor/](../deploy/vendor/), сборка не ходит в сеть, подмена версии/зеркала — `ARG JMX_AGENT_SRC`, который `ADD` принимает и путём, и URL) + [deploy/kafka-jmx.yml](../deploy/kafka-jmx.yml) (одно правило → `kafka_log_log_size{topic,partition}`) + `KAFKA_OPTS` в трёх compose + job `kafka-jmx` в [prometheus.yml](../deploy/prometheus.yml). Читает [PromMetrics.KafkaTopicSizes](../internal/web/adapter/out/prometheus/client.go) (instant `sum by(topic)(kafka_log_log_size)`), домешивает `fillTopicSizes` в [kafka_monitor.go](../internal/web/usecase/kafka_monitor.go) ДО записи в Redis-кеш (1 запрос на TTL 30 с). Величина = занято на дисках кластера (все реплики). Ответ получил флаг `sizes_available` (Prometheus отдал хотя бы одну серию): без него пустой топик с честным нулём выглядел бы так же, как отсутствующий источник. Деградация прежняя: нет Prometheus/агента или чужая Kafka → нули и «—» с `title`-подсказкой в [TopicsTable.tsx](../web-ui/src/components/kafka/TopicsTable.tsx). Эксплуатация: применение = пересоздание контейнера брокера (простой 30–60 с), битый конфиг агента не даёт JVM стартовать. Без миграций и новых конфиг-ключей |
| **§76: ссылка на команду — параметр `?team=<slug>` и кнопка «Поделиться»** | ✅ §76 | ТЗ — [76-team-share.md](sections/76-team-share.md), ветка `feature/team-share`. Активная команда жила только в серверной сессии (`Session.CurrentTeamID` в Redis, §4.15): переслать коллеге «рабочий стол команды X» было нечем, а два таба на `/` могли смотреть на разные команды неразличимо по URL. **Бэкенд не менялся вовсе** — `GET /api/me/teams` уже отдаёт `slug` каждого членства ([auth_handler.go](../internal/web/adapter/in/http/auth_handler.go)), переключение делает существующий `POST /api/me/switch-team`, а резолв `slug → team_id` клиентский, по членствам: нового эндпоинта не нужно, и «нет такой команды» / «я не член» неотличимы by design (no-leak, как единый 404 §58.3). **Фронт:** [lib/teamShare.ts](../web-ui/src/lib/teamShare.ts) — `TEAM_PARAM`, `teamPageUrl(slug)` (`${origin}/?team=<slug>`, всегда рабочий стол), `teamParamAllowed()` (белый список `/`, `/kafka`, `/audit`; `/nodes/*` исключены — командой страницы владеет §58, `/settings/*` — вне скоупа команды §7.14.1), хук `useTeamUrlParam()` — единственный писатель параметра; [components/TeamUrlSync.tsx](../web-ui/src/components/TeamUrlSync.tsx) — монтаж хука и баннер «Команда недоступна», включён в [AppShell.tsx](../web-ui/src/components/AppShell.tsx) над `<Outlet/>` ровно один раз; [TeamSwitcher.tsx](../web-ui/src/components/TeamSwitcher.tsx) — `ShareTeamButton` (Share2 → Check на 1.5 с) третьей кнопкой в строке команды, доступна всем ролям. Ссылка по **slug**, а не по id как у узла (§58): slug глобально уникален и неизменяем (`PUT /api/teams/{id}` принимает только `name`) → адрес читается человеком и не протухает. Семантика — **зеркало**: значение параметра всегда равно slug'у текущей команды сессии, входящее значение применяется один раз, запись всегда `replace` и функциональной формой (чужие ключи фильтров §54 обязаны выжить). Логики в местах переключения нет: параметр — производная от `current_team_id`, поэтому TeamSwitcher, «Избранное» сайдбара и §58 попадают в URL одним эффектом. Тесты: [teamShare.test.tsx](../web-ui/src/lib/teamShare.test.tsx) (16 сценариев), регресс сосуществования с §54/§71 — [overviewFilters.test.ts](../web-ui/src/lib/overviewFilters.test.ts), [Overview.period.test.tsx](../web-ui/src/pages/Overview.period.test.tsx). i18n: `teams.share`, `teams.unavailable`. Без миграций, эндпоинтов и конфиг-ключей. Неочевидности — §4.58. Бандл пересобран → `internal/web/static/` |
| **§77: логи узла — живое раскрытое тело, автопоиск, скорость на десятках млн записей** | ✅ §77 | ТЗ — [77-logs-ux-and-scale.md](sections/77-logs-ux-and-scale.md), ветка `feature/logs-ux-perf`. Четыре жалобы, три из которых упирались в одно: список перезапрашивался целиком и без временно́го окна. **77.1 (tail-poll):** `refetchInterval` у `useInfiniteQuery` убран; раз в 5 с тянется только «что появилось после самой свежей строки» (`from = ts − 1 с`) и вливается в начало первой страницы через `setQueryData` с дедупом по `id` — существующие строки не пересоздаются, раскрытое тело живёт; полный ответ хвоста = сигнал дыры → честный `invalidate`, а не склейка с потерей записей; `collapseToFirstPage` §72.5 не срабатывает, пока строка раскрыта (`collapseEnabled` в [useInfiniteLogs.ts](../web-ui/src/lib/useInfiniteLogs.ts)); пилюля «N новых записей ↑» работает и в snapshot. **77.2 (автоокно):** `searchAutoWindow` в [usecase/logs.go](../internal/web/usecase/logs.go) — окна `1ч→6ч→24ч→7д→30д→90д→без границы` от якоря (курсор пагинации либо `max(date_request)` из кешированного `DateRange`, TTL 30 с), первое окно с полной страницей — ответ; **последняя попытка всегда без нижней границы**, иначе фронт принял бы недобор за конец истории (§72.2); `Count` и пользовательский `from` автоокном не трогаются; замер: 50 млн строк — 7 мс на страницу против 1153 мс, 1 млн — 7 мс против 86 мс (время не зависит от объёма). **77.3 (автопоиск):** кнопка «Применить» удалена — Enter/blur у поля «Поиск» (Esc — откат), сразу у комбобоксов/дат/тумблеров; коммит идемпотентен (иначе Enter+blur дают дубль, а blur о «Сбросить» — лишний запрос); `api.get` принимает `AbortSignal` ([client.ts](../web-ui/src/api/client.ts)) → «Отменить» рвёт запрос и в ClickHouse (разрыв соединения → отмена `c.Request.Context()`), запрос дольше 0.7 с показывает «Поиск… [Отменить]», дольше 2 с — выключает автообновление. **77.4:** сегмент «Все\|Завершено\|В работе» убран как доказанный дубль «OK\|Ошибок» (`count` по 7 комбинациям на 17 боевых узлах: `err ≡ done=no`, пересечения 0; корень — `send.go` пишет `Done = 2xx`), параметр API и предикаты §72.1 сохранены, дип-линк §47 показывается снимаемым чипом. Тесты: [logs_autowindow_test.go](../internal/web/usecase/logs_autowindow_test.go) (7 unit), [log_autowindow_test.go](../tests/integration/log_autowindow_test.go) (E2E честности выдачи + масштабный замер `make test-int-logs-scale`), [LogsTab.autorefresh.test.tsx](../web-ui/src/components/node/LogsTab.autorefresh.test.tsx) (8, проверены красными на старом коде). Без миграций и конфиг-ключей. Неочевидности — §4.63. Бандл пересобран → `internal/web/static/` |
| **§78.6: единица счёта в логах — запись, а не строка** | ✅ §78.6 | ТЗ — [78-node-url-and-log-counters.md](sections/78-node-url-and-log-counters.md), ветка `feature/node-url-log-counters`. Боевой симптом: «Показано 17 из 40», остальные 23 записи недостижимы (скролл сообщал конец истории). Причина — разные единицы: `Count` считал `count()` (СТРОКИ таблицы), список после дедупа по `id` показывал ЗАПИСИ. Строка = прогон доставки: redelivery Kafka, DLQ-репроцессор §36, `ttl_expired`, replay и дренаж retry-топика §38 пишут свою строку с тем же `ID`; внутренние ретраи одного прогона схлопнуты в `attempts`/`attempts_details`. В [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go): `Count`/`CountFailed` → `uniqExact(ID)`, `Search` → `... ORDER BY date_request DESC, ID DESC LIMIT 1 BY ID LIMIT ?` (свежайшая попытка — та же строка, что раскроет `GetByID`). `CountFailed` теперь сходится с `FailedIDs` (уже был `DISTINCT ID`) и `DeleteFailed`, т.е. видимое «очищено N» перестало расходиться с числом отменённых сообщений (боевой аудит расхождения: `cancelled=5` при `deleted=10`). Намеренно НЕ тронуты: `ListSince` (live-tail — повтор `ID` там новое событие, а не дубль) и `CountErrors` §20.3 (порог Telegram-уведомлений считает прогоны доставки; перевод на записи сменил бы смысл порога у всех настроенных уведомлений). Автоокно §77.2 кода не потребовало — `len(recs) >= limit` само стало мерить записи. Тест: [log_dedup_count_test.go](../tests/integration/log_dedup_count_test.go) — первый тест логов, сеющий дубли `ID` (через эту дыру дефект и прошёл: ни один прежний тест их не сеял), проверен красным на старом коде (`Count` 120 вместо 40, страница двоила записи, `CountFailed` 80 вместо 30). Без миграций и конфиг-ключей. Неочевидности — §4.64 |
| **§78.1–78.5: короткий адрес узла `/api/v1/<команда>/<путь>`** | ✅ §78 | ТЗ — [78-node-url-and-log-counters.md](sections/78-node-url-and-log-counters.md), ветка `feature/node-url-log-counters`. Адрес без сегмента `request`/`requestAsync`: синхронность берётся из `node.root_method` при том же резолве узла (ни полей, ни миграций, ни лишних запросов — резолв кеширован L1/Redis). `RabbitMQAsync` → 404 (входящего HTTP у pull-узла нет, существование не раскрываем), sync на паузе → 202 `queued` (§3.6). **Legacy-формы работают без ограничения срока**, включая их прежнюю семантику: `/request/<async-узел>` по-прежнему 404, `/requestAsync/<sync-узел>` по-прежнему принимается. **Механика (§78.2):** три маршрута Receiver заменены ОДНИМ catch-all `/api/v1/*path` + разбор первого сегмента в [handler.go](../internal/receiver/adapter/in/http/handler.go) (`SplitVerb` → `handleIngress` → `handleAuto`), потому что `/api/v1/*path` рядом с `/api/v1/request/*path` роняет gin при старте (проверено на v1.12.0: `catch-all wildcard … conflicts with existing path segment 'request'`); разбор в `NoRoute` отвергнут — туда не доходят middleware группы (rate-limit не применился бы, а `metrics.GinMiddleware` выходит при пустом `FullPath`, и боевой трафик исчез бы из метрик). Инварианты, которые молча сломались бы: ключ rate-limit срезает сегмент метода тем же `SplitVerb` (иначе смена формы адреса удваивает квоту и меняет все ключи Redis); метка `method` переехала из `FullPath` в контекст (`metrics.RootMethodLabelKey`) со **значениями прежними** — на `method="requestAsync"` стоит дашборд Kafka, на `"request"` — алерт латентности; метку ставит вызывающий, поэтому paused sync остаётся `request`, уходя в async-ветку; новые значения только `callback` (раньше метился именем маршрута) и `route` (404 по короткому адресу). Web проксирует `/api/v1/*path` одним маршрутом вне группы `/api` — CSRF на боевой трафик по-прежнему не вешается; неизвестный `/api/v1/…` теперь отвечает 404 от Receiver, а не от SPA-фолбэка. **Смена контракта:** не-POST на `/api/v1/callback/…` → 405 (раньше 404 из `NoRoute`). **§78.3:** слоги `request`/`requestasync`/`callback` запрещены на СОЗДАНИИ команды ([team.go](../internal/web/usecase/team.go)), а не в `Team.Validate()` — та же функция зовётся при переименовании, и запрет в ней сломал бы правку legacy-команды; без миграции, ошибка локализована (`team.slug_reserved`). **§78.4:** признак «входной путь шины» в защите от самоссылки §32.2 расширен до всего `/api/v1/` — узел с `target_url` на собственный короткий адрес иначе прошёл бы проверку. **§78.5 (UI):** карточка «Конфиг» показывает короткий адрес основным, классический — строкой ниже; у команды `default` слог опускается, кроме путей с зарезервированным первым сегментом ([nodeUrl.ts](../web-ui/src/lib/nodeUrl.ts)). Тесты: [handler_shorturl_test.go](../internal/receiver/adapter/in/http/handler_shorturl_test.go), [receiver_proxy_test.go](../internal/web/adapter/in/http/receiver_proxy_test.go), [receiver_short_url_test.go](../tests/integration/receiver_short_url_test.go) (E2E на реальном PG), [nodeUrl.test.ts](../web-ui/src/lib/nodeUrl.test.ts). Swagger перегенерирован, бандл пересобран. Неочевидности — §4.65 |
| **§78.7: проброс параметров при смене метода (GET→POST)** | ✅ §78.7 | Кода не потребовалось — поведение проверено на боевом узле `sbp-qr` (GET с query → `POST <target>?<query>`, 200) и зафиксировано контрактом ТЗ, потому что вопрос возникает повторно. Query входящего запроса всегда переносится в URL исходящего (`ResolveURL` → `appendQuery` в [route.go](../internal/receiver/usecase/route.go)), параметры узла и клиента складываются (`Add`, не `Set`), вырезаются только служебные — `url_param_name` при `from_request` §3.4 и поле динамической авторизации при `auth_dynamic_source=query` §41. Тело исходящего = тело входящего: у GET оно пустое, конверсии «query → тело» нет и не вводится (формат тела — контракт принимающей системы). Пробел был в ПОКРЫТИИ: тесты подмены метода §40 шли с пустой query, тесты query — с методом POST, то есть именно эта связка не проверялась ничем. Закрыт [receiver_get_to_post_test.go](../tests/integration/receiver_get_to_post_test.go) (метод, все параметры включая повторяющийся ключ и параметр узла из `target_url`, пустое тело); тест проверен мутацией прод-кода — при отключённом `appendQuery` краснеет. Известное ограничение, зафиксированное тестом намеренно: `Content-Type` берётся из входящего запроса, поэтому «голый» GET даёт исходящий POST без него (задать на узле нечем — `forward_headers` только пробрасывает пришедшее); строгий приёмник ответит 400/415. Статические заголовки узла — вне рамок §78 |
| **§79.1–79.2: неудачная доставка = запись без успешного прогона** | ✅ §79 | ТЗ — [79-failed-unit-node-tab-metrics.md](sections/79-failed-unit-node-tab-metrics.md), ветка `feature/failed-unit-node-tab-metrics`. Боевой разбор узла `kz` (команда `conv`, 2026-08-05): в «Неудачных доставках» висели 17 записей, из которых **16 уже доставлены** авто-репроцессором (свежайший прогон 200 OK), а «Повторить все сейчас» взяло их же и отправило заново — **15 дублей в 1С**. Причина: предикат `uniqExact(ID) WHERE done=0` смотрел на СТРОКИ, а успешный повтор дописывает новую строку и старую не убирает. Теперь неудачная = ЗАПИСЬ, у которой в окне нет ни одного прогона `done=1` (поле `port.LogQuery.Unresolved`); множество тождественно `Errors` из `NodeKPI`, поэтому KPI узла и KPI вкладки «Очередь» сходятся по построению (на бою расходились 1 против 13). Фильтр `done` журнала НЕ тронут — там единица строка, у него зеркало live-tail и инварианты §72.1. Три формы SQL по принципу «платить за малую сторону» ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)): счётчик — разность агрегатов (`uniqueExprs`, общая с `NodeKPI`), набор ID — кандидаты `done=0` минус проба `done=1` по этим же ID (`deliveredAmong`+`diffIDs`), страница — кандидаты с отсевом и добором (`searchUnresolved`, ≤3 раунда: неполная страница читается фронтом как конец истории §72.2). Разность считается в Go с клампом (`subUnsigned`) — на HLL `uniq − uniqIf` уходит в минус, и `uint64` показал бы 1.8e19. `DeleteFailed` заменён на `DeleteFailedRows(ids)`: строки `done=1` не трогаются, история остаётся. `ReplayFailed` берёт только нерешённые (дубли исчезают) и после успеха убирает строки оригиналов одним batch-вызовом (`cleanupReplayed`, best-effort — сообщения уже отправлены, ронять операцию из-за гейта нельзя); `PurgeFailed` считает множество ОДИН раз для tombstone'ов и удаления (уходит боевое расхождение аудита `cancelled=5` при `deleted=10`), ценой ограничения `peekCap` записей за вызов. Все три пути собирают запрос общей `failedQuery` — она же впервые включает сужение по партициям §72.4. Тесты: [log_reader_failed_test.go](../internal/web/adapter/out/clickhouse/log_reader_failed_test.go), [log_failed_unresolved_test.go](../tests/integration/log_failed_unresolved_test.go) (E2E «спасённая репроцессором запись»), правки `log_dedup_count_test.go` (30 → 20, красный на старом коде). Без миграций. Неочевидности — §4.66 |
| **§79.3: вкладка узла в адресе (`?tab=`)** | ✅ §79.3 | Активная вкладка стала производной АДРЕСА: `/nodes/<id>?tab=overview\|logs\|config\|metrics\|queue`. До раздела `?tab=` распознавался только как `logs` (§47.4) и никогда не писался — ссылку «вот метрики этого узла» переслать было нечем. Клик пишется `push` (вкладка — навигация, «Назад» возвращает предыдущую), мусорное значение нормализуется `replace`. Окно и быстрые фильтры журнала (`from`/`to` в мс, `status`, `done`) переехали в адрес вместе с вкладкой — иначе второй источник истины разъезжается на «Назад»; уход с «Логов» их чистит (раньше это делал обработчик клика, при `Назад` не срабатывавший). Рендер вкладок остаётся УСЛОВНЫМ: `LogsTab` читает `initialFilter` только при монтировании. Чистые функции — [nodeTabUrl.ts](../web-ui/src/lib/nodeTabUrl.ts) (запись всегда функциональной формой над `prev`: чужие ключи обязаны выживать, правило §54). «Поделиться» отдаёт ссылку с текущей вкладкой. Тесты: `nodeTabUrl.test.ts` (17), `NodeDetail.tabs.test.tsx` (9 — первый тест на эту страницу). Бандл пересобран |
| **§79.4–79.5: фильтры и «Шаг графика» на вкладке «Метрики»** | ✅ §79 | Вкладка принимает те же фильтры, что журнал (`q` с режимами, `method`, `client_host`, `status`; сегмента «Завершено/В работе» нет — §77.4 убрал его из журнала как дубль «ОК/Ошибок») — KPI и график считаются под ними; условия строит общий `searchConds`, мини-язык §48 разбирает usecase (кривой regex → 400). Порты `NodeKPI`/`NodeChart` переведены на `port.LogQuery`; метрики впервые получают сужение §72.4 и потолок `metricsTimeout=10s` (полнотекст читает тела с диска, а вкладка поллится каждые ~12 с). **«Шаг графика»** (`step=auto\|1h…30d`): пара (окно, шаг) согласуется чистой `resolveChartStep` — шаг больше окна сжимается до окна, >400 столбцов поднимает шаг; фактический шаг уезжает в `step_seconds` (клиент угадывал ширину столбца по разнице меток и врал на ряде из одной точки). **Единица столбца — ЗАПИСЬ по интервалу ПРИХОДА с ИТОГОВЫМ статусом**: после успешного авто-повтора красный сегмент исчезает из столбца сам, как и запись из «Неудачных доставок»; исторические столбцы меняются задним числом — это ожидаемо и записано в подсказке. Цена — свёртка строк в записи по всему окну (`GROUP BY ID`), и приблизительный режим тут не помогает; порог `web.metrics_exact_chart_max_records` (дефолт 2 млн, известен бесплатно из `kpi.Total`) переключает график на счёт по прогонам с пометкой `chart_unit=attempts` — молча подменять семантику нельзя. Спарклайны стола перешли на ту же единицу (перестали расходиться с `In`/`Out` своей строки). Фронт: панель фильтров вынесена в общий `LogsAdvancedFilters` (13 тестов `LogsTab` остались зелёными без правок — доказательство чистоты рефакторинга), медленный (>2 с) фильтр выключает авто-обновление. Тесты: `metrics_window_test.go`, `metrics_handler_test.go` (новый), E2E `TestMetricsReader_ChartByRecord_E2E`, `MetricsTab.filters.test.tsx`. Один новый конфиг-ключ, без миграций |
| **§79.6: goleak доведён с 5 пакетов до 24** | ✅ §79.6 | Политика CLAUDE.md §8 была записана, но выполнена в 5 пакетах из ~51. Эшелон A — 13 пакетов без опций; эшелон B — 8 пакетов через новый [testleak](../internal/platform/testleak/testleak.go) (`HTTPClient()` — keep-alive пул `net/http` живёт своим циклом после `Close`; `GRPC()` — keepalive/controlBuffer/сериализатор колбэков внутри `ClientConn`). Наборы исключений в одном месте: они зависят от версий библиотек. Пограничные `platform/otel`, `platform/bootstrap`, `web/adapter/out/clickhouse` включены ПОСЛЕ прогона — оказались зелёными без единого исключения (`-count=5` стабильно). Правило приоритета в godoc: исключение только для чужих горутин, свои джойнятся в тесте. `tests/integration` вне политики осознанно (testcontainers/Ryuk/pgxpool/CH idle-closer дают 15+ исключений, ломающихся на каждом апгрейде). Makefile и CI не менялись |
| **§81.1–81.2: отмена вызывающей стороны — не отказ приёмника** | ✅ §81 | Боевой инцидент 06.08.2026 (`acs_sigur`, webhook): 71 sync-вызов, **0 успешных**, 65 обрывов ровно по 50 000 мс — и параллельно 313 async-сообщений в DLQ с `attempts=0`. Приёмник при этом отвечал **200 за 145 с**: соединение рвал элемент инфраструктуры перед шиной, а учёт здоровья узла (`ошибки нет И статус < 500`) засчитывал это ОТКАЗОМ ПРИЁМНИКА. Пять подряд открывали breaker, половинчато-открытая проба обрывалась так же — **выхода из состояния не существовало**. Ключ breaker'а один на узел и общий для sync/async, поэтому обрывы синхронного трафика душили асинхронную доставку того же узла. Фикс: трёхзначный вердикт вместо булева `upstreamHealthy` ([breaker_vote.go](../internal/sender/usecase/breaker_vote.go)) — «воздержался», когда ушла ВЫЗЫВАЮЩАЯ сторона; breaker не трогается ни в одну сторону. Дискриминатор — живость РОДИТЕЛЬСКОГО контекста, не текст ошибки: `httpclient` оборачивает вызов в дедлайн узла, поэтому наш таймаут оставляет родителя живым, а уход клиента его убивает; `ctx.Err()` проверяется РАНЬШЕ `DeadlineExceeded`, иначе чужой дедлайн выше по стеку снова засчитается приёмнику. **Скрытая мина, найденная попутно:** учёт шёл на уже отменённом контексте, а go-redis отбрасывает такую команду ещё в пуле (`internal/pool.waitTurn` проверяет `ctx.Done()`) — то есть часть отказов молча не доезжала, и корректность держалась на побочном эффекте библиотеки; `RecordSuccess`/`RecordFailure` и `SetLastOutcome` переведены на `context.WithoutCancel`+2 с, `_ = err` заменён на Debug. Маркеры `client_canceled`/`upstream_timeout` ([domain/log_reason.go](../internal/domain/log_reason.go)) вместо сырого `err.Error()`: тот нестабилен, не фильтруется и **тащил в журнал полный адрес** — у динамического URL хвост собран из входящего запроса (утечка класса §68); тот же маркер ставится в `attempts_details`. Тесты красные на старом коде: 5 обрывов давали 5 отказов |
| **§81.3: политика breaker'а настраивается** | ✅ §81.3 | Порог и cooldown были литералами `circuitbreaker.New(a.redis, 5, 30*time.Second)`. Теперь `sender.circuit_breaker.{threshold,cooldown_sec}` глобально + переопределение на узле (миграция [0033](../migrations/0033_node_circuit_breaker.up.sql), nullable-колонки: NULL = «как в конфигурации»; 0 не подошёл бы — он осмыслен для многих настроек). Доставка политики: async читает узел из БД на каждое сообщение, sync получает её в `SendRequest` (proto, теги 31–32) — **порядок выката Sender → Receiver → Web**, старый Sender поля игнорирует. `domain.BreakerPolicy` живёт в domain: через него usecase объявляет порт, а реализация применяет — иначе usecase импортировал бы конкретную реализацию. **Cooldown берётся ИЗ СОСТОЯНИЯ** (его записал `RecordFailure` по политике узла): `Allow` и `IsOpen` читают `cooldown_ns` из hash'а, глобальное значение — запасное для ключей до §81.3. Иначе настройка была бы косметической: снимок показывал бы одно, а проба выдавалась по другому. Закрывает §80.5 п. 1. Ослабление защиты попадает в аудит (`diffNodes`) |
| **§81.4: состояние и ручной сброс защиты** | ✅ §81.4 | Оператор впервые видит, почему узел молчит, и снимает блокировку одной кнопкой: раньше `circuit_breaker_open` был виден только в тексте причины отдельной записи, а снять — никак (cooldown 30 с, ключ 10 мин, бейдж «Down» — 30 суток и снимается лишь следующим фактическим вызовом). `GET /api/nodes/:id/breaker` — все роли (наблюдателю тоже надо понимать причину), `POST .../breaker/reset` — manager+, аудит `node.breaker_reset` с состоянием ДО сброса. Отдельная от `async-queue` группа маршрутов: та под `KafkaRateLimit` и про очередь, а breaker есть и у sync-узла. **Сброс = удаление ключа**, а не запись `closed`: отсутствие ключа и есть «closed без истории» — не надо думать о TTL, счётчике проб и устаревшей политике, идемпотентность бесплатна. Заодно снимается персистентный бейдж §52 — **удалением, а не записью «ok»**: исход последнего вызова после ручного сброса неизвестен, врать нельзя; читатель отсутствие ключа уже обрабатывает. Чтение **НЕ fail-open** (в отличие от `Allow`/`IsOpen`): соврать оператору «закрыт» хуже, чем вернуть 503 — fail-open нужен там, где на кону доставка, здесь на кону решение человека. Платформа: `circuitbreaker.Key()` (ключ собирался конкатенацией в пяти местах, а читает его ДРУГОЙ процесс) и тип `Admin{Snapshot,Reset}` ([admin.go](../internal/platform/circuitbreaker/admin.go)) — отдельный от `Breaker`, потому что у читающей стороны политики нет и быть не должно. Политика пишется в тот же hash: без неё UI не покажет «3 из 5» и обратный отсчёт |
| **§81.5: отменённые клиентом записи вне массового повтора** | ✅ §81.5 | Запись `client_canceled` остаётся недоставленной и ВИДНА в «Неудачных доставках» (шина доставку не подтвердила — сигнал честный), но из «Повторить все сейчас» исключается: приёмник тело принял целиком и, вероятно, обработал — повтор дал бы дубли, как в §79 (15 дублей в 1С на узле `kz`). `port.LogQuery.ExcludeReasonPrefixes` → `NOT startsWith(reason, ?)`; `ReplayFailed` делает два запроса (полное множество для счётчика пропущенных и отфильтрованное — в работу; считать разницу после выборки нельзя, cap обрезал бы её произвольно), `skipped_client_canceled` уходит в ответ и аудит. Очистка «неудачных» отсев НЕ применяет — убрать с глаз это ровно то, что от неё ждут. Точечный повтор из строки доступен: там решает оператор. Историю маркер не покрывает (записи до §81.2 несут сырой текст) |
| **§81.6–81.7: карточка защиты и поля политики в UI** | ✅ §81 | Карточка «Защита узла» ([BreakerCard.tsx](../web-ui/src/components/node/BreakerCard.tsx)) между баннером статуса и секциями очереди — НЕ зависит от типа узла: у sync секции «Ожидают отправки» нет, а защита есть, и болит как раз там. Открытое состояние: счётчик «5/5», обратный отсчёт (тикает локально — дёргать сервер ради секундной стрелки незачем), объяснение и оговорка про бейдж; закрытое — одна строка. Знаменатель только когда политика известна: у состояния до §81.3 её нет, подставлять своё нельзя. Поллинг 5 с при открытой защите, 30 с при закрытой. Состояние видно всем ролям, кнопка manager+. Без Redis карточка не рендерится вовсе. Поля политики — в блоке «Таймауты и повторы» (он есть у всех типов, в отличие от блока DLQ), пустое = «как в конфигурации». Тесты: 5 на карточку (включая «клик шлёт POST именно на `/breaker/reset`» — урок §79.11) и 4 на валидацию |
| **§82.1: потолок sync-вызова снят (keepalive gRPC)** | ✅ §82.1 | gRPC-сервер Sender'а собирался без `KeepaliveEnforcementPolicy` ([sender/app.go](../internal/sender/app.go)), поэтому действовал дефолт grpc-go: допустимый интервал ping'ов клиента — 5 минут, а клиенты пингуют каждые 30 с (`keepalive_time_sec`). Каждый ping — «нарушение», на третьем сервер шлёт `GOAWAY ENHANCE_YOUR_CALM / "too_many_pings"` и закрывает СОЕДИНЕНИЕ ЦЕЛИКОМ вместе с идущим по нему вызовом. В unary-RPC сервер до ответа в стрим не пишет, счётчик растёт беспрепятственно → потолок `4 × keepalive_time_sec`, `timeout_ms` узла не влияет. Замеры (узел 600 000 мс, приёмник 300 с, curl без таймаута): **502 на 121,013 с** напрямую в Receiver, **на 91,024 с** через Web — потолок плавающий, ping-таймер привязан к соединению пула, а не к запросу. Async не затронут (Kafka-consumer зовёт приёмник без gRPC) — отсюда боевая картина, где async доставлял за 145–155 с, а sync не доходил ни разу. Фикс: `ServerKeepalivePolicy` ([grpcsender/server.go](../internal/platform/grpcsender/server.go)) — серверная половина договора живёт в одном пакете с клиентской, потому что дефект и случился от их расхождения. Опции сервера вынесены в `sender.GRPCServerOptions`, чтобы тест поднимал сервер ТЕМИ ЖЕ опциями, что боевой процесс (иначе регресс не ловился бы при удалении политики из боевой сборки). Конфиг `sender.grpc_keepalive` + проверка в `Validate`, что политика не строже клиентских `keepalive_time_sec`. Регресс `TestSender_GRPCKeepalive_LongUnaryCallSurvives` красный на старом коде: обрыв на 41,01 с |
| **§82.2: `client_canceled` больше не обвиняет клиента** | ✅ §82.2 | Маркер §81.2.1 обещал «ушла ВЫЗЫВАЮЩАЯ сторона», а ставится на ЛЮБУЮ смерть родительского контекста: дискриминатор один и грубый (`ctx.Err() != nil`). Кроме ухода клиента сюда попадают штатный shutdown и обрыв транспорта Receiver→Sender — до §82.1 так выглядел GOAWAY собственного gRPC-сервера. Цена измерима: боевой разбор больше суток искал причину во внешнем фронте, потому что журнал прямо указывал на клиента. Отдельный маркер `transport_aborted` НЕ вводится — признак пришлось бы тянуть из Receiver'а в Sender через поле gRPC-запроса ради случая, который после §82.1 почти не встречается. Вместо него: godoc `domain.ReasonClientCanceled` с перечнем исходов, способ различить их на стороне Receiver'а (`codes.Unavailable` — транспорт, `codes.Canceled` — клиент) и переименование `SendOutput.CallerGone` → `ParentGone` (имя утверждало тот же неверный контракт, а на местах вызова godoc не виден) |
| **§82.3: async-эндпоинт принимает только async-узел** | ✅ §82.3 | `/api/v1/requestAsync/<путь>` не проверял `root_method` ВООБЩЕ: послабление делалось ради §3.6 (paused-узел копит запросы), но выдано безусловно — наружу. Зеркальная sync-проверка при этом была на месте. Боевое следствие: `acs_sigur` с `root_method=request` принимал ~11 async-запросов в минуту от клиента, застрявшего на `logId 78241` больше суток — в async шина отвечает своим `{"result":true,"id":…}` мгновенно, а клиент ждал `code: 2000` от приёмника и не сдвигался никогда (~1,5 млн повторных записей в 1С за сутки); отбить настройкой узла было нечем. Теперь внешний вход требует `root_method=requestAsync`, иначе **404 `node not found`** — существование узла наружу не раскрываем (та же линия, что у зеркальной sync-проверки и pull-узла в `handleAuto`); диагностику дают warn с `root_method`/`client_ip` и метрика `nexus_async_ingress_rejected_total{node}`. Признак внешнего вызова — `RouteInput.ExternalAsync`, ставит его ТОЛЬКО `handleAsync`. **ЛОМАЮЩЕЕ изменение контракта.** Границы закреплены тестами, а не словами: §3.6 (paused sync → 202 queued, и по короткой форме, и по legacy) и callback §16 не затронуты; побочно закрыт приём push'а pull-узлом `RabbitMQAsync`. Красных на старом коде — 4 кейса |
| **§83.1-83.2: шаблон ответа приёма (мини-DSL)** | ✅ §83 | Клиенты с курсором двигают указатель только по эхо-подтверждению: `acs_sigur` ждёт `{"confirmedLogId": <max logId>}`, и стандартный async-ответ шины его не несёт. Спека узла — JSONB `nodes.async_ack_spec` (миграция `0034`, NULL = «отвечать как раньше»); язык — свой пакет [internal/domain/ackspec](../internal/domain/ackspec) без внешних зависимостей (прецедент — `logsearch` §48). Ключевое в реализации: числа проходят через `json.Number` (курсоры журналов выходят за 2^53, и `float64` их портит), вывод без HTML-экранирования (эхо обязано совпасть со входом), правило кавычек делает `"${ … | max }"` числом `79154`, а не строкой. Скомпилированные шаблоны лежат в LRU по тексту (узел приезжает из Redis строкой; 70 нс против 937 нс компиляции). Fuzz на компилятор и рендерер; golden-кейс на боевом теле СКУД |
| **§83.3: рендер до публикации, ответ и метрика** | ✅ §83 | Рендер живёт в [route_async_ack.go](../internal/receiver/usecase/route_async_ack.go) и выполняется ДО `Produce` — не по стилю, а по необходимости: при `on_error=error` 400 обязан означать «не принято», иначе клиент повторит уже принятый пакет и шина сама породит дубли (тест проверяет `queue.published == 0`). Источники подстановок — `effBody`/`cleanQuery`, то есть после вырезания кред, плюс отдельная чистка query-поля ВХОДЯЩЕЙ авторизации (`CheckIncomingAuth` его не удаляет). Handler отдаёт готовый ответ с `nosniff` и `X-Nexus-Id` (в кастомном теле id шины может не быть). Метрика `nexus_async_ack_render_failed_total{node,reason}` — при `on_error=default` клиент видит 200, и деградация иначе невидима. Провал шаблона НЕ красит узел в degraded: исход узла (§52) описывает доставку приёмнику и принадлежит Sender'у — закреплено тестом |
| **§83.5: очередь при возврате узла в sync** | ✅ §83 | Дефект 83.0-1: `AsyncProcessor.Handle` и `DLQReprocessor` не проверяли `root_method` вообще, и узел, возвращённый в sync, продолжал доставлять накопленное. Простое сравнение с текущим режимом сломало бы §3.6: у sync-узла в очереди законно лежат сообщения, попавшие туда на паузе. Поэтому конверт получил поле `ingress_method` — режим узла В МОМЕНТ ПРИЁМА (обе копии структуры). Гейт: принято как `requestAsync` + узел уже не async → DLQ с `reason="node_not_async root_method=<текущий>"` (реальный режим обязателен: в лог CH тип пишется константой `requestAsync`, дефект 83.0-2). Пустое поле (конверт до §83) доставляется как раньше — выкат не обнуляет очередь. Гейт стоит ДО paused-ветки и продублирован в репроцессоре, иначе тот обошёл бы его с другой стороны |
| **§83.6: карточка формы, справка и предпросмотр** | ✅ §83 | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) — карточка между «Авторизация» и «Проброс заголовков», скрыта у pull-узлов; преобразование «поля формы ↔ спека» вынесено в [lib/ackSpec.ts](../web-ui/src/lib/ackSpec.ts) и покрыто тестами (выключенный переключатель шлёт `null` и НЕ сохраняет черновик шаблона). Предпросмотр — `POST /api/nodes/ack-preview` ([ack_preview.go](../internal/web/usecase/ack_preview.go)): работает по спеке, а не по id узла, поэтому доступен в несохранённой форме; провал подстановки приходит с 200 и `ok=false` + проблемный `${…}`, невалидная спека — 400 в том же формате `{error, code, field}`, что и сохранение. Справка [AckHelpDialog.tsx](../web-ui/src/components/node/AckHelpDialog.tsx) — 12 разделов с кликабельными РАБОЧИМИ примерами («Использовать» кладёт шаблон в поле); состав зафиксирован в ТЗ §83.6, чтобы текст не разъехался с поведением |
| **§83.6 (фикс): «Взять последний запрос из логов» всегда падало** | ✅ §84 | Боевой симптом на `acs_sigur` (логирование тела ВКЛЮЧЕНО, записи есть): кнопка отвечала «записей нет или логирование тела выключено». Причина — форма вызова обёртки: `api.get(url, params)` принимает карту query-параметров **вторым аргументом** ([client.ts:40-47](../web-ui/src/api/client.ts)), а [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) передавал `{ params: { which: "request", limit: 8192 } }`. Axios сериализовал вложенный объект как `?params[which]=request`, обязательный `which` до сервера не доезжал и `/log/:id/body` отвечал **400** — что фронт показывал тем же единственным текстом. Проверено на бою: `?params%5Bwhich%5D=request` → 400, `?which=request` → 200 с телом. **Типизация промолчала**: `params?: Record<string, unknown>` принимает `{ params: … }` как совершенно законное значение, и ни `tsc`, ни ESLint такое не ловят — только тест, смотрящий на ФАКТИЧЕСКИЕ аргументы вызова. Эталон правильной формы был рядом в том же репозитории ([LogsTab.tsx:1036-1040](../web-ui/src/components/node/LogsTab.tsx)) — расхождение чисто копипастное. Фикс: запрос вынесен в [lib/lastLogBody.ts](../web-ui/src/lib/lastLogBody.ts) с замкнутым множеством причин отказа (`no_node` / `no_records` / `body_not_logged` / `request_failed`) — единый текст уводил разбор в настройки узла, где всё было в порядке; на несохранённом узле запрос теперь не уходит вовсе. 8 тестов, из них «which уходит плоским параметром» красный на старом коде |
| **§84.1: круглый шаг графика** | ✅ §84.1 | [metrics_window.go](../internal/web/usecase/metrics_window.go) — вся правка в одной чистой функции. `autoChartBuckets` (60/48/84/90 от §79.5) перестал быть РЕЗУЛЬТАТОМ и стал ЦЕЛЕВОЙ плотностью: частное `окно/плотность` снапится **вверх** на `chartStepLadder` — `1м/5м/10м/15м/30м/1ч/2ч/3ч/6ч/12ч/1сут`. Критерий отбора ступеней не эстетический: **все делят 86400 нацело**, а `toStartOfInterval` считает от UTC-эпохи, поэтому только делитель суток кладёт границы на круглые отметки. Раньше 3 ч → 225 с (3 мин 45 с) и 14 д → 13 440 с (3 ч 44 мин), и эта рваная граница уезжала в журнал: `step_seconds` определяет и подсказку, и выборку при клике по столбцу (§33.4). Снап **только вверх** — гарантия, что столбцов не станет больше, чем было: нагрузка на CH при поллинге раз в 12 с не растёт ни на одном окне. Изменились три окна: 3 ч → 300 с (36 столбцов), 14 д → 21 600 с (56), 30 д → 43 200 с (60). Досрочный `return` из авто-ветки убран — авто-шаг проходит те же ограничения «шаг > окна» и «потолок 400», иначе окно короче минуты давало бы столбец шире самого окна. Тесты красные на старом коде: три строки таблицы + `TestAutoStepIsRound` (31 окно, `86400 % step == 0`) + `TestAutoStepNeverDenserThanBefore` + `TestChartStepLadderDividesDay` (проверяет сам список: опечатка в ступени тихо ломает выравнивание). **Граница защиты:** выравнивание держится на UTC — суточный столбец начинается в 03:00 МСК, а в зоне со смещением не кратным часу не лягут и часовые |
| **§84.2: период и шаг вкладки «Метрики» в адресе** | ✅ §84.2 | Ключи — **свои**: `range`/`step`/`mfrom`/`mto` ([nodeTabUrl.ts](../web-ui/src/lib/nodeTabUrl.ts)). Делить `from`/`to` с дип-линком журнала нельзя по двум конструктивным причинам: `parseLogsInitialFilter` признаёт окно только при ОБЕИХ границах (а метрики после §84.4 умеют одностороннее — журнал молча показал бы не то окно), и правило «остаются только ключи целевой вкладки» требует однозначного владельца ключа. Частное правило «уход с Логов чистит окно» обобщено до карты `TAB_PARAMS` + общей `gotoTab`. **Найдено при этом:** чистку делал только `withNodeTab`, поэтому клик по столбцу графика (`withLogsWindow`) и переход из «Очереди» (`withFailedLogs`) оставляли период с шагом в адресе — тот обещал вид вкладки, на которой пользователя уже нет; теперь все три перехода идут через одно правило. **Ключевое различение:** отсутствие `step` в адресе означает «шаг НЕЯВНЫЙ, берётся дефолтом периода». Без него дефолт одного периода (6 ч у семи суток) при переключении на сутки переезжал бы туда УЖЕ КАК ВЫБОР пользователя и закреплялся в адресе — поймано собственным тестом, а не ревизией. Словарь шага расширен до лестницы §84.1 и **сужается по периоду** (`stepsForPeriod`: только `[окно/400, окно]`) — шаг крупнее окна сервер сожмёт, мельче потолка поднимет, и сегмент показывал бы не ту плотность. Дефолт шага перестал быть «Авто» (`defaultStepFor`: наибольшая ступень, дающая ≥24 столбца → 24ч даёт 1ч). «Поделиться» несёт вид (`shareableTabParams`), но окно журнала по-прежнему НЕ шарится — правило §79.3 сохранено. 47 новых фронт-тестов |
| **§84.3: вид вкладки «Метрики» запоминается для каждого узла** | ✅ §84.3 | Закрывает пункт §79.7, который вынес это «вне рамок». **Одна строка префа §71 на узел**, ключ `node.metrics.view.<id без дефисов>` ([user_preference.go](../internal/domain/user_preference.go), зеркало — [prefs.ts](../web-ui/src/lib/prefs.ts)). Дефисы снимаются не для красоты: формат ключа §71 их не допускает, а UUID без них — 32 символа нижнего hex, с префиксом ровно **50 при потолке 64**; запас закреплён тестом, потому что удлинение префикса однажды упрётся в 400 при сохранении. **Миграции не потребовалось.** Почему не карта в одном значении: она упирается в `CHECK octet_length <= 4096` примерно на сороковом узле и требует клиентского вытеснения — механизма, который молча теряет настройки; отдельные строки эту проблему не решают, а устраняют, потолка по числу узлов не существует. Цепочка: **адрес → преф этого узла → системный дефолт**; промежуточного «общего вида на все узлы» намеренно нет (третий уровень пришлось бы объяснять в интерфейсе). Гейт `viewReady` = «запрос префов завершён» пробрасывается в `useNodeMetrics` как `enabled` — до прихода префов запрос метрик НЕ уходит вовсе, иначе повторяется мигание §71 (показали 24 ч, через 300 мс переключили на 7 д, два запроса вместо одного); `settled = !isPending`, а не `isSuccess` — недоступный `/api/me/prefs` не имеет права оставить вкладку без метрик. Преф пишется ТОЛЬКО из действия пользователя, никогда из эффекта «адрес → состояние»: иначе «Назад» переписывал бы личный дефолт (закреплено тестом «первая загрузка ничего не пишет»). Произвольный период в преф не сохраняется, но и НЕ СТИРАЕТ ранее сохранённый пресет. `team_id` = команда узла, поэтому составной FK `(user_id, team_id) → user_teams ON DELETE CASCADE` сам чистит строки при исключении из членства. **Граница защиты:** строки префов удалённых узлов не вычищаются (FK на `nodes` нет, эндпоинта удаления префа в §71 не существует) — клиент неизвестные id игнорирует, строка стоит ~150 байт. 10 фронт-тестов + доменный тест ключа |
| **§94: журнал отказов на входе («Отказы»)** | ✅ §94 | ТЗ — [94-rejected-requests.md](sections/94-rejected-requests.md), ветка `feature/rejected-requests`. Клиенты, которым шина отказала (нет узла, метод не разрешён, авторизация, лимиты), не оставляли следов: debug-лог (на бою не пишется), в ClickHouse писать некому (лог живёт в таблице УЗЛА, которого нет), а `nexus_requests_total` не знает клиента. Хранение — PostgreSQL в АГРЕГИРОВАННОМ виде: группа `(team_slug, node_path, reason, http_method)` → клиенты (IP, PTR §67, User-Agent) → 20 последних сэмплов; миграция [0040_rejected_requests](../migrations/0040_rejected_requests.up.sql). Сбор — одно middleware после `c.Next()` в группе `/api/v1` ([reject_middleware.go](../internal/receiver/adapter/in/http/reject_middleware.go)) + неблокирующий коллектор с фоновым сбросом ([collector.go](../internal/receiver/adapter/out/rejectlog/collector.go), [repo.go](../internal/receiver/adapter/out/rejectlog/repo.go)). Срок хранения — «Настройки → Общие» (0…365 дней, дефолт 30, **0 = сбор выключен + удалить накопленное**), доставка без рестарта через новую секцию `reloader.SectionGeneral`; чистка [housekeeping_rejected.go](../internal/web/usecase/housekeeping_rejected.go) раз в час + внеочередной прогон при смене настройки. API — [rejected_handler.go](../internal/web/adapter/in/http/rejected_handler.go) (operator+, scope `logs:read`), UI — вкладка внутри раздела «Логи» ([RejectedTab.tsx](../web-ui/src/pages/logs/RejectedTab.tsx), [RejectedDrawer.tsx](../web-ui/src/components/logs/RejectedDrawer.tsx)), бейдж неразобранных групп на пункте меню и поле срока в «Общих». Строка «отклонено на входе» на рабочем столе была сделана и снята по решению заказчика — главный экран отвечает за трафик узлов. Метрики `nexus_ingress_rejected_total{reason,status}` и `nexus_ingress_reject_dropped_total{cause}`. **Изменение существующей метрики:** метка `node` у `nexus_requests_total` при 404 несуществующего узла = `<unresolved>`. Неочевидности — §4.83. Миграция 0040 |
| **§95: маскирование секретов в логах узлов по шаблонам** | ✅ §95 | ТЗ — [95-log-secret-masking.md](sections/95-log-secret-masking.md), ветка `feature/log-secret-masking`. При `path_passthrough` (§39) секрет приходит В ПУТИ и оседал открытым текстом в логе узла ClickHouse (`url`/`method`); `platform/sensitive` (имена полей), `redactURL` (только query) и маскирование nginx §94 (только access-лог) этот сток не закрывали — найдено на бою `nexus-kz`. Справочник regex-шаблонов (RE2) в PG, миграция [0041_log_mask_patterns](../migrations/0041_log_mask_patterns.up.sql) (сиды Telegram + green-api). Применение — в ЕДИНОЙ точке [chlog.Writer.Write](../internal/sender/adapter/out/chlog/writer.go) к `url`/`method`/`parameters` ДО буфера и retry-топика Kafka §38 (иначе токен утёк бы через `nexus.logs.retry`); набор держит атомарный [LogMaskProvider](../internal/sender/usecase/log_mask.go), сид из PG на старте ([maskpg/reader.go](../internal/sender/adapter/out/maskpg/reader.go)) + hot-reload новой секции `reloader.SectionMasking` без рестарта. Управление — admin-only CRUD `/api/log-masks` + preview ([log_mask_handler.go](../internal/web/adapter/in/http/log_mask_handler.go), [usecase](../internal/web/usecase/log_mask.go), [repo](../internal/web/adapter/out/postgres/log_mask_repo.go)), после мутаций публикуется reload. UI — «Настройки → Маскирование логов» ([LogMasks.tsx](../web-ui/src/pages/settings/LogMasks.tsx)) с живым превью замены. Замена поддерживает группы (`$1`), чтобы не терять смысл (`bot123:***`). Границы v1: тела не маскируются (план v2), только новые записи, RE2 без backref/lookaround. §95.9 — не дубль nginx-маскировки §94 (два стока). Миграция 0041 |
| **§96: повтор берёт тело из очереди, а не из журнала** | ✅ §96 | ТЗ — [96-replay-body-from-queue.md](sections/96-replay-body-from-queue.md), ветка `feature/replay-body-from-queue`. Боевой инцидент 31.08.2026 (`support`/`it_upr-vika`): «Повторить все сейчас» отправлял в приёмник ЖУРНАЛЬНУЮ копию тела, усечённую по `max_body_size` (50 000 рун + маркер `…(truncated)`) → приёмник отвечал 500 → запись снова неудачная → её повторяли снова; при этом §79.2 удалял строки оригинала, а §36.11 отменял его в DLQ, уничтожая последний след полного тела. Причина: гейт `IsTruncatedLogBody` был подключён только к §85.3, а `replayOne` шёл мимо. Решение — источник тела для ВСЕХ трёх операций повтора теперь оригинальный конверт Kafka: новый порт [AsyncOriginalReader](../internal/web/usecase/port/async_queue.go) (`FindOriginals`/`ProbeOriginals`) + реализация [kafkaadmin/originals.go](../internal/web/adapter/out/kafkaadmin/originals.go) (живой хвост от committed offset группы `-dlq-reprocess`, затем глубокий проход от offset по времени; фильтр по `key=node_path` до разбора; ранний выход по собранному набору). Выбор источника — [replayBody](../internal/web/usecase/replay.go): конверт → целая журнальная копия → отказ `original_unavailable` (async) / `body_truncated` (sync). `ReplayFailed` ищет конверты порциями по 20 и пропускает недоступные БЕЗ `qcancel` и без `cleanupReplayed`; `PlanPeriod`/`ReplayPeriod` проставляют `OriginalInQueue` пробой без чтения тел. Заголовки восстанавливаются из конверта (`Content-Type` + `forward_headers`), `auth_header` — нет. Метрика `nexus_replay_body_source_total{node,source=queue|log|rejected}`, `body_source` в ответе и аудите, `skipped_original_unavailable` в итоге массового повтора и в UI. `config.DLQGroup()` — имя группы репроцессора теперь считают одинаково Sender и Web. Миграций нет |
| **§92: кнопка «По умолчанию» на всех экранах с периодом** | ✅ §92 | ТЗ — [92-default-period-everywhere.md](sections/92-default-period-everywhere.md), ветка `feature/default-period-everywhere`. **Бэкенда не потребовалось** — хранилище §71 generic: два новых ключа ([user_preference.go](../internal/domain/user_preference.go) + зеркало [prefs.ts](../web-ui/src/lib/prefs.ts)), ни таблиц, ни миграций, ни эндпоинтов. `useTeamDefaultPeriod(teamId, key)` параметризован ключом — вызовы §71 не менялись. **Кнопка вынесена в общий компонент** [DefaultPeriodButton.tsx](../web-ui/src/components/ui/DefaultPeriodButton.tsx) (разметка переехала из Overview без изменения поведения — прежние кейсы §71 зелёные без правок). **Сквозной режим читает то же, что пишет:** `useTeamDefaultPeriod(allTeams ? "" : teamId)` — иначе кнопка «работала бы» (PUT уходит), а после перезахода период оказывался другим, и это выглядело бы как потеря настройки. **Грабля с disabled-состоянием:** первый вариант теста кликал по кнопке при периоде, равном сохранённому дефолту, и клик молча не давал мутации — кнопка законно неактивна; кейс переписан на дип-линк `?range=30d`. **Вторая грабля §86.4:** в сквозном режиме `/api/metrics/nodes` не зовётся вовсе (шапка считается `/api/metrics/totals`), поэтому наблюдать период в тесте надо по второму запросу — иначе кейс красный по ложной причине. Kafka-монитор пишет ГЛОБАЛЬНЫЙ преф (брокер один на инстанс). Вкладка «Метрики» вне рамок: §84.3 уже помнит вид per-node автоматически. Тесты: [DefaultPeriodButton.test.tsx](../web-ui/src/components/ui/DefaultPeriodButton.test.tsx) (4 кейса) + 2 кейса §92 в [Overview.period.test.tsx](../web-ui/src/pages/Overview.period.test.tsx), red-green проверен возвратом старого условия. Миграций нет |
| **§93: развёртывание без простоя (две реплики за балансировщиком)** | ✅ §93 | ТЗ — [93-ha-replicas.md](sections/93-ha-replicas.md), записка с оценками — [../docs/HA_ZERO_DOWNTIME.md](../docs/HA_ZERO_DOWNTIME.md), ветка `feature/ha-zero-downtime`. Закрывает обещание §9.1, жившее только на бумаге. **Топология оказалась хуже ожидаемой:** на пути sync-запроса три единые точки отказа подряд, потому что Web — реверс-прокси боевого `/api/v1/*` ([receiver_proxy.go](../internal/web/adapter/in/http/receiver_proxy.go)), а не только панель; поэтому реплицируются все три сервиса, а nginx уводит боевой трафик СРАЗУ в Receiver, мимо Web. **Код к репликам был готов, не готово развёртывание:** rate-limit, breaker, сессии, статусы узлов и отмена очереди уже в Redis, инвалидация и hot-reload — через pub/sub, sweeper'ы — на consumer-группах Kafka, NDJSON-fallback заменён Kafka-топиком, PromQL панели уже с `sum()`, `Ahead`-схема не мешает старту (§74.3), а Redis-лок под несколько реплик Web написан ещё для планировщика уведомлений (§20.4) — он и стал образцом. Deploy: [docker-compose.ha.yml](../deploy/docker-compose.ha.yml) (пары `web-1`/`web-2` вместо `deploy.replicas` — `--scale` пересоздаёт обе разом; общий сетевой алиас пары оставляет `config.yml` нетронутым; `image:` явно, иначе `images.sh` не найдёт образы для отката), [nginx.conf](../deploy/docker/nginx.conf), [rolling.sh](../scripts/deploy/rolling.sh), [prometheus.ha.yml](../deploy/prometheus.ha.yml). Код: балансировка gRPC `dns:///`+round_robin+health ([grpcsender](../internal/platform/grpcsender/client.go)), дренаж по SIGTERM ([healthcheck](../internal/platform/healthcheck/healthcheck.go) + секция `shutdown` в конфиге), имя реплики в логах §51 и теге Sentry ([replica.go](../internal/platform/bootstrap/replica.go)), локи периодических задач и аренда узлов §27 ([redislock](../internal/platform/redislock/redislock.go)). Неочевидности — §4.80. Миграций нет |
| **§52-доп: честный статус узла и спарклайн по данным** | ✅ | ТЗ — [52-node-degraded-status.md](sections/52-node-degraded-status.md) (§52.8/§52.9), ветка `feature/ha-zero-downtime`. Жалоба «узел Down, а в логах успешные запросы» оказалась ДВУМЯ разными дефектами. **Статус:** исход писался по каждому вызову, поэтому первая же 500 красила узел; хуже того, при параллельных запросах статус ставил тот, который ЗАВЕРШИЛСЯ последним, — в боевом случае это была медленная пятисотка (1493 мс) среди быстрых 200 (35 мс) той же секунды, а журнал отсортирован по времени НАЧАЛА, поэтому бейдж по экрану не объяснялся. Теперь «тяжёлые» отказы (транспорт/5xx) копятся в счётчике подряд идущих неудач, down выставляется с порога `sender.node_down_threshold` (дефолт 10), успех обнуляет, 4xx счётчик не трогает. Инкремент и решение — ОДИН Lua-скрипт ([nodestatus/redis.go](../internal/platform/nodestatus/redis.go)): при двух репликах Sender (§93) «прочитал → посчитал → записал» теряло бы отказы. `SetLastOutcome` возвращает эффективный исход, и по нему же выставляется Prometheus-гаудж — иначе бейдж и алерты трактовали бы узел по-разному. **График:** `Sparkline` красил ВСЕ столбцы по статусу, а высота означала общее число входящих — узел со статусом down выглядел как полностью нерабочий при 42 ошибках из 489. Данные уже возвращал `NodeChart` (`SeriesPoint.Errors`), их просто не отдавали клиенту; добавлено поле `spark_err` (аддитивно), столбец стал двухцветным как в `TrafficChart`, цвет перестал зависеть от статуса. Проверено на стенде: 8 ошибок из 28 → Degraded и частично красный столбец; десятый отказ подряд → Down; успех → ok. Тесты: 7 на порог (включая общий счётчик двух реплик), 2 на спарклайн (red-green проверен возвратом старого поведения). Миграций нет |
| **§93-доп: прозрачность балансировщика и подсказки** | ✅ | Правки по итогам вопроса «не режется ли что-то на nginx». Оказалось — режется: дефолты отбрасывают заголовки с подчёркиванием (а шина пробрасывает их по allowlist §24), отвечают 400 на заголовок больше 8 КБ и схлопывают `//` в пути (§39). Добавлены `underscores_in_headers on`, `large_client_header_buffers 8 32k`, `merge_slashes off`. Отдельно стенд нашёл, что `Host $host` теряет ПОРТ: CSRF-проверка §90.2 сравнивает Origin с Host, и на порту 8000 любой мутирующий запрос получал 403 — панель за балансировщиком не работала бы вовсе; передаётся `$http_host`. Прозрачность подтверждена сверкой «через nginx» против «напрямую в реплику» по 20 случаям плюс тело, коды и заголовки ответа, IP клиента. Карточка «Последние запросы» на вкладке «Обзор» переведена на `table-fixed` с `colgroup` (при авто-раскладке браузер игнорирует max-width у ячейки, и длинный подпуть §39 распирал таблицу за край), `LogUrlCell` вынесен из LogsTab в общий компонент. §92.3: подсказка кнопки «По умолчанию» на вкладках узла теперь называет скоуп («для вкладок узла в команде «X»») — раньше title передавали только рабочий стол и Kafka. Миграций нет |
| **§84.4: произвольный период кликом по дню, открытые границы** | ✅ §84.4 | **Переиспользован существующий компонент**, а не написан новый: поле даты журнала (§48.8, `react-day-picker`) уже реализовало нужный жест — клик по дню применяет дату+время и закрывает поповер. Переехало из `components/node/LogDateField.tsx` в [components/ui/DateTimeField.tsx](../web-ui/src/components/ui/DateTimeField.tsx) (импорт `Popover` — напрямую у соседнего модуля, а не через `./index`, иначе цикл). [PeriodPicker.tsx](../web-ui/src/components/ui/PeriodPicker.tsx): два нативных `datetime-local` и кнопка «Применить» заменены двумя `DateTimeField`; пикер ОДИН на все пять экранов (три вкладки узла, рабочий стол, мониторинг Kafka), поэтому правка одного файла закрыла «во всех местах». **Открытые границы разрешаются на КЛИЕНТЕ в момент выбора** (`resolveCustomPeriod` в [period.ts](../web-ui/src/lib/period.ts)): пустое «по» → сейчас, пустое «от» → `по − clickhouse_retention_days` узла ([nodeLookback.ts](../web-ui/src/lib/nodeLookback.ts); внешняя таблица §64 и экраны без единственного узла → потолок 30 суток). Наружу уходит всегда конкретный диапазон — контракт `Period` не приобретает третьего состояния, **API не меняется вовсе**, сужение по партициям §72.4 сохраняется автоматически, а разрушающая очистка очереди §34.4 неявного окна получить не может по построению (в плане была отдельная блокировка кнопки — не понадобилась). Обе границы пустые → период НЕ применяется: один клик по «Произвольный» не имеет права запускать полный скан. Подпись под пикером показывает фактическое окно **абсолютными датами**: граница разрешается один раз, и «до сейчас» через час было бы неправдой. `resolveCustomPeriod` живёт в `lib/`, а не в файле компонента — иначе `react-refresh/only-export-components` валит CI с `--max-warnings=0`. 14 тестов |
| **§84.5–84.6: латентность во времени и пульс узла** | ✅ §84.5/§84.6 | **Латентность.** Новый `NodeLatencyChart` ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)) — `quantile(0.5)/(0.95)` по тем же интервалам, `WHERE` из общего `searchConds` (§79.4). Отдельный метод, а не поле `SeriesPoint`, и это не стиль: у трафика единица — ЗАПИСИ, у латентности — ПОПЫТКИ (§79.5), а слить запросы нельзя технически — основной режим `NodeChart` уже свернул строки в записи через `GROUP BY ID`. Идут **конкурентно** в `errgroup` под общим `metricsTimeout=10s`, каждая горутина с `safego.Recover` — время ответа вкладки не растёт, нагрузка на CH на этой вкладке примерно удваивается (цена названа в ТЗ). Деградация НЕ каскадит: ошибка латентности гасит только свой график (`latency_available=false`), падение графика трафика по-прежнему гасит вкладку — оба направления закреплены тестами. **Пустой интервал — РАЗРЫВ линии, а не ноль:** «узел отвечал за 0 мс» — прямая ложь, а на узле с ночным простоем таких интервалов половина окна; отличить пустой интервал от настоящего нуля можно только по `attempts`, поэтому оно и есть в `LatencyPoint`. [LatencyChart.tsx](../web-ui/src/components/ui/LatencyChart.tsx) — SVG без внешних зависимостей (как `TrafficChart`), одиночная точка рисуется маркером: линию по ней не построить, а всплеск на пустом окне пропасть не должен. **Пульс.** `last_seen_ms` добавлен в ТОТ ЖЕ `SELECT` метода `NodeKPI` — дополнительного прохода по таблице не появляется. Guard `if total == 0 { lastSeen = 0 }` обязателен: `max()` по пустому набору даёт эпоху, и UI показывал бы «56 лет назад». Значение честно называется «в выбранном окне и под текущими фильтрами»; «за всё время» — отдельное действие по клику, потому что полный `max` без окна это скан колонки на всей таблице (внешняя §64 — 10,2 млн записей) при поллинге раз в 12 с. Порог тишины — **три шага графика**: три пустых столбца подряд ровно то, что видно глазом рядом, поэтому подпись и картинка не расходятся; отдельной настройки порога нет. 13 фронт-тестов + 4 в usecase |
| **§84.7: исход последнего вызова в шапке узла** | ✅ §84.7 | **Закрывает follow-up §52** («Runtime-бейдж на странице узла — возможный follow-up»): до раздела трёхсостоянье ok/degraded/down было только на рабочем столе, а на странице узла стоял лишь конфигурационный статус — «Активен» показывался и у узла, к которому доставка не проходит вовсе. Правило приоритета Redis→Prometheus вынесено из `applyLastOutcomes` в общий [LastOutcomeResolver](../internal/web/usecase/last_outcome.go): у него появился ВТОРОЙ потребитель, а две копии одного правила разошлись бы — таблица показывала бы одно, страница узла другое. Новый узкий [NodeRuntimeUsecase](../internal/web/usecase/node_runtime.go) — калька `NodeBreakerUsecase` по той же причине: тащить Redis и Prometheus в `NodeUsecase` ради бейджа нельзя, иначе от них начинает зависеть выдача самой карточки узла. `GET /api/nodes/:id/runtime` — **все роли** (наблюдателю тоже надо понимать, почему узел молчит) и **без** `KafkaRateLimit`: это чтение Redis/Prometheus, к очереди отношения не имеющее. **Ключевое правило: `available=false` («неизвестно») НЕ красится в ok** — зелёная плашка при неизвестном состоянии успокаивает ровно тогда, когда этого делать нельзя; `ok` бейджа не рисует вовсе (шум на каждом узле). При этом рабочий стол сохраняет прежнюю трактовку «неизвестно = ok»: там у бейджа нет пустого состояния, каждая строка таблицы обязана иметь тон — расхождение осознанное и закреплено тестом. `BreakerCard` инвалидирует `NODE_RUNTIME_KEY`: сброс защиты §81.4 гасит и персистентный бейдж, иначе кнопка выглядела бы безрезультатной прямо на том экране, где её нажали. 7 тестов в usecase + 6 фронтовых |
| **§84.8: ёмкость партиции и голова очереди — узловой срез §80.2** | ✅ §84.8 | **Ни одного нового запроса ни к ClickHouse, ни к Kafka** — оба показателя выводятся из данных, которые вкладка уже получила. Доля ёмкости ([queueCapacity.ts](../web-ui/src/lib/queueCapacity.ts), формула §80.2 дословно: `запросов × p95 / период`) считается из ответа метрик на вкладке «Метрики» и показывается ТОЛЬКО у async-узлов: у sync очереди нет вовсе и число было бы бессмысленным. «Ждут / партиция / возраст головы» — на вкладке «Очередь» из уже загруженного `aq-list`: список отсортирован по времени приёма, значит `items[0]` и есть голова; `capped` даёт «50+», а не «50», иначе число читается как точное. Разнесено по двум вкладкам не из эстетики: ёмкость — производная метрик, её должен видеть наблюдатель; чтение очереди — manager+ и под `KafkaRateLimit`. Между блоками взаимные ссылки: ёмкость отвечает «помещается ли узел», а «сколько ждёт прямо сейчас» — только вкладка «Очередь». `capacityShare` возвращает **null, а не ноль**, когда считать не из чего: ноль читался бы как «узел ничего не занимает». Тесты — на боевых числах §80.1 (`acs_sigur` 136 %, `omnichannel` 2,7 %). **Найдено прогоном на стенде, не тестами:** пояснение к формуле печатало «p95 0.0 с» — `(p95Ms/1000).toFixed(1)` превращает всё быстрее 50 мс в ноль, а жёсткие часы превратили бы окно в 30 минут в «0 ч»; строка, ОБЪЯСНЯЮЩАЯ расчёт, читалась как «p95 нулевая». Единица выбирается по величине (`fmtCapacityDuration`), подписи локализованы ключом `metrics.capacity.units` — тем же приёмом, что у размеров (`fmtSize`). Тесты на боевых числах остались зелёными, потому что боевые величины — секунды; ловится это только глазами на быстром узле. **Границы против §80:** общий экран «Кто держит очередь», метка `partition="-1"` у `nexus_kafka_lag`, возраст головы как МЕТРИКА Prometheus, параллелизм внутри партиции и `async_max_inflight` остаются за §80 — §84 ничего не меняет в Sender'е |
| **§84.9: ответ шины в журнале — пересчёт по текущему шаблону** | ✅ §84.9 | **Диагноз:** после §83 узел отвечает клиенту своим телом (`{"confirmedLogId": 79569}`), а журнал в колонке «Ответ» показывает ответ ПРИЁМНИКА (`{"response":"success","code":"2000"}`). Отрендеренный ack не сохраняется **нигде**: не кладётся в конверт Kafka, не попадает в Kafka-headers, не пишется в Debug-лог, а Receiver с ClickHouse не соединён вовсе — `c.Data(...)` в сокет и `return`. §83.3 при этом вводит `X-Nexus-Id` именно чтобы «саппорт связал ответ с записью журнала»: связать можно, а увидеть нечего. **Решение — пересчёт, а не хранение:** колонка в CH означала бы миграцию на всех боевых таблицах узлов, а на внешних §64, которыми Nexus не владеет, её нельзя добавить в принципе. `GET /api/nodes/:id/log/:logId/ack` ([logs_ack.go](../internal/web/usecase/logs_ack.go)) — метод на `LogsUsecase`, потому что у него уже есть `resolveNode` со всеми гейтами; отдельный usecase дублировал бы их. **Все роли** со `logs:read`, а не manager+: `ack-preview` требует manager, потому что исполняет ПРИСЛАННУЮ спеку на произвольном образце, здесь же — спека уже сохранённого узла на теле, которое запрашивающий и так видит в журнале. **Гейт показа — по ЗАПИСИ (`record.type`), а НЕ по `root_method` узла** ([logAckGate.ts](../web-ui/src/lib/logAckGate.ts)): `root_method` описывает узел СЕЙЧАС, а не тот прогон — у sync-узла спека законна (через API; на паузе она и работает, §3.6), и его записи async-прогонов шаблон реально формировал, а наивный гейт по `root_method` прятал бы их и показывал обратное; кейс закреплён тестом с обеих сторон. **После новой редакции §83.5** (перевод в sync ЧЕРЕЗ ФОРМУ выключает спеку) у переведённого узла блок пропадает и для старых записей — пересчитывать нечем; гейт при этом остаётся верным для всех прочих сочетаний и не меняется. Не восстановимо из журнала и об этом сказано в UI: `Content-Type` входящего запроса (подставляется пустой → трактуется как JSON) и **query** (колонка `parameters` несёт query ИСХОДЯЩЕГО URL — подставлять её было бы прямой ложью, вместо этого предупреждение `query_not_stored`). Признак «шаблон мог измениться» — `node.updated_at > date_request`, консервативно: меняется от любой правки узла, но ложная тревога дешевле молчания. Битая спека в БД → `ok=false`, а не 500. 13 тестов в usecase + 12 фронтовых |
| **§84: наблюдаемость узла** | ✅ §84 | Раздел ТЗ [84-node-observability.md](sections/84-node-observability.md). Диагноз на боевом `acs_sigur` (24 ч): Всего=Доставлено=4558, Ошибок 0 — и при этом p95 30 490 мс, p99 142 958 мс при медиане 5333 мс. Ошибок ноль потому, что `timeout_ms=600000` не даёт ничему упасть: запросы не отказывают, а ЖДУТ; p99 — ожидание в очереди (в пик 1114 записей за полчаса, доля ёмкости партиции 161 %, §80.1), а не медленный приёмник. Состав: §84.1 круглый авто-шаг (лестница делителей 86400, снап ВВЕРХ), §84.2 период и шаг в адресе своими ключами `range/step/mfrom/mto`, §84.3 вид per-node в префах §71 без миграции + конкретный дефолт шага вместо «Авто», §84.4 произвольный период кликом по дню и открытые границы, §84.5 график латентности p50/p95, §84.6 пульс узла, §84.7 runtime-бейдж §52 в шапке (закрывает follow-up §52), §84.8 узловой срез §80.2, §84.9 пересчёт ответа шины в журнале. Миграций нет, новых ключей конфига нет |
| **§85: повторная отправка запросов из логов за период** | ✅ §85 | Раздел ТЗ [85-replay-from-logs.md](sections/85-replay-from-logs.md). Реализован целиком (Phase 85.1–85.11), слит в `dev`, выходит в 1.27.0 |
| **§85.3: пригодность записи к повтору — одна доменная функция** | ✅ Phase 85.2 | [replay_eligibility.go](../internal/domain/replay_eligibility.go): `ReplayCandidate` (проекция записи БЕЗ полного тела — начало, признак маркера, обе длины), `ClassifyReplayCandidate` на пять причин отказа, `ReplayEffectiveMethod`. **Порядок проверок значим и закреплён отдельным тестом:** §68-плейсхолдер короче исходного тела, поэтому размерный признак усечения на нём срабатывает ВСЕГДА — переставь проверки, и оператор увидит «тело обрезано» вместо «multipart», уйдя разбираться в `max_body_size`, где всё в порядке. **Усечение определяется ДВУМЯ признаками, и это не перестраховка:** маркер точен, но виден только с хвоста тела; `request_size > length(request)` работает по метаданным, но у многобайтового текста, обрезанного на одну руну, копия С МАРКЕРОМ (14 байт) оказывается ДЛИННЕЕ оригинала и признак молчит — случай в таблице тестов. Ошибка в безопасную сторону: лишний отказ дешевле отправки обрезанного тела наружу. **Маркер `…(truncated)` переехал в домен** из приватной константы `sender/usecase` — его ставит Sender, а читает web-слой, и две копии одной строки разъехались бы при первой правке (тесты Sender'а теперь сверяются с доменной константой, то есть инвариант закреплён). `ReplayEffectiveMethod` заодно снял дубль правила §39/§40/§34.5 из `replayOne` |
| **§85.5: страница кандидатов повтора — ASC-курсор и отсев повторных прогонов** | ✅ Phase 85.3 | Порт `LogReader.ReplayCandidates` + `port.ReplayCursor` ([log_reader.go](../internal/web/usecase/port/log_reader.go)), реализация — [adapter/out/clickhouse/log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go). Порядок ASC не для красоты: он сохраняет последовательность запросов при реинжекции (ключ Kafka = путь узла, одна партиция, §3.5) И совпадает с началом ключа сортировки таблицы (`date_create, date_request, …`), поэтому курсор двигает точку старта, а `LIMIT` останавливает чтение — страница не стоит скана окна. Тела целиком не читаются: `substringUTF8(request,1,512)` (детект §68), `endsWith(request, маркер)`, `length(request)`, `request_size`. **Главная находка блока — ловушка курсора, которую не ловит ни `Count`, ни `LIMIT 1 BY ID`:** строка ≠ запись (§78.6), `LIMIT 1 BY ID` схлопывает прогоны только ВНУТРИ страницы, поэтому запись с прогонами t1 и t3 (DLQ-репроцессинг §36, доставка с паузы §3.6, `ttl_expired`) попадает в первую страницу строкой t1, курсор уходит за t1, а во вторую та же запись проходит строкой t3 — и уезжает получателю ДУБЛЕМ, ровно как в §79 (15 дублей в 1С). Лечится `dropSeenEarlier`: ограниченная проба `min(date_request)` по ID страницы (приём §79.1 «платим по малой стороне»), строка отбрасывается при `date_request ≠ first_ts`. Курсор при этом встаёт на последнюю ПРОСМОТРЕННУЮ строку, а не отданную — иначе следующая страница начнёт с отсеянных и обход зациклится. Integration-тест `TestClickHouse_ReplayCandidates_MultiRunRecordOnce` **красный на коде без пробы** (проверено: запись отдаётся 2 раза), второй тест сверяет хронологию и все пять причин §85.3 на реальных телах. Запись «нет в пробе → оставляем» намеренная: молча терять запрос из-за расхождения двух чтений хуже дубля, который оператор увидит |
| **§85.5/§85.7: usecase повтора за период — батч с курсором и предпросмотр** | ✅ Phase 85.4 | [replay_period.go](../internal/web/usecase/replay_period.go) — отдельный файл, а не продолжение `replay.go` (тот уже держит одиночный повтор и «Повторить все неудачные»); общее у них ровно одно — `replayOne`, оно переиспользуется как есть. `PlanPeriod` **read-only**: `Count` даёт общее число записей окна, страницы кандидатов разбираются до конца окна либо до `replayPeriodPreviewCap=2000`, флаг `exact` честно говорит «числа неполные» — проценты НЕ экстраполируются (непригодные кучкуются по времени, оценка звучала бы точнее, чем есть); диспетчер не вызывается ни разу (тест с падающим на любой вызов диспетчером), аудита нет (довод §84.9.4). `ReplayPeriod` — один батч до `replayPeriodBatchCap=200`, отказ одной записи батч не роняет (как `ReplayFailed`). **Гейты проверяются ДО чтения журнала** (`preparePeriod`): sync-узел → `ErrReplayPeriodSyncNode`, выключенное логирование → `ErrNodeLogsNotConfigured`, `log_request_body=false` → `ErrReplayPeriodBodyNotLogged` (иначе оператор запускал бы прогон, отказывающий на каждой записи по одной за раз), **обе границы окна обязательны** → `ErrReplayPeriodNoWindow` (без верхней прогон затягивал бы в набор собственные повторы §85.6, без нижней — всю историю узла); тест проверяет, что при отказе гейта журнал не читается вовсе. Аудит `node.replay_period` — **на КАЖДЫЙ батч** с полем `done`: прогон обрывается закрытием вкладки, и след обязан остаться от того, что успело уйти. Свой ключ rate-limit `replay_period:<user>` и свой лимит (`WithPeriodRateLimit`, дефолт 60/мин): общий лимит одиночного повтора 10/мин остановил бы цикл после десятого батча. Уход клиента (`ctx.Err()`) прерывает батч **без выдачи курсора** — часть страницы уже отправлена, и продолжение с этого места пропустило бы остаток |
| **§85.9: HTTP — два маршрута повтора за период** | ✅ Phase 85.5 | `POST /api/nodes/:id/logs/replay-period/{plan,run}` ([replay_handler.go](../internal/web/adapter/in/http/replay_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go)) — `authedManager` + `RequireSessionOnly` (как одиночный replay). **Маршруты намеренно ВНЕ группы `/nodes/:id/async-queue`**: на ней висит `KafkaRateLimit` (60/мин на пользователя, поставлен ради peek'а Kafka), а здесь клиент крутит цикл батчей — общий лимит очереди зарубил бы его на середине; тот же довод, по которому вне группы стоят маршруты breaker §81.4. **Фильтр приходит query-параметрами, а не в теле:** их разбирает ТОТ ЖЕ `logQueryFromContext`, что список и счётчик журнала, поэтому фронт переиспользует существующий `logsFilterParams` как есть, а второго парсера тех же фильтров (и расхождения «предпросмотр показал одно, отправка взяла другое») не появляется. В теле — только курсор, размер батча и флажок §85.6. Найдено при сборке: без явного `parseSearch` в usecase непустой `q` молча не действовал бы — адаптер читает только `QExpr`; закрыто тестом «QExpr обязан быть разобран». Маппинг отказов: 400 нет окна / битый поиск, 409 отключён или синхронный, 422 тело не логируется, 429 rate-limit, 503 логи недоступны (мягкая деградация как §67), `context.Canceled` (кнопка «Остановить») — Debug без ответа и без Sentry. 4 handler-теста |
| **§85.10: интерфейс — кнопка и диалог повтора из логов** | ✅ Phase 85.6 | [ReplayPeriodDialog.tsx](../web-ui/src/components/node/ReplayPeriodDialog.tsx) — три шага (период и фильтр → предпросмотр → прогон) в одном `Modal`. Период — существующий `PeriodPicker` (§84.4 уже даёт клик по дню с закрытием календаря, своего контрола не понадобилось), фильтр — существующая панель `LogsAdvancedFilters` с `showDates={false}` (приём §79.4: два контрола, пишущих в одни границы, затирали бы друг друга). **Цикл батчей крутит клиент:** курсор приходит с сервера и уходит обратно, поэтому продолжение не переотправляет ушедшее; границы окна берутся ИЗ ПЛАНА, а не пересчитываются на каждом батче (§85.6 — иначе «по сейчас» затягивает в набор собственные повторы). Предохранитель `MAX_BATCHES`: если сервер перестанет двигать курсор, цикл остановится явно, а не будет крутиться молча. Кнопка — в шапке вкладки рядом с пикером (операция идёт по ВСЕМ записям окна, а не по подмножеству «неудачных»), гаснет с КОНКРЕТНОЙ причиной (логи выключены / нет таблицы / тело не пишется), у sync-узла её нет вовсе. `api.post` получил `params`/`signal` — фильтр уходит теми же query-параметрами, что у журнала. **Дефект, найденный собственным тестом:** Go отдаёт пустой срез как `null`, и диалог падал на `rows.length` ровно в самом безобидном случае «отправлять нечего»; закрыто с обеих сторон — сервер инициализирует списки пустыми, клиент страхуется `?? []`. 30 ключей i18n ru/en, 8 фронт-тестов, бандл пересобран |
| **Логин `admin` на форме входа — только на стенде** | ✅ Phase 85.8 | [Login.tsx](../web-ui/src/pages/Login.tsx) инициализировал поле литералом `"admin"` БЕЗУСЛОВНО: боевая форма входа называла имя существующего привилегированного аккаунта каждому, кто открыл страницу (`web.login_rate_limit_per_min` ограничивает темп перебора, но не подсказку). **Признак среды взят с уже существующего запроса:** `GET /api/version` — единственный `/api` без авторизации ([app.go](../internal/web/app.go), вне групп), и SPA его и так делает ради версии в футере (`queryKey: ["version"]`), поэтому нового сетевого вызова не появилось. **Новый ключ `web.dev_mode`, а НЕ переиспользование `allow_version_override`:** тот — разрешение на конкретную настройку §34.3, а не маркер среды; связав их, получили бы, что выключенный на стенде override версии молча уносит подсказку логина, а включённый на боевой установке возвращает `admin` на страницу входа (обе стороны закреплены тестом `TestVersionHandler_DevModeIsIndependentOfOverrideGate`). Дефолт `false` делает ОТСУТСТВИЕ ключа в боевом `config.yml` боевым поведением — важно, потому что этот файл выкатывается вручную. Префилл ставится эффектом и только пока поле не тронуто (`touched`-ref): ответ `/api/version` асинхронный, и без этого он затирал бы уже набранный логин — отдельный тест. Ошибка/недоступность `/api/version` → префилла нет (fail-closed = боевое поведение). `autoComplete="username"` сохранён: на своей машине браузер подставит СОБСТВЕННЫЙ логин оператора. 5 фронт-тестов, из них три красные на старом коде (проверено) |
| **§85: ревизия перед сдачей — что нашла** | ✅ Phase 85.9 | Четыре находки, ни одну из которых не поймали ни тесты, ни линт. **(1) Прерванный батч не оставлял следа в аудите.** Ранний `return` по `ctx.Err()` выходил ДО `audit.Log`, а сама запись пошла бы с уже отменённым контекстом — то есть §85.9 обещал след от ушедшего получателю, а его не было. Теперь цикл размыкается через `aborted`, аудит пишется всегда и с `context.WithoutCancel` (§6), в деталях появился флаг `aborted`; тест красный на прежнем коде (`[] should have 1 item`). **(2) ТЗ обещало ключ конфига, которого не было:** `WithPeriodRateLimit` существовал, но нигде не подключался — лимит всегда был дефолтным. Добавлен `web.replay_period_rate_limit_per_user_per_min` (дефолт 60), проброшен в `app.go`, имя в ТЗ приведено к фактическому. **(3) Мёртвое поле** `ReplayCandidate.Reason` читалось из ClickHouse и не использовалось — убрано из проекции и структуры. **(4) Предпросмотр ограничивался только числом РАЗОБРАННЫХ записей:** страница, целиком ушедшая в отсев §85.5.1, счётчик не двигает, и окно с большой долей многопрогонных записей превращало предпросмотр в полный обход журнала — добавлен второй потолок по числу проходов со своим тестом. Попутно: четыре файла фронта уехали в CRLF (правка через Python на Windows) и давали 1646 строк diff в `QueueTab.tsx` вместо 36 — нормализовано в LF, содержимое бандла от этого не изменилось (тот же хеш `index-VtCSjSbj.js`) |
| **Идентификатор записи после повтора — из ответа шины, а не выдуманный** | ✅ Phase 85.10 | `ReplayResult.NewLogID` был `uuid.NewString()` ([replay.go](../internal/web/usecase/replay.go)) — значение выглядело как идентификатор новой записи, но не было связано ни с чем: диалог показывал его оператору, а аудит `node.replay` ссылался на **несуществующую запись**. Теперь `replayedLogID` берёт его у самой шины по двум источникам, и порядок между ними значим: заголовок `X-Nexus-Id` (Receiver ставит его, когда отвечает по шаблону §83 — тело там пишет оператор, и идентификатора в нём может не быть) → тело штатного async-ответа `{"result":true,"id":"<uuid>"}` (и 202 узла на паузе §3.6). **Тело SYNC-ответа не разбирается никогда:** это ответ ПРИЁМНИКА, и его собственное поле `id` (заказ, документ) уехало бы в аудит как идентификатор записи журнала; по той же причине значение из тела обязано быть валидным UUID — форма шины гарантирует именно его. Правки Receiver'а не потребовалось: диспетчер и так копирует все заголовки ответа. Поле стало `omitempty`, пустое в аудит не пишется, диалог пустую моноширинную строку не рисует (она читалась бы как «запись есть, но безымянная»). 5 подтестов на источники + 2 фронтовых; красные на прежнем коде. **Находка гейта:** `make test-integration` показал, что `TestReplay_E2E_ClickHouse` **фиксировал ровно эту неправду** — узел в нём синхронный, а ассерт `require.NotEmpty(NewLogID, "replay must return new id")` проверял, что выдуманный UUID непуст; `make test` и vitest этого не видели, потому что тест только integration-овый. Ассерт приведён к контракту (`Empty` + «в аудит пустое не пишется») и дополнен вторым прогоном с `X-Nexus-Id` — сквозная проверка положительного случая; на прежнем коде тест теперь красный. Попутно снят `defer dispatcher.mu.Unlock()`, который на втором прогоне дал бы самоблокировку |
| **§86: сквозной просмотр узлов всех команд («Все команды»)** | ✅ §86 | Раздел ТЗ [86-all-teams-scope.md](sections/86-all-teams-scope.md). Реализован целиком (Phase 86.1–86.12), слит в `dev`, вышел в 1.27.0. Порядок строк пересмотрен позже — §86.10/§86.11 (ветка `feature/all-teams-ranking`). Миграций нет, Receiver и Sender не затронуты |
| **§86.10: порядок «проблемные первыми» + заморозка** | ✅ Phase 86.10-A/B | **Отправная точка — наблюдение, а не требование: данные для ранга уже считались и выбрасывались.** `OverviewTotalsScoped` звал `NodesOverviewScoped(...).Totals`, то есть делал полный проход по всем узлам скоупа с KPI **и спарклайном** на каждый, а наружу отдавал одну сумму. Отсюда всё решение: `/api/metrics/totals` начал отдавать лёгкий срез `nodes` (`node_id`, `in`, `out`, `errors`, `last_outcome`), а сам проход **перестал строить спарклайны** — до двух CH-запросов на узел превратились в один, и срез обошёлся ДЕШЕВЛЕ прежнего поведения. Отдельного эндпоинта нет намеренно: второй маршрут = второй полный проход со своим кешем. Флаг `withSpark` наружу не вынесен (`nodesOverviewScoped`, [metrics.go](../internal/web/usecase/metrics.go)) — «обзор без графиков» имеет смысл только на пути шапки; доходит до **обеих** веток источника, в Prometheus-ветке снимает общий range-запрос рядов. Кеш и `singleflight` хранят сумму И срез (`ScopedTotals`, [metrics_totals.go](../internal/web/usecase/metrics_totals.go)): иначе каждое второе автообновление отдавало бы шапку с пустым срезом, и клиент терял бы порядок. **Главное решение фронта — срез стал БАЗОВЫМ СЛОЕМ карты метрик, а не вторым источником** ([Overview.tsx](../web-ui/src/pages/Overview.tsx)): порционные пачки ложатся поверх, и ранг, бейдж и фильтр по статусу считает один и тот же `nodeVariant` по одним числам. Разъедься источники — узел вставал бы наверх с серым «неизвестно», и верх списка читался бы как случайный; попутно статус стал честен для всего списка без прокрутки. `p95=0` и пустой спарклайн в базовом слое безопасны ровно потому, что `fmtMs(0)` уже рисует «—», а в таблице колонки p95 нет вовсе — проверено по коду отрисовки до правки. **Замораживается КАРТА «id → позиция», а не готовый список:** фильтры отсеивают ту же карту, поэтому взаимный порядок переживает поиск/метод/статус, а вид (таблица/карточки) в ключ заморозки не входит — переключение представления список не перетасовывает. Ранг считается СТРОГО из среза, а не из объединённой карты: та подмешивает порционные пачки, и порядок зависел бы от того, докуда долистали. Тесты: срез приходит на все узлы включая узлы без CH-таблицы (иначе выпали бы из карты рангов); `NodeChart` на пути шапки не зовётся **ни разу**, на пути обзора зовётся для каждого узла с трафиком — эта пара и есть red/green-доказательство флага; 9 фронт-тестов в [Overview.rank.test.tsx](../web-ui/src/pages/Overview.rank.test.tsx). **Грабля фронт-теста:** подписи бейджа статуса и опций выпадающего фильтра — одни и те же i18n-ключи, поэтому `getByText("overview.status.down")` находит `<option>` мгновенно, ещё до прихода данных; а проверка «нет серых» верна на пустом списке. Обе зеленели, ничего не проверив — отсюда фильтр по `tagName` и совмещённое ожидание |
| **§86.10: находки стенда и ревизии** | ✅ Phase 86.10 | Три дефекта, которых не увидели ни unit-тесты, ни линт. **1. Стенд (панель сетевых запросов):** одно касание фильтра по статусу добавляло в порционную загрузку ВЕСЬ список через `preload` (82 узла сверх видимых, двумя запросами), и дальше он пересчитывался в ClickHouse каждые 12 с — в том числе ПОСЛЕ сброса фильтра, потому что пачки заморожены и не удаляются. Со срезом preload на обычном пути не нужен; гейт добавлен, а не удалён — без среза фильтр снова замкнулся бы в круг. Второе условие гейта — `totalsQ.isPending`: без него на первом рендере срез ещё не пришёл, preload успевал выстрелить, и одна преждевременная пачка оставалась в автообновлении навсегда. Замер: было 82 id каждые 12 с, стало — один запрос на 2 видимые строки. **2. Ревизия:** карта рангов строится из `nodesQ.data`, а тот приходит УЖЕ суженным поиском (`/api/nodes?search=`), при этом поиск не входил в ключ заморозки — открыв страницу по ссылке с поиском и сбросив его, оператор получал бы наверху горстку найденного ранее, а остальное алфавитом (в карте этих узлов просто нет). Поиск добавлен в ключ; метод и статус фильтруют на клиенте и в ключ НЕ входят — в этом и смысл заморозки карты, а не готового списка. Там же нашлось, что карта могла замораживаться, когда срез уже есть, а список узлов ещё не приехал (пустая карта навсегда) — теперь не морозим без обоих входов. **3. Комментарии-ложь:** `statusFilterPending` и `preload` описывали прежний механизм («окно длится, пока догружаются пачки preload»), хотя на обычном пути оно теперь длится до прихода среза. **Грабля теста, стоившая двух заходов:** первая версия проверки поиска «сбрасывала» его пересозданием страницы, но поиск переживает перемонтирование через зеркало сессии (§54) — сценарий на клиенте не происходил. Мок списка узлов переведён на фильтрацию по РЕАЛЬНОМУ параметру запроса, а сброс — на очистку поля ввода |
| **§86.11: кнопка «Обновить» рядом с «Авто»** | ✅ Phase 86.11 | Иконка без подписи справа от переключателя автообновления ([Overview.tsx](../web-ui/src/pages/Overview.tsx), ключ `overview.refresh` в ru/en). Не украшение: заморозка §86.10 без ручного обновления — ловушка (оператор видит устаревший порядок и ничего не может сделать), а при выключенном «Авто» это вообще единственный способ обновиться. Перезапрашиваются **существующие** запросы (`refetch`), а не `invalidateQueries` по экрану: лишний полный проход по всем узлам скоупа дорог. Порции именно перезапрашиваются — хук получил `refresh()` по префиксу `queryKey` ([visibleMetrics.ts](../web-ui/src/lib/visibleMetrics.ts)); сброс пачек погасил бы показанные цифры в «—» и заставил бы ClickHouse считать те же узлы заново. **`rankEpoch` инкрементится ПОСЛЕ прихода нового среза**, и это не мелочь: сделай наоборот — карта пересобралась бы по старым числам, а новые её уже не пересобрали бы, ключ заморозки к тому моменту совпал. Кнопка `disabled` со спиннером (серия кликов не должна давать шквал запросов), на состояние «Авто» не влияет. Тест на природу элемента (`BUTTON` + доступное имя из `aria-label`, пустой `textContent`) — урок §79: тесты на клик природу элемента не проверяют |
| **§87: роль доступа «Оператор»** | ✅ §87 | Раздел ТЗ [87-operator-role.md](sections/87-operator-role.md). Четвёртая роль UI между `viewer` и `manager`: эксплуатация узла (вкладка «Очередь» целиком, статус, сброс breaker, повторы из логов, чтение аудита) без права создавать, менять и копировать узлы. Реализована целиком (Phase 87.1–87.4). Одна миграция ([0035_user_role_operator](../migrations/0035_user_role_operator.up.sql)), Receiver и Sender не затронуты (роль UI им не видна) |
| **§87.2: иерархия рангов выдержала вставку роли в середину** | ✅ Phase 87.1 | [enums.go](../internal/domain/enums.go) `Rank()` 3/2/1/0 + [roles.ts](../web-ui/src/lib/roles.ts). Возможности оператора — строгое подмножество менеджерских, поэтому решётка прав не понадобилась: гейты опущены с `RequireMinRole(manager)` на `RequireMinRole(operator)`. Сдвиг `manager` 1→2 / `admin` 2→3 безопасен — ранг вычисляемый (грепом подтверждено, что `rank` не персистится нигде) |
| **§87.3: группа `authedOperator`, 7 перенесённых регистраций** | ✅ Phase 87.2 | [routes.go](../internal/web/adapter/in/http/routes.go): `PATCH /nodes/:id/status`, `POST /logs/:id/replay`, `replay-period/{plan,run}`, `POST /nodes/:id/breaker/reset`, вся группа `/nodes/:id/async-queue/*`, `GET /audit` + `/audit/export.csv`. Тесты — [routes_role_test.go](../internal/web/adapter/in/http/routes_role_test.go) поверх настоящего `RegisterAPI` |
| **§87.4: миграция 0035 и потеря роли при откате** | ✅ Phase 87.1 | [0035_user_role_operator.up.sql](../migrations/0035_user_role_operator.up.sql) расширяет CHECK; `down` переводит операторов в `viewer`. `scripts/release/rollback_info.py` эту потерю НЕ пометит (смотрит `DROP TABLE`/`DROP COLUMN`/`DELETE FROM`, а тут `UPDATE`) — строку «Откат» в CHANGELOG пишем вручную |
| **§91: журнал аудита (объём выдачи, видимость, состав событий)** | ◐ в работе | Раздел ТЗ [91-audit-scale-and-scope.md](sections/91-audit-scale-and-scope.md). Сделано: keyset-пагинация и `GET /api/audit/count` (91.1), глобальные записи админу и снятие `team.switch` + запись `user.logout` (91.2), подгрузка по скроллу и счётчик в UI (91.3). Неочевидности — §4.79. Две миграции (0038 индекс, 0039 очистка `team.switch` — необратима) |
| **§91.1: курсор сравнивает кортеж `(created_at, id)`** | ✅ Phase 91.1 | [audit_repo.go](../internal/web/adapter/out/postgres/audit_repo.go): метки времени не уникальны, поэтому сортировка только по `created_at` оставляла порядок среди равных неопределённым — на границе страницы записи дублировались бы или терялись. Отсюда же `ORDER BY created_at DESC, id DESC` и индекс миграции 0038. Есть integration-тест на 25 записях, половина с ОДИНАКОВОЙ меткой |
| **§91.1: WHERE у списка и счётчика — общий код** | ✅ Phase 91.1 | `auditWhere()` собирает условия один раз для `List` и `Count`. Разъехавшись, они дали бы «показано N из M», где M посчитано по другому множеству — ровно тот класс расхождений, что §72.2 у логов узла |
| **§91.2: глобальные записи (`team_id IS NULL`) — только админу** | ✅ Phase 91.2 | Флаг `IncludeGlobal` в фильтре, ставится при роли admin. Для operator/manager журнал остаётся командным: глобальные записи — это входы ВСЕХ пользователей инстанса, и расширять видимость за пределы членств нельзя (ср. §62.3, §76.5, §86.2) |
| **§91.2: `team.switch` удалён, `user.logout` добавлен** | ✅ Phase 91.2 | Переключение команды выполняется и переходами по ссылкам §76/§89.3 — записи появлялись без участия пользователя; вдобавок уходили со СТАРЫМ `team_id` (actor вычисляется до смены) и в журнале новой команды не показывались. Миграция 0039 чистит накопленное и **необратима**. `user.logout` закрывает расхождение с §7.13: константа была объявлена с самого начала, но не использовалась |
| **§91.3: `useInfiniteList` — общее ядро подгрузки** | ✅ Phase 91.3 | [useInfiniteList.ts](../web-ui/src/lib/useInfiniteList.ts): накопление страниц, дедуп, порог 200px, догрузка при недоборе высоты, схлопывание кеша. `useInfiniteLogs` стал обёрткой с ПРЕЖНИМ API, поэтому LogsTab/QueueTab и их 15 тестов не тронуты — рефакторинг проверяется их зеленью |
| **§90: безопасность внешних сервисов (секреты в БД, Secure-кука, автозаполнение)** | ◐ в работе | Раздел ТЗ [90-external-services-security.md](sections/90-external-services-security.md). Сделано: защита форм от автозаполнения (90.3), Secure-кука по схеме запроса (90.2), `crypto.DecryptLenient`, расшифровка в bootstrap-overlay и hot-reload'ерах, шифрование секретов в репозитории, ротация ключа для `nodes.rmq_password` и `app_settings` (90.1/90.4). Неочевидности — §4.76–§4.78. Миграций нет |
| **§90.1: шифруются четыре секрета `app_settings`** | ✅ Phase 90.6 | [app_settings_repo.go](../internal/web/adapter/out/postgres/app_settings_repo.go): `sentry.dsn`, `clickhouse.password`, `mail.password`, `notifications.telegram.bot_token`. Схема БД не меняется (значения остаются строками в JSONB), миграции данных нет — чтение принимает и исторический plaintext. Закрывает дамп/бэкап/прямой доступ к PG; НЕ закрывает чтение `.env` (ключ там же) — граница названа в ТЗ явно |
| **§90.1: строгий Get против мягкого overlay** | ✅ Phase 90.5/90.6 | Разное поведение при `ErrDecryption` продиктовано read-modify-write в `usecase.Update` (мягкость → затирание секрета) и контрактом overlay'я как опционального слоя (строгость → простой трёх сервисов). Подробно — §4.76 |
| **§90.1: порядок внедрения (читатели раньше писателя)** | ✅ Phase 90.5 → 90.6 | В обратном порядке overlay залил бы `v1:...` в `cfg.ClickHouse.Password` молча. `readAppSettings` используется и всеми hot-reload'ерами ([reload.go](../internal/platform/bootstrap/reload.go)), поэтому одна правка в `decodeAppSettings` закрыла и горячую перезагрузку; `MustCipher` поднят выше `ApplyAppSettings` в трёх `main` |
| **§90.2: Secure вычисляется на запрос** | ✅ Phase 90.2 | [auth_handler.go](../internal/web/adapter/in/http/auth_handler.go) `requestIsHTTPS` (TLS или `X-Forwarded-Proto: https`) + общий `setSessionCookie` для Login/Logout (раньше Logout не выставлял `SameSite`). Чинит петлю «логин 200 → /me 401 → форма входа» при входе по IP. Подробно — §4.77 |
| **§90.3: автозаполнение чинится атрибутами** | ✅ Phase 90.1 | `autocomplete="off"`/`new-password` в ClickHouse, Mail, Notifications, Users, Sentry, RabbitMQSection; форма входа не тронута. Регрессионный тест проверяет атрибуты, а не поведение клика — заполняет браузер, а не приложение (§4.78) |
| **§90.6: разовая миграция открытых данных и обратный ход** | ✅ Phase 90.12 | `MODE=encrypt` шифрует то, что лежит в БД открыто (lazy-upgrade §90.1 сам по себе может не сработать годами — до первого сохранения настроек), `MODE=decrypt` раскрывает секреты `app_settings` обратно ПЕРЕД откатом кода на версию без §90.1. SQL-миграцией это невозможно принципиально: у миграции нет ни `ENCRYPTION_KEY`, ни AES-GCM. Повторный прогон безопасен — значение, читаемое текущим ключом, пропускается байт в байт, двойного шифрования не бывает |
| **§90.6: креды узлов раскрывать ЗАПРЕЩЕНО** | ✅ Phase 90.12 | `ErrDecryptNodesForbidden`: все три сервиса читают `nodes.*` строгим `Decrypt` ([node_repo.go](../internal/web/adapter/out/postgres/node_repo.go), [sender/nodepg](../internal/sender/adapter/out/nodepg/reader.go), [receiver/nodecache](../internal/receiver/adapter/out/nodecache/reader.go)), поэтому plaintext в этих колонках = неработающие узлы. Они шифруются с самого начала (§5.5), ни одна версия кода не ждёт их открытыми — откатывать нечего. Найдено integration-тестом: первая версия обратного хода расшифровывала и узлы, и тест сразу упал на `repo.Get` |
| **§90.4: ротация ключа полная и идемпотентная** | ✅ Phase 90.4/90.7 | [keyrotate](../internal/platform/keyrotate/keyrotate.go): три колонки `nodes` (добавлен забытый `rmq_password`) + четыре секрета `app_settings`; plaintext шифруется и считается `rows_upgraded_plaintext` (прогон с `OLD_KEY == NEW_KEY` = разовая миграция §90.1); документ разбирается в `map[string]any`, чтобы не потерять незнакомые поля. Логика вынесена из `package main` — иначе её не покрыть тестами. Процедура — DEPLOYMENT §12 |
| **§89: разные улучшения (шрифты, заголовок вкладки, ссылки на команду, внешний адрес)** | ◐ в работе | Раздел ТЗ [89-misc-improvements.md](sections/89-misc-improvements.md). Сделано: локальные шрифты и CSP (89.1), раздача /assets — MIME/кеш/404 (89.1), код ноды в заголовке вкладки (89.2), ссылки на команду в избранном и переключателе (89.3), внешняя ссылка команды — бэкенд и фронт (89.4), фикс адреса узла по чужому слагу (89.6) |
| **§89.1: шрифты живут в `src/assets`, а не в `public`** | ✅ Phase 89.1 | Выбор продиктован embed-директивой: Vite эмитит ассеты, на которые ссылается CSS, в `dist/assets` с хешем, а это ровно то, что уже покрыто `//go:embed index.html assets` ([static.go](../internal/web/static/static.go)). `public/fonts` дал бы `dist/fonts` — вне `assets`, то есть потребовал бы и правки директивы, и нового маршрута (иначе `/fonts/x.woff2` проваливается в SPA-fallback), и имена без хеша, несовместимые с `immutable`-кешем |
| **§89.1: Google отдаёт разный ответ по User-Agent** | ✅ Phase 89.1 | На дефолтном UA `css2` возвращает пять `@font-face` с `format('truetype')` — без woff2, без `unicode-range`, без деления на сабсеты, и ТОЖЕ с кодом 200. [update-google-fonts.sh](../scripts/fonts/update-google-fonts.sh) подставляет UA Chrome и проверяет результат по наличию `unicode-range`, а не по коду ответа |
| **§89.1: вариативные шрифты → один файл на сабсет** | ✅ Phase 89.1 | Inter и JetBrains Mono вариативные: на все запрошенные веса одного сабсета Google отдаёт ОДИН файл (33 блока `css2` → 13 URL), вес задаёт только объявление `font-weight`. Наивное имя `<slug>-<weight>-<subset>` кладёт 12 побайтово одинаковых копий — 626 КБ вместо 226 КБ, — и по выводу сборки это НЕ ВИДНО: Vite схлопывает дубликаты по хешу содержимого и печатает те же 8 файлов. Скрипт вернёт вес в имя, если у группы окажется больше одного URL |
| **§89.1: инлайн woff2 запрещён явно** | ✅ Phase 89.1 | Правило Vite — «размер < `assetsInlineLimit`» без исключений для `url()` в CSS, а `jetbrains-mono-cyrillic-ext.woff2` весит 1,6 КБ при пороге 4096: по умолчанию он уехал бы в base64 внутрь render-blocking CSS (+33 % байт, `unicode-range` перестаёт экономить) и после ужесточения CSP (`font-src` без `data:`) сломал бы рендер молча |
| **§89.1: MIME для `.woff2`/`.map` ставится явно** | ✅ Phase 89.1 | Их нет во встроенной таблице `mime/type.go`, а в alpine-образе Web нет `/etc/mime.types` (пакет `mailcap` не ставится) — тип определялся бы сниффером; у sourcemap сигнатуры нет вовсе, он уезжал как `text/plain`. Чистая функция `assetContentType` в middleware группы `/assets` ([spa.go](../internal/web/adapter/in/http/spa.go)), а не `mime.AddExtensionType` (глобальный реестр процесса, CLAUDE.md §4); `http.ServeContent` уважает выставленный `Content-Type` и тогда не сниффит |
| **§89.1: `Cache-Control` и 404 для `/assets/*`** | ✅ Phase 89.1 | Заголовков кеша не было вовсе, а `embed.FS` даёт нулевой `ModTime` → нет `Last-Modified`, `ETag` не ставит никто → без валидатора браузер качал весь бандл при каждом полном заходе. Имена хешированы → `public, max-age=31536000, immutable`; `index.html` (отдаётся не из `/assets/`) получает `no-cache`. Отсутствующий ассет раньше проваливался в `NoRoute` и получал index.html с кодом 200 — браузер отказывался исполнять из-за nosniff и писал невнятное «Refused to execute script»; теперь 404 + `no-store` |
| **§89.1: тест MIME обязан быть без сигнатуры шрифта** | ✅ Phase 89.1 | Сниффер Go знает `wOF2` и вернул бы `font/woff2` сам — на настоящих байтах тест проходил бы и без нашего заголовка, то есть проверял бы везение. В [spa_test.go](../internal/web/adapter/in/http/spa_test.go) нагрузка намеренно без сигнатуры; проверено, что с отключённым `assetContentType` кейсы woff2 и sourcemap краснеют |
| **§89.2: `useDocumentTitle` монтируется в `App`, а не в `AppShell`** | ✅ Phase 89.2 | `AppShell` живёт только под `Protected`, и вкладка ЛОГИНА осталась бы без имени — ровно там, где человек выбирает, куда вводить пароль; `/api/version` публичный. Данные берутся из существующего `queryKey ["version"]` (`useInstanceID`), новых запросов ноль — зафиксировано тестом `toHaveBeenCalledTimes(1)` |
| **§89.3: `@dnd-kit` кладёт в `attributes` `role="button"`** | ✅ Phase 89.3 | На `<a href>` ЯВНАЯ роль перебивает нативную: строка перестаёт быть ссылкой и для скринридера, и для `getByRole("link")` — то есть ровно та потеря семантики, ради устранения которой блок и делался. Снимается `useSortable({ attributes: { role: "link" } })`, а не выкусыванием из спреда. Проверено экспериментом: без override 5 из 6 тестов [SidebarFavorites.test.tsx](../web-ui/src/components/SidebarFavorites.test.tsx) краснеют |
| **§89.3: почему левый клик не отдан роутеру** | ✅ Phase 89.3 | Появившийся в адресе `?team=` зеркало §76 применило бы как ВХОДЯЩУЮ ссылку: второй `POST /api/me/switch-team` и вторая инвалидация team-scoped кеша. Плюс два дефекта зеркала — ref одноразовости `handled` переживает `/nodes/:id`, где §58 меняет команду мимо адресной строки (клик по прежней команде вернул бы адрес назад вместо переключения), и в сквозном режиме ветка `allTeams` стоит выше резолва slug'а (§86.7.1) и съела бы выбор. Поэтому href — ради браузера, а обычный левый клик гасится `preventDefault` |
| **§89.3: `draggable={false}` и `preventDefault` в drag-guard** | ✅ Phase 89.3 | У `<a href>` нативный HTML5-драг включён по умолчанию: браузер перехватывает `pointermove`, показывает «призрак» адреса, и сортировка @dnd-kit не стартует. Отдельно: click, который браузер шлёт после drop, раньше достаточно было проигнорировать — со ссылкой без `preventDefault` перетаскивание избранного заканчивалось бы переходом по href |
| **§89.3: заглушка `ResizeObserver` в общем setup тестов** | ✅ Phase 89.3 | Radix позиционирует поповеры через floating-ui, который требует `ResizeObserver`; в jsdom его нет, и любой тест с открытым Popover падал «ReferenceError» ещё до первого assert'а. Заглушка, а не полифилл: измерений в jsdom всё равно нет |
| **§89.4: поле у КОМАНДЫ, а не у узла** | ✅ Phase 89.4 | Шлюз публикует команду целиком; дублировать один базовый адрес в каждом узле — лишняя работа и источник расхождений. Миграция [0037](../migrations/0037_team_external_url.up.sql), колонка справочная (Receiver её не читает, маршрутизации не касается). `CHECK` на формат в SQL намеренно нет: правило живёт в `domain.ValidateTeamExternalURL`, регэксп в миграции стал бы вторым источником истины и разъехался бы при первой правке |
| **§89.4: `ValidatePublicBaseURL` переиспользовать нельзя** | ✅ Phase 89.4 | Она (§28) требует чистый origin и запрещает путь, а внешний шлюз обычно публикует шину под своим префиксом (`https://gw.example.com/nexus`) — запрет пути обессмыслил бы поле в основном сценарии. Своя проверка разрешает путь, запрещает query/fragment и срезает хвостовые слеши (иначе `//` посередине адреса, чего глазами не видно, а шлюз такой путь не узнаёт). Нормализация внутри `Team.Validate` — у `Team` нет `SetDefaults`, приём как у `PeerInstance.Validate` |
| **§89.4: у команды ТРИ независимых места сканирования** | ✅ Phase 89.4 | В отличие от узла (один `scan`): общий `teamCols`, свой `SELECT` в `ListUserTeams` и ещё один в `ListTeamsByUsers` (§44.G). Пропуск колонки в двух последних ничего не роняет — отдаёт пустую строку именно в `/api/me/teams`, то есть ровно там, где значение ждёт интерфейс. Закрыто [team_external_url_test.go](../tests/integration/team_external_url_test.go), проверяющим все три пути чтения плюс стирание ссылки |
| **§89.4: три ответа API отдают поле, и у каждого своя причина** | ✅ Phase 89.4 | `/api/teams*` — форма команды; `/api/me/teams` — форма СОЗДАНИЯ узла, где команда выбирается явно (§86.6) и не резолвится ниоткуда; `/api/nodes/:id/team` — вкладка «Конфиг», которой нужна ссылка команды-ВЛАДЕЛЬЦА, а её может не быть в членствах смотрящего (тот же случай, ради которого §58 завёл резолвер). Лишнего запроса не появляется: ключ уже прогрет `useEnsureNodeTeam` |
| **§89.6: адрес узла собирался по слагу чужой команды** | ✅ Phase 89.4 | `useNodeUrlBuilder` брал slug ТЕКУЩЕЙ команды сессии. На странице узла это маскировал гейт `useEnsureNodeTeam`, но на форме СОЗДАНИЯ с явным выбором команды (§86.6) превью показывало `…/api/v1/alpha/<path>`, тогда как узел создавался доступным по `…/api/v1/webhook/<path>`; рядом `chosenTeam`/`chosenDatabase` уже были сделаны «по выбору» — адрес просто отстал. Хук получил необязательный источник команды; чистые `buildNodeUrl`/`buildLegacyNodeUrl` не менялись. Регресс закрыт [ConfigTab.test.tsx](../web-ui/src/components/node/ConfigTab.test.tsx) — проверено, что все три кейса краснеют на старом коде |
| **§88: почта (SMTP) и восстановление пароля** | ◐ в работе | Раздел ТЗ [88-mail-password-reset.md](sections/88-mail-password-reset.md). Исходящая почта + самостоятельное восстановление пароля по ссылке из письма. Сделано: домен и хранение настроек (88.1), транспорт и тестовая отправка (88.2), панель «Настройки → Почта» (88.3), хранилище токенов и миграция 0036 (88.4), usecase восстановления и шаблоны писем (88.5), HTTP-слой (88.6), интерфейс восстановления (88.7), смежные экраны (88.8), ревизия перед сдачей (88.9) — РЕАЛИЗАЦИЯ ЗАВЕРШЕНА. Гейты сдачи пройдены: полные тесты, контейнерный -race, integration, ревизия diff'а и живой прогон на стенде |
| **§88.8.4: панель «Настройки → Почта»** | ✅ Phase 88.3 | [settings/Mail.tsx](../web-ui/src/pages/settings/Mail.tsx) + вкладка в [Settings.tsx](../web-ui/src/pages/Settings.tsx) (массив `tabs` И `<Routes>` — оба места), admin-only, между `clickhouse` и `notifications`. Стиль как у соседей по группе app_settings (голые `<input className={inp}>`, нативные чекбоксы), исключение — пароль: `SecretInput` с «глазиком», потому что длинный SMTP-пароль вслепую вводят с опечаткой. Числа держатся строками: пустое поле обязано остаться пустым, иначе очищенный таймаут уехал бы нулём и не прошёл валидацию. `section()` собирает payload в одной точке — «Сохранить» и «Проверить» иначе отправляли бы разное, и тест проверял бы не то, что сохранится. Сохранение инвалидирует и `["version"]`: признак доступности восстановления §88.4.5 меняется этим же действием, форма входа должна о нём узнать |
| **§88.3: пересборка встроенного бандла** | ✅ Phase 88.3 | `make build-ui` кладёт `web-ui/dist/*` в `internal/web/static/`, но `cp` НЕ убирает прежние хешированные ассеты — старый `index-<hash>.js`/`.map` снимаются с учёта точечным `git rm`, иначе в `embed.FS` копятся мёртвые мегабайты. CSS-хеш при этой правке не изменился (содержимое обновилось на месте) — удалять надо ровно JS-пару. Проверка после сборки: `index.html` обязан ссылаться на НОВЫЙ хеш |
| **§88.3: пакет `platform/mail` на go-mail** | ✅ Phase 88.2 | [mail.go](../internal/platform/mail/mail.go): `Config`/`Message`/`Sender.Send` + чистый `planFor` (маппинг настроек в план транспорта) и `optionsFor`. Consumer-интерфейс `MailSender` — [usecase/mail.go](../internal/web/usecase/mail.go), туда же перевод `domain.ResolvedMail` → `mail.Config` (platform не зависит от домена). Соединение живёт ОДНУ отправку: пул для одного письма на действие пользователя не нужен, а долгоживущее пришлось бы пересоздавать при каждой смене настроек. Пароль намеренно не хранится в `transportPlan` — структуру можно печатать в отладке целиком; при `auth=none` в соединение не уходят ни имя, ни пароль |
| **§88.3: почему не stdlib `net/smtp`** | ✅ Phase 88.2 | Сравнение двух НАПИСАННЫХ реализаций: ~250 строк против ~90, из них ~145 — протокол и кодировки. Implicit TLS (465) `smtp.SendMail` не умеет; PLAIN поверх нешифрованного канала stdlib запрещает намеренно (а внутренние релеи на :25 настроены именно так); `AUTH LOGIN` в stdlib отсутствует вовсе, и строки-вызовы сервера не стандартизированы; MIME (RFC 2047, multipart, quoted-printable) собирается руками; контекст и таймаут навешиваются дедлайном на сокет и сторожевой горутиной. Все эти места ломаются МОЛЧА — симптом у получателя, а не в CI. Одна новая прямая зависимость, транзитивных ноль |
| **§88.3: `starttls` = TLSMandatory, не Opportunistic** | ✅ Phase 88.2 | Молчаливый откат в открытый канал недопустим: по нему едет ссылка на смену пароля. Зафиксировано тестом `TestPlanFor_StartTLSIsMandatoryNotOpportunistic` |
| **§88.3: фейковый SMTP вместо сети** | ✅ Phase 88.2 | [fakesmtp_test.go](../internal/platform/mail/fakesmtp_test.go) — сервер на `127.0.0.1:0` (`httptest` неприменим, это не HTTP). Тела разбираются НАСТОЯЩИМИ парсерами (`net/mail`, `mime/multipart`, `quotedprintable`): проверка подстрокой давала ложные падения — заголовки сворачиваются (`Subject:\n =?UTF-8?…`), а тело едет в quoted-printable (`token=3Dabc` вместо `token=abc`). Тема с кириллицей проверяется на обратимость: закодирована в encoded-word И раскодируется в исходную строку. `readDotBody` сохраняет CRLF — иначе stdlib-парсеры не берут письмо. goleak в `TestMain` стережёт висящее соединение после отказа и отмены |
| **§88.4.1: миграция 0036 и репозиторий одноразовых ссылок** | ✅ Phase 88.4 | [0036_one_time_tokens](../migrations/0036_one_time_tokens.up.sql) + [OneTimeTokenRepoPg](../internal/web/adapter/out/postgres/one_time_token_repo.go) + порт [one_time_token_repo.go](../internal/web/usecase/port/one_time_token_repo.go) + домен [one_time_token.go](../internal/domain/one_time_token.go). `Consume` — атомарный CAS одним `UPDATE … RETURNING`: гонка восьми одновременных подтверждений даёт ровно одного победителя (integration-тест), а `SELECT`+`UPDATE` сменил бы пароль дважды. `Peek` не расходует ссылку (§88.4.6). Изоляция назначений закреплена тестом: токен чужого `purpose` не гасится, не находится `Peek` и не попадает в `CountActive`. `CHECK` отвергает опечатку в назначении. Откат проверен живьём — `Down(1)` убирает таблицу и индекс, повторный `Up` восстанавливает (§74) |
| **§88.9: ревизия перед сдачей — три находки** | ✅ Phase 88.9 | (1) `resolveUser` возвращал код исхода на ЛЮБУЮ ошибку поиска — недоступная БД маскировалась под «пользователь не найден», и во время аварии форма отвечала бы «письмо отправлено», а аудит писал бы неправду; сигнатура разведена на три исхода + предикат `isUserMissing`. (2) `default`-ветка `Confirm` отдавала `err.Error()` НЕаутентифицированному клиенту — а туда попадают ошибки хранилища с именем таблицы и куском запроса; наружу оставлен только отказ политики пароля (признак `usecase.ErrPasswordPolicy`), остальное → 500 + лог. (3) §88.9 обещало метрики отправки письма, которых не было: добавлены `nexus_mail_send_total{purpose,result}` и `nexus_mail_send_duration_seconds`, подключены к ОБЕИМ точкам отправки общим `observeMailSend`. Проверено без правок: маскирование Sentry уже покрывает `new_password`/`smtp_password`/`reset_token` подстрокой |
| **§88.9: гонка в фейковом SMTP, найденная только контейнерным -race** | ✅ Phase 88.9 | `fakeSMTP.silentGreeting`/`failAt` тесты присваивали полями УЖЕ работающему серверу, а его горутина читала их без блокировки. Нативный `make test -race -short` на Windows гонку НЕ увидел — увидел контейнерный прогон (ровно то, ради чего он записан в правилах). Лечение — не мьютекс, а устранение общего изменяемого состояния: параметры задаются опциями конструктора `newFakeSMTP(t, withFailAt(...), withSilentGreeting())` и после старта не меняются |
| **§88.9: живой прогон на стенде** | ✅ Phase 88.9 | Пройден браузером на настроенном релее. Подтверждено: ссылка «Забыли пароль?» видна при `password_reset_ready:true`; диалог переносит набранный логин; ответ на `request` НЕРАЗЛИЧИМ для существующего и несуществующего пользователя (202, одно тело, 33 мс против 29 мс — фоновая отправка убирает и тайминговую разницу), различие только в аудите (`sent` против `user_not_found`); письмо реально ушло; страница сброса открывается по прямой ссылке, меняет пароль; повторный переход по той же ссылке → «недействительна»; ВСЕ выданные ссылки пользователя погашены сменой пароля (три из трёх, §88.7); вход старым паролем → 401, новым → 200; аудит содержит и `password_reset.confirm`, и `password.change` |
| **§88.9: ложно-зелёные гейты на Windows** | ✅ Phase 88.9 | Контейнерный `-race` дважды отрапортовал успех, ничего не выполнив: Git Bash сконвертировал `-w /src` в `C:/Program Files/Git/src` (лечится `MSYS_NO_PATHCONV=1`), а образ `golang:1.26` содержит Go 1.26.4 при `go >= 1.26.5` в `go.mod` и `GOTOOLCHAIN=local` (лечится `-e GOTOOLCHAIN=auto`). В обоих случаях `\| head` съедал код возврата, и гейт выглядел пройденным. Вывод обязан идти в файл, а `$?` — проверяться отдельно |
| **§88.8.5–88.8.7: смежные экраны** | ✅ Phase 88.8 | [Users.tsx](../web-ui/src/pages/settings/Users.tsx): чип «без email» в списке и предупреждение под пустым полем в диалоге; email обязательным НЕ становится (колонка nullable с первых миграций, `binding` уже `omitempty` — обязательность сломала бы редактирование существующих учёток и создание служебных). Обе подсказки показываются ТОЛЬКО при включённом восстановлении (общий хук `usePasswordResetReady` на том же `queryKey ["version"]`, лишнего запроса нет) — иначе это фоновый шум, который перестают замечать. [Password.tsx](../web-ui/src/pages/settings/Password.tsx): адрес, куда придёт ссылка, и предупреждение при пустом email; текст `note` дополнен — смена пароля аннулирует выданные ссылки (§88.7), иначе поведение окажется сюрпризом. [AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx): два action в фильтре; `confirm` красится «успехом», а `request` остаётся нейтральным — он пишется и для безрезультатных попыток, исход лежит в `details.result` |
| **§88.8.1–88.8.3: интерфейс восстановления** | ✅ Phase 88.7 | [Login.tsx](../web-ui/src/pages/Login.tsx) (ссылка), [ForgotPasswordDialog.tsx](../web-ui/src/pages/ForgotPasswordDialog.tsx), [ResetPassword.tsx](../web-ui/src/pages/ResetPassword.tsx) + маршрут в [App.tsx](../web-ui/src/App.tsx). «Забыли пароль?» — `<button type="button">`, а не ссылка: элемент открывает диалог, а не ведёт по адресу (§79 наоборот); закреплено тестом на `tagName`/`type`. Виден только при `password_reset_ready` из `/api/version`, fail-closed при недоступности эндпоинта. Атом `Field` расширен пропом `labelRight` — вкладывать `<button>` внутрь `<label>` нельзя (невалидная разметка, `jsx-a11y` ловит). Маршрут `/reset-password` объявлен ЯВНО рядом с `/login`: catch-all иначе увёл бы прямой переход из письма на «/». `passwordStrength`/`generatePassword` вынесены из `settings/Users.tsx` в [lib/password.ts](../web-ui/src/lib/password.ts) — третья копия появилась бы на следующем экране |
| **§88.7: локали правятся текстово, а не JSON-раундтрипом** | ✅ Phase 88.7 | Грабля этого блока: `json.load`+`json.dumps` над `locales/*.json` развернул компактные однострочные объекты (`"app": { "title": "Nexus" }` → три строки) и дал 118 изменённых строк вместо 20. Ревизия diff'а такое пропустить не должна — ключи вставляются текстовой заменой по якорю с последующей проверкой `json.loads` на валидность |
| **§88.4.2: три публичных маршрута** | ✅ Phase 88.6 | [password_reset_handler.go](../internal/web/adapter/in/http/password_reset_handler.go) + [routes.go](../internal/web/adapter/in/http/routes.go): `request`/`validate`/`confirm` объявлены РЯДОМ с `api.POST("/auth/login")` — второй и последний блок публичного внутри `/api`, чтобы «что тут публично» читалось в одном месте. CSRF группы их не обходит и обходить не должна: GET проходит всегда, POST с пустым `Origin` — тоже (curl/тесты), а сторонний сайт получает отказ до обработчика. `request` отдаёт **202** (письмо уходит фоном). Ключевой тест `TestPasswordResetHandler_Request_IdenticalResponse` сравнивает ТЕЛА четырёх исходов между собой — утечка «такой пользователь есть» заметна именно на этом уровне |
| **§88.4.4: rate-limit восстановления** | ✅ Phase 88.6 | Три ключа с одним значением `web.password_reset_rate_limit_per_min` (дефолт 5, во ВСЕХ трёх `config/*.yml`): `pwreset:ip:<ip>`, `pwreset:user:<введённая строка>`, `pwreset:confirm:<ip>`. Второй считается **независимо от существования пользователя** — иначе сам лимит стал бы способом узнать ответ. `keyScope` = `pwreset`, поэтому метрика `nexus_ratelimit_check_errors_total{scope="pwreset"}` появляется сама. Fail-open при недоступном Redis закреплён тестом: сбой кеша не должен обрушать восстановление целиком |
| **§88.4.5: признак доступности в `/api/version`** | ✅ Phase 88.6 | Поле `password_reset_ready` в [dto_common.go](../internal/web/adapter/in/http/dto_common.go), провайдер подключается билдером `WithPasswordResetReady` (у конструктора и так семь позиционных аргументов). Значение берётся из `MailAvailabilityProvider` — памяти процесса, обновляемой подписчиком `reloader.SectionMail` ([app.go](../internal/web/app.go)); БД публичный эндпоинт не трогает. Поле аддитивное: реестр инстансов §73 разбирает тот же ответ и не ломается |
| **§88.4/88.7: `PasswordResetUsecase`** | ✅ Phase 88.5 | [password_reset.go](../internal/web/usecase/password_reset.go): `Request` / `Validate` / `Confirm`. Все семь исходов запроса дают наружу ОДИН ответ (`ttlMinutes` одинаков), различие — только в аудите (`details.result`), метрике и служебном логе; таблично закреплено тестом `TestPasswordReset_Request_AllOutcomesLookIdentical`. Инфраструктурный сбой хранилища, наоборот, возвращается ошибкой: молча «принять» запрос, не создав токен, значит обещать письмо, которого не будет. В `Confirm` порядок значим — `validatePassword` ДО `Consume`, иначе опечатка в длине пароля сжигала бы ссылку безвозвратно (тест ловит это подставным `consErr`). Активность пользователя проверяется И при запросе, И при подтверждении: между ними до суток |
| **§88.5: отправка письма фоном** | ✅ Phase 88.5 | Синхронно — резолв, проверки, создание токена, аудит; письмо уходит в `runner` (опция `WithPasswordResetRunner`, дефолт `go fn()`, в тестах синхронная — иначе флейк и спор с goleak). `context.WithoutCancel` + таймаут `cfg.Timeout+5s` + `safego.Recover`. Токен пишется ДО запуска отправки: письмо, обогнавшее запись, дало бы мёртвую ссылку. Цена признана явно — сбой SMTP пользователю невидим, поэтому он логируется уровнем `error` (порог Sentry), пишется в аудит `result: send_failed` и считается метрикой `nexus_password_reset_requests_total{result}` |
| **§88.6: шаблоны писем** | ✅ Phase 88.5 | [render.go](../internal/platform/mail/render.go) + [templates/](../internal/platform/mail/templates/) через `//go:embed`. `NewTemplates() (*Templates, error)`, а не `template.Must` в package var: глобальное изменяемое состояние запрещено (CLAUDE.md §4), `panic` вне `main` — §5. Язык — ПОЛУЧАТЕЛЯ (`users.lang`), не запрашивающего: письмо читает владелец ящика, а запрос мог прийти от постороннего; неизвестный язык → английский. Тема НЕ содержит пользовательского ввода (`\r\n` в имени дал бы инъекцию MIME-заголовка) — закреплено тестом. `html/template` экранирует имя (`<script>` → `&lt;script&gt;`) и фильтрует `javascript:` в `href` (`ZgotmplZ`), `text/template` в plain-части оставляет текст как есть |
| **§88.7: хук гашения ссылок в `ChangePassword`** | ✅ Phase 88.5 | [auth.go](../internal/web/usecase/auth.go) `WithPasswordResetInvalidator` + `invalidateResetLinks`. Хук стоит в `ChangePassword`, потому что это ЕДИНСТВЕННАЯ точка смены пароля (`ChangeOwnPassword` делегирует, админский handler зовёт её же, подтверждение восстановления тоже) — покрываются все каналы разом. Best-effort рядом с `DeleteByUser`: пароль уже сменён, откатывать операцию из-за недоступного хранилища нельзя. Закрываемая дыра: без него старая ссылка из почты позволяла бы перебить только что установленный пароль |
| **§88.4.2: `UserRepo.GetByEmail`** | ✅ Phase 88.4 | [user_repo.go](../internal/web/adapter/out/postgres/user_repo.go): `lower(email) = lower($1) LIMIT 2` под индексом `users_email_lower_idx`. Берутся ДВЕ строки намеренно: колонка не уникальна, и несколько совпадений → `ErrUserEmailAmbiguous`, который вызывающий трактует как «не найден». Иначе владелец общего адреса получил бы ссылку на чужую учётную запись |
| **§88.4.1: таблица `one_time_tokens` — общая, а не под один сценарий** | ✅ Phase 88.4 | Имя и состав зафиксированы в ТЗ до реализации. Восстановление пароля — первый потребитель; следующие названы в §88.12 (подтверждение смены email, приглашение пользователя), инварианты у всех одни. Разводит колонка `purpose` под `CHECK` (образец `users.role`: опечатка иначе даёт токены, которые молча никогда не найдутся) + `payload JSONB` под данные назначения. **Погашение обязано фильтровать по `purpose` — граница безопасности:** без фильтра ссылка на слабое действие предъявляется на сильном эндпоинте; закрепить тестом. `purpose` входит в сигнатуру КАЖДОГО метода порта, кроме чистки — забыть его тогда невозможно. `Consume` возвращает запись целиком, а не `userID`: следующему потребителю нужен `payload` |
| **§88.4.1: `uuid_generate_v4()`, а не `gen_random_uuid()`** | ⛔ Phase 88.4 | Грабля PG 12 (на бою именно она): `gen_random_uuid()` встроена только с PG 13, до этого требует `pgcrypto`, а в схеме включён лишь `uuid-ossp` (миграция 0001). Все существующие миграции используют `uuid_generate_v4()` — новая не должна выбиваться. Дефект поймался бы и в integration (testcontainers поднимает `postgres:12-alpine`), но дешевле не вносить |
| **§88.8.4: `POST /api/settings/mail/test`** | ✅ Phase 88.2 | [app_settings_handler.go](../internal/web/adapter/in/http/app_settings_handler.go) `TestMail` + [routes.go](../internal/web/adapter/in/http/routes.go) в `authedAdmin`. Тело — секция целиком плюс `to`: у почты, в отличие от Telegram, адрес назначения не является частью настроек. `SettingsTester.TestMail` берёт таймаут ИЗ САМИХ НАСТРОЕК, а не из общего `pingTimeout` = 5 с: для ping'а ClickHouse пяти секунд хватает, а полный SMTP-диалог через корпоративный релей в них не укладывается — рабочая конфигурация показывала бы «context deadline exceeded». Негодные значения секции отсекаются до похода в сеть (иначе оператор видит ошибку резолвера вместо «порт вне диапазона»); «не смогли отправить» — это `TestResult{OK:false}`, а не 500 (общий контракт с CH/Sentry/Telegram), тогда как `mail sender == nil` — дефект wiring'а и потому Go-ошибка |
| **§87.5: сетка карточек роли 2×2, чип `warn`** | ✅ Phase 87.3 |
| **§88.2: секция `mail` в `app_settings` — шесть точек интеграции** | ✅ Phase 88.1 | [domain/mail.go](../internal/domain/mail.go) (`MailSettings`, `Resolve()`, `ValidateMailSettings` по полям, `ValidateMailConsistency` по смерженному, `MailPasswordResetReady`). Секция обязана попасть в: struct [app_settings.go](../internal/domain/app_settings.go), анонимную структуру [app_settings_repo.go](../internal/web/adapter/out/postgres/app_settings_repo.go) `Update`, `mergeAppSettings`, `changedSections`, маскирование в `Get` ([app_settings.go](../internal/web/usecase/app_settings.go)) и `sectionsFor` [reloader.go](../internal/platform/reloader/reloader.go). Регресс на потерю секции — `TestAppSettingsRepo_MailRoundTrip_E2E` ([app_settings_repo_test.go](../tests/integration/app_settings_repo_test.go)), проверен красным на коде без `Mail` в `Update` |
| **§88.2: имя секции `mail`, а не `smtp`** | ✅ Phase 88.1 | SMTP — деталь транспорта; появится отправка через HTTP API провайдера, и переименовать секцию будет нельзя (имя уже в сохранённом JSONB и в `reloader.Section`, куда `changedSections` кастует строку БЕЗ маппинга). Внутрь секции входят и параметры восстановления, поэтому `smtp` было бы у́же смысла |
| **§88.2: `MailAvailabilityProvider` — почему не чтение из БД** | ✅ Phase 88.1 | [mail_availability.go](../internal/web/usecase/mail_availability.go), `atomic.Bool` + `Refresh`, образец — [session_ttl.go](../internal/web/usecase/session_ttl.go). Признак «восстановление доступно» отдаётся публичным `GET /api/version`, который на боевой установке НЕ делает ни одного запроса в БД (провайдер версии дёргается только при `web.allow_version_override`, т.е. в dev) и опрашивается соседними инстансами §73 — вешать на него `SELECT` нельзя. Единственный подписчик `reloader.SectionMail`; сам SMTP-транспорт перезагружать нечего (соединение живёт одну отправку). Ошибка чтения НЕ сбрасывает признак в false: провал одного запроса — не повод убирать со страницы входа работающую функцию |
| **§88.2: `from_name` без `\r\n`** | ✅ Phase 88.1 | Перевод строки в имени отправителя позволил бы дописать произвольные MIME-заголовки (`Bcc:` и пр.). По этой же причине имя пользователя не подставляется в тему письма §88.6. Валидация хоста заодно отвергает схему и порт (`smtp://host`, `host:587`) — оба варианта дают нерабочее соединение с невнятной ошибкой резолвера |
| **§87.5: сетка карточек роли 2×2, чип `warn`** | ✅ Phase 87.3 | [Users.tsx](../web-ui/src/pages/settings/Users.tsx): `sm:grid-cols-3` → `sm:grid-cols-2` + `max-h-[80vh] overflow-y-auto` у диалога. Четыре карточки в ряд не помещаются (окно 460px → ~97px на карточку). Тесты — [Users.role.test.tsx](../web-ui/src/pages/settings/Users.role.test.tsx), [roles.test.ts](../web-ui/src/lib/roles.test.ts) |
| **§86.4.1: замер на бою, отвергнувший батч по таблице** | ✅ Phase 86.1 | Снято с боя v1.26.0 (11.08.2026) по 7 командам из 12: **34 узла, 33 РАЗЛИЧНЫЕ таблицы**, 2 узла с `external_table`, суммарно 24 ч ≈ 1.2 с / 30 д ≈ 3.6 с. **Первый вывод — батч `GROUP BY node_id` вместо запроса на узел отвергнут ДО написания кода:** таблиц столько же, сколько узлов, общая ровно одна (`nexus_vika.dadata` на `dadata` + `dadata_suggestions`), то есть приём сэкономил бы ОДИН запрос из тридцати четырёх, а стоил бы двух новых методов порта, отдельной ветки для `external_table` (§64 разрешает им произвольную схему, а §61 оставил послабление `OR node_id = ''` — строки без `node_id` схлопнулись бы в чужую корзину при группировке) и тестов на эквивалентность агрегаций. Записано в ТЗ, чтобы приём не изобретали заново. **Второй вывод — стоимость определяется ОБЪЁМОМ ДАННЫХ, а не числом узлов**, и это опровергает исходную посылку проекта: `gate` — 2 узла и 2.23 с за 30 дней (10.7 млн запросов), `vika` — 12 узлов и 0.64 с (2.2 млн). Поэтому рычаги выбраны те, что бьют по объёму (приблизительный подсчёт §44.L, кеш агрегата), а порционная загрузка отвечает за скорость появления строк, а не за экономию прохода для KPI-шапки. **Ориентир §44.L «171 узел / 30 д ≈ 7 с» снят на синтетическом стенде** и боевую картину не описывает — на бою всё хозяйство считается быстрее одной стендовой команды |
| **§86.3: `GET /api/nodes?scope=all` — сквозной список узлов** | ✅ Phase 86.2 | Общий разбор скоупа вынесен в [scope.go](../internal/web/adapter/in/http/scope.go) (`wantsAllTeams`, `resolveAllTeamsUser`) — его переиспользуют аудит и метрики, а не копируют. Usecase: `NodeUsecase.ListAcrossTeams` + приватный `membershipScope`, ПОВЕРХ которого переписан существующий `SearchAcrossTeams` §62 — обход членств был там уже написан, и вторая копия разъехалась бы при первой правке. Репозиторий не тронут вовсе: `ListNodesFilter.TeamIDs` → `WHERE team_id = ANY($1)` появился ещё в §62, миграций и SQL нет. **`f.TeamID` вызывающего затирается явно** (`f.TeamID = ""`): в репозитории `TeamIDs` имеет приоритет, но оставить оба скоупа в одном фильтре — заготовка для будущей ошибки, поэтому сквозной режим их разводит, и это закреплено ассертом. **Пустые членства НЕ доходят до репозитория** — и это не оптимизация, а защита: пустой `TeamIDs` в адаптере означает «фильтра нет», то есть выдачу узлов ВСЕХ команд инстанса; отдельный тест проверяет `listCalls == 0`. API-токен получает **403, а не тихий откат** в свою команду (`resolveAllTeamsUser` до usecase): молчаливое сужение выглядело бы для клиента как «вижу всё», хотя он видит одну команду — тест проверяет и код, и что запрос в репозиторий не ушёл. Неизвестное значение `scope` (пустое, `team`, `ALLX`) — обычный режим, а не 400: неизвестный параметр не должен ронять список. Имена команд сервер НЕ обогащает (в отличие от §62, где поиск живёт в шапке без гарантии членств на руках) — `NodeResponse.TeamID` уже существовал, фронт резолвит имя по карте из `/api/me/teams`. 7 тестов (usecase + handler), swagger перегенерирован |
| **§86.6: команда узла выбирается явно — `team_id` в POST /api/nodes** | ✅ Phase 86.3 | `CreateNodeRequest.TeamID` (`omitempty,uuid`), handler берёт его вместо `currentTeamID(c)`, пустое — прежний контракт (старые клиенты и API-токены не ломаются). Авторизация — `NodeUsecase.ensureCreateTeam` по образцу `ensureMembership` API-токенов (§18.3): чужая команда → `ErrPermissionDenied` → 403. **Проверка идёт ТОЛЬКО когда команда отличается от команды актора** — совпадение с командой сессии это прежний путь §18.3 со своим якорем доверия (логин и switch-team членство уже проверили), и лишний `SELECT` в PG на каждое создание узла там не нужен; тест сторожит `listCalls == 0` на общем пути. **Гейт стоит ДО `prepareNewNode`, и это не вкусовщина:** иначе `normalizeCHTable` успел бы сходить в PG за `ch_database` ЧУЖОЙ команды, а потолок числа узлов посчитался бы по ней же — тест проверяет это по нетронутому имени таблицы после отказа. `team_id` добавлен в детали аудита `node.create`: команда больше не подразумевается сессией, и без неё по журналу нельзя восстановить, куда узел создан (отдельный тест). Префикс БД ClickHouse следует за выбранной командой автоматически — `normalizeCHTable` и так работает от `n.TeamID`; тест `nexus_beta.parcel` при сессии в alpha закрывает главную тихую ошибку раздела (узел одной команды писал бы логи в БД другой, потому что явно указанный чужой префикс `normalizeCHTable` сохраняет). 4 usecase-теста + 3 handler-теста, swagger перегенерирован |
| **§86.7: `GET /api/audit?scope=all` — журнал по всем командам пользователя** | ✅ Phase 86.4 | `AuditFilter.TeamIDs` + `team_id = ANY($n::uuid[])` в [audit_repo.go](../internal/web/adapter/out/postgres/audit_repo.go), `AuditUsecase.ListAcrossTeams`, разбор скоупа переиспользован из [scope.go](../internal/web/adapter/in/http/scope.go) (Phase 86.2), а не скопирован. **`scope=all` — НЕ синоним существующего `?team_id=*`:** тот снимает фильтр вовсе и показывает журнал ВСЕГО инстанса (admin-only), а этот ограничен членствами и доступен всем, кто вправе читать журнал (manager+, §26.3). Две разные вещи под похожими именами — расхождение зафиксировано в godoc и в тесте. `TeamIDs` имеет приоритет над `TeamID` и затирает его: два взаимоисключающих условия в одном `WHERE` дали бы вечно пустую выдачу. Пустые членства не доходят до репозитория (пустой `TeamIDs` = «фильтра нет» = тот самый глобальный журнал). **Членства подключены builder'ом `WithTeams`, а не параметром конструктора:** `NewAuditUsecase` зовётся в полутора десятках мест (каждый usecase пишет свой аудит), и расширение сигнатуры ради одного читателя заставило бы править их все. **Список и CSV-выгрузка ходят через общий `listEntries`** — два разбора одного скоупа разъехались бы, и выгрузка отдавала бы не то, что на экране. Записи с `team_id IS NULL` (глобальные действия admin'а, §18.1) в сквозную выдачу не попадают — как и при фильтре по одной команде. **Integration-тест на реальном PG 12** (`TestAuditScope_TeamIDs_E2E`): форма `ANY($n::uuid[])` в этом репозитории новая (остальные UUID-условия одиночные, `$n::uuid`), и её совместимость с кодированием `[]string` в pgx проверяется только настоящим PostgreSQL — unit-тесты усечения фильтра этого не видят. 4 unit-теста + 1 integration |
| **§86.4: порционные метрики + агрегат шапки отдельным маршрутом** | ✅ Phase 86.5 | `NodesScope{TeamID|UserID, NodeIDs}` + `NodesOverviewScoped` ([metrics.go](../internal/web/usecase/metrics.go)), агрегат — `OverviewTotalsScoped` в отдельном [metrics_totals.go](../internal/web/usecase/metrics_totals.go) с TTL-кешем и `singleflight`. `ListNodesFilter.IDs` → `id = ANY($n::uuid[])` **поверх** team-условия (AND, а не вместо): иначе набор id стал бы способом прочитать чужую команду. Резолв членств у списка узлов, журнала и метрик сведён в общий `teamScope` ([team_scope.go](../internal/web/usecase/team_scope.go)) — три копии правила «скоуп = членства» разъехались бы, а расхождение здесь означает утечку. **Маршрут `/api/metrics/totals`, а НЕ `/metrics/nodes/totals`:** сегментом ниже уже стоит wildcard `/metrics/nodes/:id`, и статический сосед рядом с ним роняет gin-роутер (та же грабля, что увела глобальный поиск в `/api/search/*`, §62). **Ключ кеша квантует границы окна по TTL** — у скользящего окна («последние 24 часа») правая граница это «сейчас», и без квантования ключ уникален на каждый запрос, то есть кеш не срабатывает никогда; набор команд в ключе сортируется (порядок членств репозиторием не гарантирован). **Принудительный approx отменён замером:** планировалось включать HLL в сквозном режиме, но §86.4.1 показал, что всё хозяйство считается около секунды, а побочный эффект неприятен — число узла на рабочем столе расходилось бы с точным числом на его собственной странице. **`NodeThroughputRow.NodeID` (и `node_id` в DTO)** — сшивать строки с метриками по пути стало нельзя (`UNIQUE(team_id, path)`, §86.7). **Попутно закрыта утечка в Prometheus-ветке:** fallback без ClickHouse строил строки из ответа Prometheus, а у метки команды нет (`node = <path>`) — рабочий стол без CH показывал пути узлов ВСЕХ команд инстанса; теперь строки строятся по узлам скоупа. Два следствия, оба закреплены тестами: молчащий узел получает строку с нулями (как в CH-ветке), «сирота» (трафик без узла в конфигурации) с рабочего стола уходит и остаётся предметом диагностики §44.E. Три существующих теста пришлось привести к новому контракту — они и поймали изменение |
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
- **`size_bytes` топика — из Prometheus, а не из Kafka (§75).** Высокоуровневый
  `segmentio/kafka-go` не экспонирует `DescribeLogDirs`, поэтому админ-клиент оставляет поле
  нулевым; `messages_estimate` рядом считается надёжно из watermarks (ListOffsets). Размер
  домешивает usecase из `sum by(topic)(kafka_log_log_size)` — метрики JMX-агента брокера. Ставить
  `kafka_exporter` для этого бесполезно: он размер не экспортирует вовсе. Подробности — §75 ниже.
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
| §35.B4 RBAC-фикс вкладки | ✅ | Failed-view = `logs:read` (viewer+); управление очередью = **operator+** (изначально admin; §35.5 открыл роль «Управление узлами», §87 опустил до `operator` — вся группа `/nodes/:id/async-queue/*` под `authedOperator`; секции «Ожидают»/управление неудачами показаны через `useRoleAtLeast("operator")`); пауза/отключение = operator+ |
| §35.F Переписать вкладку «Очередь» | ✅ | [QueueTab.tsx](../web-ui/src/components/node/QueueTab.tsx): KPI-шапка (ожидают/неудачи) + баннер (пауза/отключить) + 2 секции; failed из CH (`?done=no`), ленивое тело (`/log/:id`), replay (`ReplayDialog`), дип-линк «Открыть в логах» (`LogsInitialFilter.done`); i18n en/ru; бандл пересобран |

**Неочевидности / решения.**
- **§35 — неудачи уже в ClickHouse, дублировать Kafka-peek было ошибкой.** `send.Send` логирует запись
  (`done=false`, с reason/телом/attempts) ДО `publishDLQ` — неудачные доставки уже в CH, быстро
  доступны по индексам `done`/`date_request`. §34.6-peek читал то же самое из Kafka, но дорого. §35
  убирает peek и берёт из CH (`CountFailed` + существующий `GET /logs?done=no`).
- **§35/§36.10 — очистка неудачных доставок (обновлено).** Раньше: «удалить неудачи нельзя» (DLQ без
  DeleteRecords, CH-логи — история). Теперь оператор может принудительно очистить «Неудачные доставки»
  узла (`POST /api/nodes/:id/async-queue/purge-failed`, **manager+**): (1) ID `done=0`-сообщений за окно
  отменяются tombstone'ом (как §34.4) → DLQ-репроцессор дропает их (`result=dropped`, перестаёт повторять);
  (2) записи `done=0` удаляются из CH-таблицы узла **lightweight DELETE** → счётчик/список обнуляются сразу.
  Физически DLQ по-прежнему не чистится (tombstone + commit). Реализация: `AsyncQueueUsecase.PurgeFailed`
  → порт `FailedLogsPurger` (`LogReaderCH.FailedIDs`/`DeleteFailed`) + `QueueCancelWriter`. Без CH/таблицы —
  no-op. Тесты: unit `TestAsyncQueue_PurgeFailed_*`, integration `TestAsyncQueue_PurgeFailed_E2E`
  (qcancel в Redis + DELETE в CH). Replay и пауза/отключение остаются как раньше.
- **§36.11 — «Повторить все сейчас».** Форс-повтор всех неудачных узла (`POST
  /api/nodes/:id/async-queue/replay-failed`, **manager+**): каждое `done=0`-сообщение за окно
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
  `node_id = ?` (с 1.22.5 строго; послабление на пустой `node_id` держалось до §61 и снято совсем,
  кроме внешних таблиц §64 — см. §4.62); `GetByID` — нет (уникальный ID). `nodeID` прокинут в порты (LogReader/NodeLogMetrics/
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

### 4.1 Шифрование живёт только в `adapter/out/postgres` (узлы и app_settings)

`domain.Node` всегда хранит **открытый** plaintext. Шифрование/расшифровка происходит
исключительно в [postgres/node_repo.go](../internal/web/adapter/out/postgres/node_repo.go)
(`Create`, `Update`, `scan`) и [sender/adapter/out/nodepg/reader.go](../internal/sender/adapter/out/nodepg/reader.go).
Это намеренное архитектурное решение (§5.5 ТЗ + §17.4 «mapper.go»):
usecase не знает, что креды зашифрованы — он работает с готовым `*domain.Node`.

**§90.1 распространил правило на `app_settings`.** Четыре секрета (`sentry.dsn`,
`clickhouse.password`, `mail.password`, `notifications.telegram.bot_token`) шифруются в
[postgres/app_settings_repo.go](../internal/web/adapter/out/postgres/app_settings_repo.go);
`domain.AppSettings` снаружи — plaintext, маскирование `***` в usecase не менялось. У этой
таблицы **второй читатель мимо репозитория** —
[bootstrap/app_settings.go](../internal/platform/bootstrap/app_settings.go), через который
работают и все hot-reload'еры ([reload.go](../internal/platform/bootstrap/reload.go)), поэтому
расшифровка добавлена и туда. Подробности о разном поведении двух читателей при ошибке — §4.76.

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

### 4.5.1 Источник тела повтора — очередь, а не журнал (§96)

Журнальная копия тела режется по per-node `max_body_size` (§22.2), и до §96 повтор отправлял именно
её. На узле с лимитом это означало, что запрос, однажды не принятый приёмником, не мог быть принят
уже никогда: уходил обрезок, приёмник отвечал 500, запись повторяли снова.

Полное тело всё это время лежало рядом — в конверте `nexus.async.dlq`, откуда его читает
авто-репроцессор §36. Теперь оттуда же его берёт и повтор.

Два неочевидных места:

- **Два прохода по DLQ, а не один.** Живой хвост (от committed offset группы `-dlq-reprocess`)
  короткий и покрывает обычный случай. Но репроцессор коммитит разобранное, и конверт записи, чей
  `dlq_ttl_seconds` истёк, оказывается ПОЗАДИ committed offset — физически живой до retention
  топика, но невидимый для первого прохода. Второй проход стартует от offset по времени
  (`SetOffsetAt(received_at)`), и без нижней границы не запускается вовсе: скан топика, общего для
  всех узлов инсталляции, «с начала» стоил бы дорого.
- **Идентификатор оригинала переиспользовать нельзя.** Соблазн «просто верни конверт в топик»
  разбивается о tombstone `qcancel:<id>` (§34.4): он гасит ЛЮБУЮ копию с этим идентификатором,
  включая наш повтор. Поэтому схема прежняя — новый `ID` плюс отмена старого.

Промах поиска (нет Kafka, ошибка, упор в cap) не ошибка, а «конверта нет»: дальше работает правило
§96.4 — целая журнальная копия повторяется, усечённая отклоняется.

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
  Шрифты Inter + JetBrains Mono с §89.1 **локальные**:
  [web-ui/src/assets/fonts/](../web-ui/src/assets/fonts/) (8 woff2, 226 КБ) +
  генерируемый `fonts.css`, подключается из `main.tsx`; обновление —
  [scripts/fonts/update-google-fonts.sh](../scripts/fonts/update-google-fonts.sh).
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
- **С `wg.Go(func(){…})` (Go 1.25+) рецепт другой — и он проще.** `Done` вызывается
  defer'ом ЗА пределами переданной функции, поэтому `defer safego.Recover(...)` ставится
  просто первой строкой внутри неё: паника гасится до того, как счётчик группы
  уменьшится. Ручного `wg.Add(1)`/`defer wg.Done()` там нет вовсе, так что правило про
  LIFO-порядок к таким местам неприменимо — не пытайся его туда «вернуть»
  ([instanceprobe/prober.go](../internal/web/adapter/out/instanceprobe/prober.go)).
  `go fix` на Go 1.26 переписывает старую форму в `wg.Go` автоматически.

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
(config.yml = 64 МиБ), и `clogwire.Split` режет под этот же порог — но
**брокер** kafka в [deploy/docker-compose.yml](../deploy/docker-compose.yml) не
задавал `message.max.bytes`, и его **дефолт ~1 МиБ перебивал** per-topic-конфиг
→ сообщение >1 МиБ отвергалось, строки терялись (NDJSON-fallback убран в §38).

Это **прод-баг**, не только CI: тот же брокер-compose в проде, тот же дефолт.
Фикс — две части:
1. **Брокер.** `KAFKA_MESSAGE_MAX_BYTES`/`KAFKA_REPLICA_FETCH_MAX_BYTES = 10485760`
   в compose — брокерский потолок не ниже per-topic лимита. (После §68 значения в
   обоих compose и в example подняты до `67108864`: `receiver.max_body_bytes` 32 МиБ
   × 1.33 base64 async-envelope + запас. До 19.08.2026 было `16777216` под тело 10 МиБ.)
2. **Запас на фрейминг.** `chunkLimit` ([chlogretry/retrier.go](../internal/sender/adapter/out/chlogretry/retrier.go))
   режет под-батчи на `max.message.bytes − 128 КиБ`: `clogwire.Split` меряет
   только JSON-конверт, а Kafka добавляет обвязку record-batch/ключ/заголовки —
   под-батч ровно на лимите иначе отвергается после фрейминга.

### 4.0.5 Лимит тела 32 МиБ — четыре согласованных места, а не один параметр

19.08.2026 `receiver.max_body_bytes` поднят 10 МиБ → **32 МиБ** (33554432) под подписанные ЭЦП
пакеты 1С/СБИС: вложение едет в теле в base64 (+33%), и 10 МиБ упирались в 413 уже на файле ~7 МБ.

Одного параметра мало — тело обязано пройти четыре независимых потолка, и каждый режет в своём
месте, с разной диагностикой:

| Потолок | Где | Симптом при отставании |
|---|---|---|
| `client_max_body_size` (nginx) | фронт-прокси, DEPLOYMENT | 413 от nginx, в логах шины запроса НЕТ вовсе |
| `receiver.max_body_bytes` | Receiver, `readBody` | 413 `request body too large` (ранняя проверка по `Content-Length`) |
| gRPC `max_message_bytes` (64 МиБ, обе стороны) | sync Receiver→Sender | `ResourceExhausted` |
| `kafka.topic.max_message_bytes` + **брокерский** `message.max.bytes` | async | `Message Size Too Large`, сообщение теряется |

Грабли, которые это выкатывание вскрыло:

- **Брокерский дефолт (~1 МиБ) перебивает per-topic конфиг** — тот же корень, что у §4.0.4. На
  dev-стенде (`services`) `KAFKA_MESSAGE_MAX_BYTES` не был задан вовсе: топик `nexus.async` имел
  10 МиБ, а брокер — 1048588 байт, то есть async крупнее 1 МиБ падал бы ещё до этой правки.
- **`kafka.topic.*` применяется ТОЛЬКО при создании топика** (`CreateTopics`, `AlterConfigs` в коде
  нет): у работающей установки живые `nexus.async{,.dlq,.paused}` и `nexus.logs.retry` остаются со
  старым лимитом — процедура `kafka-configs --alter` в DEPLOYMENT.
- **`config/config.yml` в `.gitignore`, `config_debug.yml` под `skip-worktree`** — правка обоих
  НЕ попадает в репозиторий. Значение фиксируется в `config.example.yml`, иначе следующая установка
  снова получит старый лимит.
- **`cmd/echosrv` читал тело с потолком 5 МиБ** — стенд-приёмник молча усекал ответ и «подтверждал»
  проброс, которого не было. Поднят до 64 МиБ (`maxEchoBodyBytes`).

Стендовая проверка (19.08.2026, нативный запуск + стек `services`): sync 32 МиБ → 200, тело дошло
до получателя байт-в-байт (33554432 символа) и вернулось эхом; 32 МиБ + 1 байт → 413 (тело даже не
отправлено — сработал ранний отказ по `Content-Length` вместе с `Expect: 100-continue`); async
20 и 32 МиБ → доставлены через Kafka, `request_size` в ClickHouse = полный размер.

### 4.0.6 Два лимита тела: с 1.34.0 оба по 300 МиБ, но цепочки под ними разные

`receiver.max_body_bytes` был один на оба пути, и поднять sync было нельзя, не сломав async:
тело async едет в Kafka-конверте в base64 (+33%), где потолок задаёт брокер. С 1.30.1 лимита два
(`receiver.max_body_bytes` / `receiver.max_async_body_bytes`, [config.go](../internal/platform/config/config.go),
[handler.go](../internal/receiver/adapter/in/http/handler.go)).

Что важно знать при правках:

- **Три точки чтения тела, две из них async.** `handleSync` читает по sync-лимиту, `handleAsync` и
  `handleCallback` — по async-лимиту.
- **Четвёртая точка — §3.6, и её легко не заметить.** Sync-запрос к **paused**-узлу уходит в
  очередь, поэтому тело уже прочитано по sync-лимиту, а поедет в Kafka. Без отдельной проверки
  клиент получал бы 202 «принято», а брокер отвергал бы сообщение при публикации — потеря без
  следа. Проверка стоит в `handleSync` на ветке `ErrNodePaused` и отвечает 413 с ОТДЕЛЬНЫМ текстом
  (`errBodyTooLargeForQueue`): у клиента «тот же запрос, что и минуту назад» вдруг отвергается, и
  причина — состояние узла, а не размер сам по себе.
- **Нормализация лимитов — в конструкторе `New`, а не в `readBody`.** Первая версия оставляла
  «0 = дефолт» на откуп `readBody`, и при `maxBodyBytes=0` async-лимит становился нулём: гейт §3.6
  отвергал ЛЮБОЕ непустое тело. Поймали существующие тесты paused-узла (`TestShortURL_*`), а не
  новые — поэтому оба лимита нормализуются один раз, до использования.
- **Дефолт `min(10 МиБ, max_body_bytes)`** ([defaults.go](../internal/platform/config/defaults.go),
  `defaultMaxAsyncBodyBytes`): конфиг без нового ключа ведёт себя как раньше, а async никогда не
  принимает больше sync. (До 1.34.0 этот пункт и DEPLOYMENT называли дефолт 32 МиБ — расхождение с
  кодом, исправлено при подъёме sync-лимита.)
- **Стартовое предупреждение** `warnAsyncBodyLimitOverKafka` ([receiver/app.go](../internal/receiver/app.go)):
  если `max_async_body_bytes × 1.4 > kafka.topic.max_message_bytes` — warn в лог. Гейта нет
  намеренно: реальный потолок задаёт брокер, значений которого Receiver не знает, и падать на
  догадке нельзя.

Сопутствующее, без чего 124 МиБ не работают:

| Что | Было → стало | Почему |
|---|---|---|
| `receiver.read_timeout_ms` | 10 с → 300 с | включает ПРИЁМ тела; 124 МиБ на медленном канале не влезали, клиент видел обрыв, в логах шины запроса не было |
| gRPC `max_message_bytes` (3 места) | 64 → 192 МиБ | тело идёт к Sender одним сообщением; обе стороны обязаны совпадать |
| `mem_limit`/`GOMEMLIMIT` receiver | 192m/150MiB → 1g/800MiB | замер: RSS 690 МиБ на один запрос 124 МиБ |
| `mem_limit`/`GOMEMLIMIT` sender | 256m/200MiB → 1792m/1400MiB | замер: 869 МиБ на один запрос, 1.36 ГиБ на три параллельных — тела запроса и ответа живые одновременно, GC освобождать нечего |
| nginx `client_max_body_size` | 48m → 140m | режет раньше шины, ответ 413 приходит HTML'ом |
| `cmd/echosrv` | 64 → 192 МиБ | усекал ЗАПРОС на своём лимите, и стенд показывал 200 при обрезанном эхо — ложное «проброс работает» |

Стендовый прогон 19.08.2026: sync 124 МиБ → 200, у получателя ровно 130023424 байта, эхо вернулось
целым (gRPC 124 МиБ в обе стороны); 124 МиБ + 1 → 413 до отправки тела; async 32 МиБ → 200 и
доставлено, 32 МиБ + 1 → 413, 124 МиБ на async → 413; paused-узел: 20 МиБ → 202 queued,
40 МиБ → 413 «too large for queued delivery».

### 4.0.7 Оба лимита тела 300 МиБ (1.34.0): sync через gRPC, async через Kafka

02.09.2026 `receiver.max_body_bytes` поднят 124 → **300 МиБ** (314572800). Повод — боевой узел
`dconv` команды `conv`: обмен 1С встал, клиент копил непереданные данные в ОДИН растущий пакет
(46 → 62 МБ за смену, +~2 МБ/час) и получал 413 на каждой попытке раз в 15 минут. В журнале
отказов §94 это видно группой `dconv/vika` / `body_too_large`, а в логах узла — НЕТ вовсе: 413
отдаётся до записи лога, и без §94 такой отказ невидим.

**Async поднят до тех же 300 МиБ — но цепочка под ним длиннее.** Клиент бил в `/requestAsync`, и
переводить его на sync-эндпоинт не стали: async-путь подняли до того же потолка. Цена в том, что
тело едет в Kafka в base64-конверте (+33% плюс заголовки), 300 МиБ → **~420 МиБ на ОДНО
сообщение**, и звеньев, каждое из которых обязано быть поднято, пять:

| Звено | Значение | Что будет, если пропустить |
|---|---|---|
| `receiver.max_async_body_bytes` | 300 МиБ | 413 на приёме |
| `kafka.topic.max_message_bytes` | 448 МиБ | приём 200, publish падает `Message Size Too Large` |
| брокерские `message.max.bytes` / `replica.fetch.max.bytes` | 448 МиБ | то же: брокерский дефолт ПЕРЕБИВАЕТ per-topic |
| `consumer.fetch_max_bytes` / `max_partition_fetch_bytes` | 512 / 448 МиБ | сообщение записано, но не вычитывается НИКОГДА — партиция встаёт |
| heap и `mem_limit` брокера | 1792m / 2560m | OutOfMemoryError на первом крупном сообщении |

Плюс два свойства, которые не чинятся конфигом и приняты осознанно: **партиция занята на всё время
доставки** такого сообщения (а все сообщения одного узла идут с ключом `node_path` — §80.1, то есть
ждёт весь узел), и продьюсер kafka-go выделяет буфер `BatchBytes` = `topic.max_message_bytes`
(448 МиБ), из-за чего async дороже sync по памяти Receiver'а. Грабля 1.31.0 (сообщение больше
лимита топика вставляло партицию намертво) — ровно про пропуск третьего или четвёртого звена.

**Живые топики не подхватывают `kafka.topic.*`** (применяется только при `CreateTopics`,
`AlterConfigs` в коде нет): существующим `nexus.async{,.dlq,.paused}` и `nexus.logs.retry` лимит
поднимается `kafka-configs --alter`. Брокерский `message.max.bytes` — тоже динамический
(`--entity-type brokers`), рестарт брокера не нужен; проверено на стенде 02.09.2026.

Отдельно: **лимит выбирается по ЭНДПОИНТУ, а не по узлу** (`readBody` в handler'е). Поэтому смена
`root_method` узла сама по себе ничего не меняет — запрос на `/requestAsync` режется async-лимитом
ещё до резолва узла. Это и было причиной, по которой перевод боевого `dconv` на sync не помог.

Сопутствующее, без чего 300 МиБ не работают (тот же список, что в 4.0.5/4.0.6, с новыми числами):

| Что | Было → стало | Почему |
|---|---|---|
| `receiver.read_timeout_ms` | 300 с → 600 с | включает ПРИЁМ тела; 300 МиБ на канале 10 Мбит/с едут ~250 с — прежние 5 мин впритык |
| gRPC `max_message_bytes` (3 места) | 192 → 384 МиБ | тело идёт к Sender одним сообщением; обе стороны обязаны совпадать |
| `mem_limit`/`GOMEMLIMIT` receiver | 1g/800MiB → 3072m/2400MiB | замер 02.09.2026: sync 1717 МиБ на двух, async 2679 МиБ на двух |
| `mem_limit`/`GOMEMLIMIT` sender | 1792m/1400MiB → 3072m/2600MiB | замер: 2304 МиБ на двух одновременных, 2488 на трёх |
| nginx `client_max_body_size` | 140m → 320m | режет раньше шины; 413 приходит от nginx, и запроса нет в логах шины |
| nginx `client_body_timeout` | 300s → 600s | согласовано с `receiver.read_timeout_ms` |
| `cmd/echosrv` | 192 → 384 МиБ | усекал ЗАПРОС на своём лимите — стенд показывал бы 200 при обрезанном эхе |
| `receiver.max_async_body_bytes` | 10 → 300 МиБ | ради него и вся kafka-цепочка выше |
| `kafka.topic.max_message_bytes` | 16 → 448 МиБ | 300 × 1.4 (base64 + заголовки конверта) |
| брокерские `message.max.bytes` / `replica.fetch.max.bytes` | 16 → 448 МиБ | брокерский дефолт перебивает per-topic |
| `kafka.consumer.fetch_max_bytes` | 50 → 512 МиБ | иначе сообщение не вычитывается никогда |
| `kafka.consumer.max_partition_fetch_bytes` | 16 → 448 МиБ | то же, на уровне партиции |
| `KAFKA_HEAP_OPTS` + `mem_limit` брокера | 512m/768m → 1792m/2560m | сообщение живёт в heap на приёме и отдаче |

**Фронт-прокси может быть не один.** У боевого запроса в `X-Forwarded-For` два адреса — перед
nginx шины стоит ещё один прокси, и его `client_max_body_size` поднимается отдельно: иначе 413
придёт оттуда и до шины запрос не дойдёт вовсе.

**Память — главный ограничитель, и он умножается на реплики.** С §93 (receiver, sender и web —
каждый в двух экземплярах) сумма потолков ≈ 12.4 ГиБ приложения плюс ≈ 4.3 ГиБ инфраструктуры
(Kafka с 1.34.0 берёт 2560m), итого ≈ 16.7 ГиБ. Это потолки, а не
потребление (в простое ~1.5 ГБ), но если физической RAM меньше суммы, одновременный пик на двух
репликах убивает не контейнер, а хост — сверяйте с `free -g` ДО выката.

**Стартовая проверка sync↔gRPC** — `warnSyncBodyLimitOverGRPC` + чистая `syncBodyLimitFitsGRPC`
([receiver/app.go](../internal/receiver/app.go), тесты
[app_bodylimit_test.go](../internal/receiver/app_bodylimit_test.go)): если `max_body_bytes` плюс
запас под envelope (1 МиБ) не влезает в `sender_grpc.max_message_bytes` — `warn` на старте.
Симметрична `warnAsyncBodyLimitOverKafka` и по той же причине не гейт: конфигурация, где крупные
тела падают, а мелкие работают, не повод не поднять сервис. Ловит ровно тот сценарий, который
делает возможным этот релиз, — подъём лимита тела без парного подъёма gRPC (и откат кода без
отката конфигурации: тогда 413 сменится на 502). Проверено вживую на стенде: при 300 МиБ тела и
192 МиБ gRPC в логе `receiver.max_body_bytes exceeds receiver.sender_grpc.max_message_bytes`.

`cmd/echosrv` заодно перестал усекать молча: тело читается с `+1` байтом сверх лимита, и при
превышении пишется `warn` (молчаливое усечение уже давало ложную приёмку в 1.30.0).

**`config_debug.yml` синхронизирован с рабочей копией — долг закрыт.** Файл лежал под
`skip-worktree`, и правки годами шли только в локальную копию разработчика: git-версия отстала до
`max_body_bytes: 5242880` без gRPC-лимитов вовсе, при живом конфиге отладки на 124 МиБ. В этом
релизе флаг снят и закоммичена актуальная версия (localhost-адреса и креды локального стека
`services` — те же, что уже открыто описаны в [STAND_TESTING.md](../docs/STAND_TESTING.md); боевых
секретов в файле нет). Дальше правила прежние: ключи заводятся во ВСЕ три конфига сразу, иначе
файл снова отстанет (см. `[memory] feedback_config_yml_manual_prod`).

Стендовый прогон 02.09.2026 (нативный запуск + стек `services`, `GOMEMLIMIT` как в контейнере):

- **sync** (узел `bigbody-test`): 300 МиБ → 200, у получателя ровно 314572800 байт (эхо-лог
  `body_len`); 300 МиБ + 1 → 413 `request body too large` за 7 мс (ранний отказ по
  `Content-Length`, тело даже не отправлялось); три параллельных → все 200 за 2.2–2.9 с.
- **async** (узел `async-limit-test`, после `kafka-configs --alter` на топиках И брокере):
  300 МиБ → 200 с `id`, доставлено через Kafka, у получателя ровно 314572800 байт; 300 МиБ + 1 →
  413; два одновременных → оба 200 и оба доставлены. Всего 6 сообщений по 420 МиБ прошли через
  брокер без потерь, Kafka-контейнер держал ~832 МиБ.
- Обе стартовые проверки согласованности в логе Receiver зелёные: `async body limit fits kafka
  message size` (440401920 ≤ 469762048) и `sync body limit fits grpc message size`.

### 4.0.8 §97 — конфиг стал потолком, рабочий лимит живёт в настройках

Подъём до 300 МиБ (4.0.7) снял боевую блокировку, но раздал право слать такое тело всем узлам
сразу и до следующего релиза. §97 разделяет два вопроса, которые до этого отвечал один параметр:
«сколько шина СПОСОБНА переварить» (потолок, меняется выкатом — под него настроены nginx, gRPC,
Kafka и память) и «сколько РАЗРЕШЕНО сегодня» (рабочий лимит, задаёт администратор в интерфейсе).

Что важно знать при правках:

- **Дефолт 100 МиБ, а не потолок.** `domain.BodyLimitDefaultBytes`; nil в настройках означает «не
  задано», и разворачивается он в 100 МиБ и в Web (валидация/форма), и в Receiver (применение) —
  через общий `domain.BodyLimitOrDefault`. Разные дефолты по сторонам дали бы форму, показывающую
  не то, что реально применяется.
- **Провайдер хранит ПАРУ, а не два числа.** `receiver/usecase.BodyLimitsProvider` —
  `atomic.Pointer[bodyLimits]`. Причина в `handleSync`: тело читается по sync-лимиту, а ветка
  `ErrNodePaused` (§3.6) сравнивает уже прочитанное тело с async-лимитом. Два независимых
  `atomic.Int64` позволили бы увидеть половину старой пары и половину новой — с нарушенным
  инвариантом `async ≤ sync`. Handler читает пару одним вызовом `Limits()`.
- **Нормализация переехала из конструктора handler'а в провайдер** («0 = дефолт», зажатие
  потолком, `async ≤ sync`). Конструктор `httpadapter.New` больше не принимает два int — это
  сломало 6 мест в тестах, включая assert по приватному полю `h.maxAsyncBodyBytes`; тест
  нормализации переведён на публичное поведение провайдера.
- **Сид — тем же Reloader'ом, что и hot-reload** (`bootstrap.BodyLimitsReloader`, образец —
  `RejectLogReloader`). Ошибка сида не валит старт: `warn` и работа на дефолте домена. Именно на
  дефолте, а не на потолке: принять меньше, чем разрешил администратор, безопаснее, чем больше.
- **Секция события — существующая `general`.** Новую заводить не потребовалось, `sectionsFor`
  не менялся. Поля добавлены и в узкий `appSettingsOverlay` bootstrap'а — без этого Receiver их
  просто не увидел бы, а reloader молча ставил бы дефолт.
- **Потолок ≤ 0 = «не задан»**: проверяется только нижняя граница (1 КиБ). Конфиг без ключа ведёт
  себя как раньше.
- **Значение может оказаться выше потолка задним числом** (конфиг снизили после сохранения) —
  `domain.ClampBodyLimit` зажимает и возвращает признак, провайдер пишет `warn`. Это расхождение
  между тем, что администратор видит в форме, и тем, что применяется.
- **Миграции нет**: `app_settings` — singleton-строка с JSONB, новый параметр это новое поле
  Go-структуры.

Попутно исправлен дефект того же класса: маппинг ошибок в `PUT /api/settings/app` был растущей
цепочкой `||`, и в неё забыли добавить срок хранения журнала отказов (§94.5) и всю секцию почты
(§88) — невалидное значение возвращало 500 вместо 400 с текстом. Цепочка заменена списком
`settingsValidationErrors`; добавляя новую проверку в `Update`, ошибку надо внести туда же.

**Грабли стенда, которые стоили времени:**

- **`go build ./...` и `go vet ./...` НЕ покрывают `tests/integration`** — у пакета build tag, и
  два вызова изменённого конструктора `httpadapter.New` там компилировались только в
  `make test-integration`. Проверять сигнатуры надо
  `go vet -tags=integration ./tests/integration/`.
- **`body limits applied from app_settings` пишется на уровне `info`.** При `logging.level: 2`
  (типовая настройка стенда — только warn/error) строки не видно, и кажется, что reloader не
  сработал. Лимиты при этом применяются; чтобы увидеть — поднять уровень до debug.
- Консоль логов `/api/logs` отдаёт записи **новыми вперёд**: `items[0]` — самая свежая.

### 4.0.9 §98 — семь правок интерфейса, у которых общая природа

Раздел [98-ui-usability-and-node-protection.md](sections/98-ui-usability-and-node-protection.md).
Все семь пунктов объединяет одно: механика в бэкенде уже была, а интерфейс её не показывал либо
упирался в потолок, поставленный ради производительности и с тех пор не пересмотренный. Ниже —
только то, чего по коду не видно.

**Приоритет политики защиты узла стал ЧЕТЫРЁХУРОВНЕВЫМ** (§98.5):
`узел (поле > 0)` → `app_settings.general` → `sender.circuit_breaker` из конфига → дефолт кода 5/30.
До §98 уровней было три, и `resolve()` знал только про два верхних.

- **Глобальная политика больше не поле, а провайдер.** `circuitbreaker.PolicyProvider`
  ([redis.go](../internal/platform/circuitbreaker/redis.go)) на `atomic.Pointer`. Пара (порог,
  пауза) подменяется ЦЕЛИКОМ по той же причине, что у `BodyLimitsProvider` §97: `RecordFailure`
  пишет обе величины в hash состояния одним действием, и раздельные атомики дали бы «3 из 5 · проба
  через 60 с» при сохранённых 5/30.
- **Два конструктора, и это не дублирование.** `New(client, threshold, cooldown)` — статическая
  политика (тесты, вызывающий без шины настроек); `NewWithPolicy(client, provider)` — подменяемая.
  Иначе 14 тестовых вызовов пришлось бы переписать на трёхэтажную конструкцию ради одного
  продакшн-места.
- **Sender впервые подписан на секцию `general`** ([app.go](../internal/sender/app.go)) — раньше
  только `sentry`/`clickhouse`/`logging`/`masking`. Провайдер живёт в поле `App.breakerPolicy` и
  создаётся ТОЛЬКО при живом Redis: без него breaker не работает вовсе.
- **`GET /api/settings/public` отдаёт только заданные в UI значения**, а не «UI ?? конфиг Web'а».
  Политику применяет Sender, читающий СВОЙ файл конфигурации, и в инсталляции они могут разойтись
  (§81.3.1): подстановка конфига Web'а дала бы подсказку в форме узла, которой доставка не
  подчиняется.
- **Границы глобальных значений — зеркало границ поля узла** (1..100 и 1..3600) с отдельным тестом
  на само совпадение. Ноль запрещён: «не задано» выражается `nil`, иначе у одного смысла два
  представления, и `0` из формы затирал бы политику вместо «взять уровнем выше».
- **Переходное окно.** Применённая политика лежит в hash состояния, поэтому новое значение
  действует со СЛЕДУЮЩЕГО учтённого отказа; у уже открытого breaker'а прежний cooldown доживает до
  истечения ключа (10 мин). Это то же окно, которое §81.3.1 уже допускал.
- **Порядок выката Sender → Receiver → Web.** Старый Sender секцию не слушает и работает по
  `config.yml` — деградация тихая.

**Очистка неудачных: цикл вместо одного прохода** (§98.4,
[async_queue.go](../internal/web/usecase/async_queue.go)).

- §79.2 связал отмену и удаление одним набором ID ради согласованного аудита, и ценой стал потолок
  в `peekCap` (1000) записей. Инвариант сохранён — он держится ВНУТРИ батча.
- **Конечность цикла держится на ожидании мутации ClickHouse.** `syncMutationCtx`
  ([log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)) задаёт `mutations_sync=1`
  и НЕ задаёт `lightweight_deletes_sync`: последняя появилась в 23.11, и сервер постарше — ровно
  тот, ради которого страховка вводится, — отверг бы её как неизвестную настройку, сломав удаление
  целиком. На 24.x ожидание и так включено по умолчанию, интеграционный тест зелёный и без
  настройки; проверять её эффект локально нечем, и это записано в godoc, чтобы следующий не считал
  тест доказательством.
- **Цикл обязан останавливаться на пустой выборке**, даже когда сервер сказал «есть ещё»: так
  бывает, если все отобранные кандидаты оказались доставленными позже (`deliveredAmong` отсеивает
  их уже после выборки). Удалять нечего → следующая итерация вернула бы тех же кандидатов → вечный
  цикл. Слепое пятно то же, что у одиночного прохода до §98.
- Бюджет `purgeFailedMaxBatches` = 200 — про конечность запроса, а не про производительность.

**Резолв путей вкладки Kafka — серверный, и это не удобство** (§98.2,
`port.NodePathResolver`). Метрика Prometheus несёт `node` = путь БЕЗ слога команды
(`metrics.NodeLabelKey`), а клиентские резолверы (§62 поиск, §86 листинг) идут через `teamScope()`
по членствам смотрящего — без admin-bypass. Экран Kafka админский, данные кластерные: админ, не
состоящий в команде, увидел бы путь без ссылки, хотя сам путь уже показан. Однозначность проверяет
СУБД (`HAVING count(*) = 1`), а не Go: `path` уникален лишь внутри команды, и одноимённые узлы в
разных командах — штатная ситуация.

**Виртуализация журнала** (§98.6, [LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx)).

- **Единица виртуализации — `<tbody>` на запись, а не `<tr>`.** У раскрытой строки §77.1 тел два, и
  они обязаны измеряться и прокручиваться как одно целое. Несколько `<tbody>` в таблице допустимы —
  разметка остаётся настоящей таблицей, колонки не разъезжаются.
- **Ключ — id записи, а не индекс:** tail-poll дописывает свежие записи СВЕРХУ (§77.1), индексы
  сдвигаются, и кеш измеренных высот по индексу относился бы к другим строкам.
- **Обе защиты от нулевых измерений — про поведение, а не про тесты.** Нулевая высота контейнера
  означает «ещё не разложен» (скрытая вкладка); нулевая высота строки схлопнула бы суммарную высоту
  в ноль и оставила на экране одну строку. В jsdom размеры нулевые ВСЕГДА — без этих защит тесты
  журнала проверяли бы пустую таблицу и молча зеленели.
- **Потолок поднят до 50 000, а не снят.** Виртуализация разгружает DOM, но накопленные страницы
  живут в памяти JS.
- **`QueueTab` и `AuditLog` делят `useInfiniteList`, но виртуализации НЕ получили** — их потолок
  остаётся 1000. Меняя хук, помнить про трёх потребителей.
- **Тесты журнала считали строки в DOM** как показатель загруженных записей. После виртуализации
  это разные величины: они переведены на счётчик «Показано N» (он берёт длину списка). Клик по
  строке «с номером N» тоже перестал работать — нужная строка может лежать вне окна виртуализации.

**Секреты и менеджер паролей** (§98.7, [secretMask.ts](../web-ui/src/lib/secretMask.ts)).

- Менеджер Chrome/Edge опознаёт поле **по `type="password"`**, а не по `autocomplete`: `new-password`
  лишь меняет предложение с «подставить» на «сгенерировать». Поэтому секрет чужой системы
  маскируется CSS (`-webkit-text-security`) на обычном текстовом поле.
- **Пометить надо и СОСЕДНЕЕ поле логина** — менеджер ищет пару. `PASSWORD_MANAGER_OFF`
  применён к логину узла (входящему и исходящему), пользователю RabbitMQ, ClickHouse и SMTP.
- **Умолчание инвертировано:** почти все `SecretInput` — секреты чужих систем, поэтому защита стала
  поведением по умолчанию, а форма сброса СОБСТВЕННОГО пароля получила явный `ownPassword`. Так
  следующее добавленное поле секрета получает верное поведение, не вспоминая про флаг.
- Где `-webkit-text-security` не поддержан (`CSS.supports`), поле возвращается к `type="password"`.
  Ветки «показать открытым текстом» нет и быть не должно.

**Флаг `*_set` относится к строке кредов, а не к типу авторизации** (§98.1). У узла с
`auth_type=token` `auth_credentials_set` остаётся `true` и после переключения формы на `basic` —
подсказка «Установлен» в поле пароля была бы ложью, а `buildPayload` отправил бы пустое значение, и
сервер по контракту Update сохранил бы СТАРЫЙ токен. Отсюда гейт «тип не менялся» в
[secretPlaceholder.ts](../web-ui/src/lib/secretPlaceholder.ts).

**Экран отказов стал team-scoped и в адресе** (§98.3). `/logs/rejected` внесён в
`TEAM_PARAM_ROUTES` (§76): исключён оттуда был `/logs` — консоль служебных логов инстанса, к
команде отношения не имеющая, а журнал отказов читается team-scoped эндпоинтом, и ссылка без
`?team=` открылась бы у получателя в его команде как «не найдено».

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

### 4.34 Смена команды при открытой ноде: 404-цикл, чужие данные на экране, затирание формы

Жалоба «кнопка Обновить долго висит» вскрыла четыре независимых дефекта.

- **На экране оставался узел ЧУЖОЙ команды со старыми данными.** После переключения команды все
  запросы узла отдают 404 (team-scope, `NodeUsecase.Get`), но react-query держит последние успешные
  `data`. Проверка `if (!node) → «Узел не найден»` ([NodeDetail.tsx](../web-ui/src/pages/NodeDetail.tsx))
  для этого сценария **мертва**: `isLoading` уже `false` (рефетч успешного запроса не `pending`), а
  `node` не пустой. Ни ошибки, ни редиректа — самый неинформативный исход. Фикс: явный разбор 404
  (`isNotFound`, [api/client.ts](../web-ui/src/api/client.ts)) → `navigate("/", {replace: true})`.
- **404 ретраился и давал вечный спиннер.** Глобальная политика ([queryClient.ts](../web-ui/src/lib/queryClient.ts))
  исключала из ретраев только 401/403 → 404 повторялся 3× с бэкоффом ≈3с. Поверх этого
  `refetchInterval` (узел 5с у RabbitMQAsync, метрики 12с, логи 12с) перезапускал цикл снова и
  снова: `useIsFetching() > 0` практически всегда → кнопка «Обновить» **бесконечно** disabled.
  Фикс: не ретраить весь класс 4xx (ответ «так не бывает» повтором не исправить); ретраи остаются
  для 5xx/сетевых. Плюс `refetchInterval` узла гасится при `q.state.error`.
- **Кнопка «Обновить» была глобальной.** `useIsFetching()` без фильтра считал ВСЕ запросы SPA
  (включая поллинг шапки и соседних страниц), а клик звал `qc.invalidateQueries()` без аргументов —
  инвалидация кеша всего приложения. Сужено фильтром `q.queryKey[1] === id`: у всех запросов
  страницы узла id идёт вторым элементом ключа (`["node", id]`, `["node-metrics", id, …]`,
  `["logs", id, …]`, `["aq-*", id, …]`).
- **Форма редактирования затиралась фоновым рефетчем** ([NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)):
  `useEffect(… setForm(existing.data), [existing.data])` без защиты + глобально включённый
  `refetchOnWindowFocus` → ушёл в другое окно, вернулся — несохранённые правки исчезли. К командам
  отношения не имеет, воспроизводилось всегда. Фикс: `filledRef` — форма заполняется из сервера
  ровно один раз.
- **Почему на форме редактирования НЕ редирект:** он уничтожил бы несохранённые правки. Там баннер
  `node.foreign_team` + заблокированное сохранение (PUT всё равно вернёт 404 — team-проверка в
  `NodeHandler.Update` перед записью; данные в чужую команду не утекают). Редирект — только на
  странице просмотра.
- **Мёртвая запись в кеш-политике:** `TEAM_INDEPENDENT_KEYS` содержал `"settings-public"`, тогда как
  реальный ключ — `["public-settings"]` ([lib/nodeUrl.ts](../web-ui/src/lib/nodeUrl.ts),
  [node/useNodeMetrics.ts](../web-ui/src/components/node/useNodeMetrics.ts)). Перевёрнутое имя не
  совпадало ни с чем → публичные настройки зря перечитывались при каждой смене команды. Имя в этом
  списке обязано совпадать с `queryKey[0]` — опечатка тут не ломает ничего явно и потому живёт долго.

### 4.33 «Настройки» вне скоупа команды + команда API-токена выбирается явно

- **Что было.** Токен молча наследовал `s.CurrentTeamID` — состояние тим-свитчера в момент клика
  «Создать» ([api_token_handler.go](../internal/web/adapter/in/http/api_token_handler.go)). В UI
  команда не выбиралась, не показывалась в списке и не упоминалась в §7.14 (спека диалога писалась
  в v1, до §18/Phase 10, и не была досинхронизирована). Пользователь получил токен, видящий одну
  команду, ничего не выбирая. **Скоуп — by design (§18.3), непрозрачность скоупа — дефект.**
- **Кеш-политика Настроек была ложной.** `invalidateTeamScoped` ([lib/teams.ts](../web-ui/src/lib/teams.ts))
  сбрасывает всё, чего нет в `TEAM_INDEPENDENT_KEYS`. Из ~10 ключей Настроек **ни один** не был
  team-scoped на бэкенде: `app-settings` глобальны, `/api/users` глобален по построению (в хендлере
  об этом прямой комментарий), `/api/teams` отдаёт все команды, участники скоупятся `:id` из URL,
  каталоги — общие. То есть 4+ вкладки перезапрашивались зря. Кульминация: `/api/users` читался под
  `["users-all"]` (освобождён) и `["users"]` (не освобождён) — один эндпоинт, две противоположные
  политики. Теперь принцип зафиксирован в §7.14.1 и в комментарии у константы: **раздел «Настройки»
  не реагирует на переключатель команд**; в скоупе остаются рабочий стол/логи/аудит/метрики.
- **Почему не «список токенов по текущей команде»** (первая, отвергнутая итерация): тогда Настройки
  сами становятся скоуплены, и получается «одна вкладка реагирует, другая нет». Плюс токен,
  созданный для другой команды, сразу исчезал бы из списка («создал — не вижу»). Решение: список —
  все мои токены (колонка «Команда» обязательна как признак), команда — селект в форме.
- **Валидация членства обязательна именно из-за селекта.** Пока `team_id` брался из сессии, проверка
  была не нужна (членство проверено в `SwitchTeam`/`resolveLoginTeam`). Как только поле приходит от
  клиента — без проверки любой выпишет себе токен в чужую команду и обойдёт §18. Реализовано узким
  интерфейсом на стороне консьюмера (`teamMembershipLister`, один метод) — не жирный `port.TeamRepo`
  (11 методов); прецедент — §49 `FavoriteTeamRepo`. `ErrPermissionDenied` → **403** (отказ в праве,
  не кривой ввод). `teams == nil` → проверка пропускается (прецедент §49 «nil → фича выключена»),
  в проде `app.go` всегда передаёт `teamRepo`.
- **Срок действия токена был фикцией.** UI слал `expires_in_days`, handler принимал
  `ExpiresAt *time.Time json:"expires_at"` — поле молча терялось, **все боевые токены бессрочные**
  вопреки выбранным в интерфейсе 365 дням. Контракт согласован по UI (`expires_in_days`), дату
  считает сервер: часы клиента на срок не влияют. Рядом чинился `CreateResp`: UI ждал `api_token`,
  бэкенд отдаёт `info` → `created.api_token` всегда был `undefined` (работало лишь потому, что
  читался только `r.token`).
- **Инвариант §18.3 не был покрыт ничем, кроме комментариев:** в `api_token_middleware_test.go` не
  было ни одного упоминания team, в `api_token_test.go` `"team-1"` передавался без единого assert'а.
  Закрыто тестом «псевдо-сессия получает `CurrentTeamID == token.TeamID`».
- **Ловушка на будущее (не трогать наивно):** семантика пустого `teamID` рассогласована —
  `NodeUsecase.Get("")` = «любая команда», `NodeUsecase.List("")` = «default-team»
  ([node.go](../internal/web/usecase/node.go)). Пока это живо, выражать «все команды» пустой строкой
  нельзя: получится тихая дыра в изоляции. Мульти-командный токен (`TeamIDs []string`) потребовал бы
  правки ТЗ §18.1/§18.3 и ~30 call-sites скоуп-слоя — сознательно не делаем.

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
- **`sessionStorage`, а не `localStorage` — из-за звёздочки §44.B.** Живой период в localStorage
  **всегда перекрывал бы** пользовательский дефолт, и звёздочка «По умолчанию» перестала бы
  наблюдаться вообще. С sessionStorage новая вкладка стартует с дефолта — механизмы не
  конфликтуют. Второй довод: фильтр — состояние сиюминутной задачи; «прилипший» на неделю фильтр
  даёт непонятно пустой список при следующем заходе. (Прецеденты хранения расходились — §44.B
  выбирал localStorage, §49 отверг его в пользу PG; с §71 дефолтный период тоже уехал в PG, но
  аргумент про перекрытие остаётся в силе.)
  Ключ зеркала намеренно **общий для всех команд** и после §71: раз явно выбранный период переживает
  переключение команды, он переживает его единообразно — и через URL, и через зеркало.
- **`saveFilters` строго ДО `setParams`** ([Overview.tsx](../web-ui/src/pages/Overview.tsx),
  `updateFilters`). Если зеркалить фильтры эффектом на `[params]`, то при ручной очистке
  restore-эффект в том же коммите увидит «URL уже пуст, зеркало ещё непусто» и воскресит только что
  очищенный фильтр. Замыкает логику то, что при всё-дефолт `saveFilters` делает `removeItem`:
  «очистил» ⇒ «восстанавливать нечего» ⇒ restore no-op. Проверено на стенде.
- **Дефолтный период не сериализуется** (`serializeFilters` сравнивает с `defaultFilters()`, куда
  дефолт с §71 приходит параметром — дефолт команды из серверных префов). Отсюда следствие, которое
  выглядит как баг, но является ценой живой звёздочки: ссылка, отправленная с дефолтным периодом
  отправителя, откроется у получателя с **его** дефолтом; с §71 расхождение возможно даже у одного
  человека в разных командах. Чтобы зафиксировать период в ссылке, нужен пресет, отличный от дефолта.
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

### 4.51 §70 — несколько нод на одном ClickHouse: что неочевидно

- **Имя базы НЕ доказывает владения.** Слаг команды допускает `_`, поэтому нода без идентификатора
  с командой `kz_edo` и нода `kz` с командой `edo` дают одно и то же `nexus_kz_edo`. Регулярками
  это не ловится — только маркером `__nexus_owner` внутри самой базы. Отсюда следствие: проверка
  обязана срабатывать и в рантайме (`TeamUsecase.Create` → `CreateDatabase` → `Claim`), а не только
  на старте, иначе создание команды тихо подключит ноду к чужим данным.
- **Пустой `instance.id` — не идентичность.** У всех нод, существовавших до §70, маркер несёт
  `instance_id = ''`, и вторая такая нода признала бы чужую базу своей. Поэтому гейт первого
  запуска опирается на признак «свежая PostgreSQL» (`Identity.FreshPG`: нет узлов, команд не больше
  сидированной), а не на маркер, и проверяется ДО вычисления вердикта.
- **Движок маркера выбран под orphan-скан.** Скан ищет `engine LIKE '%MergeTree%'`, поэтому
  `TinyLog` в него не попадает — иначе админ удалил бы маркер кнопкой «Удалить бесхозную таблицу»,
  и база стала бы ничьей. Плюс в SQL скана добавлен `name NOT LIKE '\_\_nexus\_%'` (заодно скрыл
  брошенные `__nexus_tmpl_check_*` от проверки шаблона §19.4).
- **Захват атомарен без блокировок.** `CREATE TABLE` **без** `IF NOT EXISTS` — это compare-and-swap:
  ClickHouse сериализует создание одноимённой таблицы, проигравший получает код 57 и читает чужой
  маркер. Отдельный случай — маркер существует, но пуст (сосед умер между `CREATE` и `INSERT`):
  пять повторов по 200 мс, затем запись себя, иначе база осталась бы ничьей навсегда.
- **Две разные строгости — намеренно.** `OwnsTable`/`AssertOwns*` (fail-closed: чужая, без маркера,
  неизвестно → отказ) стоят перед разрушающим. `MayManageTable` (допускает базу без маркера) —
  перед идемпотентными стартовыми `ADD COLUMN`: при обновлении ноды до §70 маркеры появляются
  только после первого старта Web, и строгая проверка молча выключила бы обслуживание схемы, если
  Sender обновился раньше.
- **Сохранение узла спрашивает «чужая ли», а не «наша ли»** (`IsForeignTable`). Строгая проверка
  запрещала бы правку конфигурации, пока ClickHouse недоступен или маркеров ещё нет, — то есть
  состояние стороннего сервиса блокировало бы работу оператора.
- **Метка метрик называется `nexus_instance`.** Имя `instance` занимает сам scrape Prometheus
  (адрес цели); одноимённая метка приложения молча превратилась бы в `exported_instance`, и фильтры
  по ней не работали бы. Пустой идентификатор метку/тег не добавляет вовсе: метка входит в
  идентичность ряда, и её появление разорвало бы историю графиков действующей ноды.
- **Кеш вердиктов привязан к серверу.** Адрес ClickHouse меняется из UI (§8.4), поэтому `Guard`
  подписан на `Manager.OnReload` — иначе после перевода ноды на другой сервер вердикты относились бы
  к прежнему.
- **`external_table` (§64) — не read-only.** Sender в такие таблицы пишет INSERT; флаг исключает их
  только из DDL-списков, housekeeping и §56. Поэтому «чужая база разрешена только для
  external_table» означает разрешённую межнодовую **запись** — это осознанное решение (§70.6), а не
  недосмотр.
- **Down-миграция 0029 не восстанавливает прежний CHECK** `{0,31}`: на ноде с суффиксом или с
  длинным слагом он не наложится и откат упал бы на середине.
- **Три дефекта, которые нашёл только живой стенд** — юнит и integration их пропустили, потому что
  все три про поведение процесса и про состояние реальной ноды:
  1. гейт возвращал ошибку из `App.Start`, а сервис-обёртка гасит её логом: в неинтерактивном
     режиме процесс продолжал жить, и «нода не должна стартовать» превращалось в «нода запущена и
     молчит». Гейт перенесён в `main` (`bootstrap.MustCHOwnership` → `os.Exit(1)`);
  2. признак первого запуска «свежая PostgreSQL» делал ноду незапускаемой: узлов нет и на втором
     старте, поэтому гейт бил по её собственной базе. Добавлен флаг `instance_identity.ch_claimed`
     (миграция 0030), выставляемый после первого успешного захвата;
  3. одного `ch_claimed` тоже мало: при обновлении ДЕЙСТВУЮЩЕЙ ноды до §70 флага ещё нет, и она
     отказывалась подниматься на собственных данных. Итог: гейт требует ОБОИХ признаков, а свой
     маркер при непустом `instance.id` снимает подозрение сразу — идентификатор уникален, значит
     база наша (у ноды без идентификатора маркеры неразличимы, там гейт остаётся строгим).
- **Четвёртая итерация (1.21.1): «база есть» ≠ «есть сосед», если база ПУСТАЯ.** В 1.21.0 любое
  чистое развёртывание через `deploy/docker-compose.yml` не поднималось: сервису `clickhouse`
  задан `CLICKHOUSE_DB: nexus_default`, образ создаёт эту базу ДО старта Web, гейт видел
  `verdict=unclaimed` при свежей PostgreSQL и валил Web в цикл перезапуска — **безвыходно**, потому
  что захватить базу может только Web, а PostgreSQL остаётся свежей, пока он не стартовал. Фикс —
  `Guard.claimIfEmpty` ([owner.go](../internal/platform/clickhouse/owner.go)): база без маркера и
  **без единой таблицы** усыновляется, гейт первого запуска до неё не доходит. Граница послабления
  (в коде и в §70): база с таблицами, но без маркера, по-прежнему валит старт; пустую базу
  соседа-до-§70 мы пометим своей и будем писать вдвоём — это ровно поведение до §70 (`node_id`, §61).
- **Как это проскочило все гейты и чем ловить впредь.** Дефект виден только при подъёме
  прод-compose на пустых томах, а это делает лишь job `loadtest` — он `auto-run` только на
  `master`/теге, на `dev` — `manual`. §70 мержился в dev, где его никто не кликал; на теге v1.20.2
  §70 ещё не было; юнит- и integration-тесты compose не поднимают, а `TestMultiInstance*` вообще не
  попадали ни в один `-run` локальных целей `make test-int-*` (добавлены в `test-int-ch`).
  **Правило: трогаешь логику СТАРТА сервисов — гоняй `loadtest` (кнопка ▶ на dev) или руками
  `docker compose up --wait` на пустых томах.** Регрессия закрыта
  `TestMultiInstance_FirstRunAdoptsEmptyDatabase` + `…RefusesNonEmptyUnmarkedDatabase`.

### 4.52 §71 — персональные предпочтения: что неочевидно

- **Уникальность через индекс по `COALESCE`, и это не украшение, а условие работоспособности.**
  Глобальный преф хранится строкой с `team_id IS NULL`. С обычным `UNIQUE` по колонкам две такие
  строки считаются различными (`NULL <> NULL`), `ON CONFLICT` для них не срабатывает, и upsert молча
  превращается в append: таблица растёт, а чтение отдаёт произвольную из копий. Держит инвариант
  integration-тест «двойной upsert глобального префа даёт ОДНУ строку».
  **Грабля, которую едва не пропустили:** просится `UNIQUE NULLS NOT DISTINCT`, и первая редакция
  миграции была именно такой — но это PostgreSQL 15+, а боевые инсталляции работают на **12**.
  Тесты (testcontainers) и docker-compose поднимают 16, поэтому все гейты были зелёными, а на бою
  миграция не применилась бы и Web/Receiver не поднялись бы вообще. Отсюда два следствия: выражение
  `ON CONFLICT` в репозитории обязано совпадать с выражением индекса, и **дефолтный образ в
  integration-тестах — минимальная поддерживаемая версия** (`postgres:12-alpine`,
  `defaultPostgresImage` в `tests/integration/node_repo_test.go`, переопределяется
  `NEXUS_TEST_PG_IMAGE`). Тестировать на версии свежее боевой — значит не тестировать совместимость.
- **Два FK, а не один.** Составной FK на `user_teams` даёт инвариант «преф ⊆ членство» и каскад на
  исключение из команды, но по правилу MATCH SIMPLE строки с `team_id IS NULL` он не проверяет — их
  каскад закрывает отдельный FK на `users(id)`. Тест «удаление пользователя сносит и глобальные
  префы» держит это поведение.
- **Гейт готовности фронта — `!isPending`, а не `isSuccess`.** Рабочий стол ждёт префы, прежде чем
  запросить метрики (иначе первый запрос ушёл бы с 24ч, а следом второй — с настоящим дефолтом;
  двойная нагрузка на ClickHouse на каждый заход). Если бы флаг готовности требовал успеха, то при
  недоступном `/api/me/prefs` (5xx, сеть, старый бэкенд без эндпоинта) он не наступил бы **никогда**
  и страница навсегда осталась бы без метрик — настройка интерфейса уронила бы основную функцию.
  Второй рубеж на бэке: `PreferenceUsecase.Preferences` не возвращает ошибку вообще.
- **Префы читаются одним запросом на всего пользователя** (`GET /api/me/prefs` отдаёт и глобальные,
  и по всем командам). Иначе при переключении команды её дефолт приходил бы позже смены `teamId`:
  период мигал бы, а `serializeFilters` успел бы записать в URL значение, посчитанное против чужого
  дефолта. По той же причине `me-prefs` внесён в `TEAM_INDEPENDENT_KEYS` — это не «фича вне команд»,
  а ровно наоборот.
- **Дефолт передаётся в `overviewFilters` параметром, а не читается внутри.** Раньше модуль сам
  ходил в `localStorage`; теперь значение асинхронное, и чистые функции остаются чистыми. Параметр
  обязательный намеренно: опциональный со значением 24ч тихо сохранил бы старое поведение в забытом
  call-site, а так его отсутствие ловит `tsc`.
- **Сброс периода при смене команды и ref-guard первого появления `teamId`.** Сброс безусловный:
  переключился на другую команду — видишь её период, даже если до этого выбрал период руками. Так
  решено по обратной связи после первой редакции §71, где ручной выбор сохранялся: работая в двух
  командах с разными горизонтами наблюдения, период приходилось переставлять после каждого
  переключения.
  Ловушка в реализации: `useCurrentTeamID()` отдаёт `""` до загрузки членств, поэтому наивная
  проверка «идентификатор изменился» срабатывает на переходе `"" → team-a` и затирает период из
  прямой ссылки при обычном открытии страницы. Отсюда одноразовый ref-guard (`seenTeam`), где
  первое разрешение `teamId` сбросом не считается. Оба свойства закреплены тестами: «переключение
  команды сбрасывает даже период, выбранный вручную» и «прямая ссылка `?range=30d` уважается при
  первой загрузке» (второй проверяет ещё и что запрос метрик ушёл ровно один раз).
- **Период не зеркалится в `sessionStorage` (§54), в отличие от остальных фильтров.** Уход на
  страницу узла и возврат по кнопке «Узлы» дают дефолт команды, а не период, выбранный когда-то
  раньше и, возможно, в другой команде. Реализовано как `serializeMirror` — обёртка над
  `serializeFilters`, вычищающая `range`/`from`/`to`; на чтении те же ключи отбрасываются, чтобы
  зеркала, сохранённые прежними версиями SPA, не воскрешали период. Побочный эффект: сценарий §58
  (авто-переключение команды при открытии шаренного узла) закрывается сам собой — Overview в этот
  момент размонтирован, и reset-эффект там не работает.
- **Лимит записей проверяется раньше FK.** У пользователя с достигнутым потолком запись в чужую
  команду вернёт `ErrPreferencesLimit`, а не `ErrUserNotTeamMember`: условие `WHERE` отсекает
  вставку до того, как сработает внешний ключ. Это учтено в integration-тесте (чужая команда
  проверяется на пользователе без префов).
- **`42P08` на upsert'е.** Ключ встречается в запросе дважды (в `INSERT` и в `EXISTS`), и без
  явного `$3::text` в обоих местах PostgreSQL выводит для параметра разные типы и падает с
  «inconsistent types deduced for parameter». Та же грабля, что с повторным `$1` в §65.

### 4.53 §72 — серверная фильтрация логов: что неочевидно

- **«Ошибки» — полное дополнение «ОК», и это не косметика.** Прежние SQL-предикаты
  (`status BETWEEN 200 AND 299` / `status >= 400 OR status = 0`) оставляли дыру: запись с 3xx не
  проходила НИ ОДИН из быстрых фильтров и исчезала из UI при любом выборе, кроме «Все», а
  незавершённая запись с кодом 2xx числилась успехом. Теперь `condLogOK` /
  `condLogErr = NOT condLogOK` в [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)
  и зеркало `logRecordOK` в [logs.go](../internal/web/usecase/logs.go) — сумма фильтров равна всему
  набору. Взят ровно тот предикат, который применял клиентский фильтр списка и применяет красная
  подсветка строки: серверный фильтр показывает то же, что видел пользователь.
- **Реализаций предиката две — SQL и in-memory (live-tail), менять только парой.** Разъедутся —
  и один и тот же фильтр даст разные наборы в snapshot и в Live. Инвариант дизъюнктности закреплён
  тестами `TestSearchConds_StatusComplement`, `TestMatchLogFilter_StatusComplement` и
  `TestClickHouse_StatusFilterComplement_E2E`.
- **Прежняя семантика не была запинена ничем.** В фикстурах `TestClickHouse_WriteAndRead` и
  `TestClickHouse_SearchExtended_E2E` не было ни 3xx, ни «2xx + done=0», поэтому обе семантики
  давали там одинаковый результат. Дыру покрытия закрыли три теста выше — прежде чем менять
  предикат, стоит проверить, что новый тест на старом коде действительно красный.
- **Семантика `done` намеренно не тронута.** На `done = 0` держатся KPI неудачных доставок §35
  (`CountFailed`), `FailedIDs`, `DeleteFailed` и счётчики дашборда §44; смена задела бы пороги
  Telegram-алертов §20.3. `Status` и `Done` — независимые оси, а не два вида одного фильтра.
- **Параметры фильтра логов собирает ровно один модуль** —
  [logsQuery.ts](../web-ui/src/lib/logsQuery.ts). До §72.2 их строили три места по-разному: список
  (`useInfiniteQuery`) слал всё, кроме `status`/`done`; счётчик `/logs/count` — включая их;
  SSE-стрим — своим `URLSearchParams` вообще без них. Отсюда и «Показано 3 из 33»: список
  фильтровался в браузере по загруженной странице, а счётчик — сервером по всей таблице. Теперь
  набор общий, и разъехаться они не могут.
- **`status`/`done` обязаны входить в `queryKey` списка.** Иначе react-query отдаёт прежний кеш и
  список не перезапрашивается — фильтр снова превращается в «спрятать строки уже загруженной
  страницы». Это самая незаметная часть правки: без неё всё выглядит рабочим, пока не посмотришь в
  сеть.
- **Признак конца истории (`items.length < limit`) при серверном фильтре верен.** `LIMIT` в
  ClickHouse применяется ПОСЛЕ `WHERE`, поэтому недобор страницы означает «отфильтрованные записи
  кончились», а не «фильтр выел строки».
- **Live-буфер чистится при (пере)подключении стрима.** Раньше несоответствующие фильтру записи из
  буфера дочищал клиентский пост-фильтр; с его удалением они остались бы висеть на экране после
  смены фильтра. Сброс — в начале SSE-эффекта, вместе с подсветкой и счётчиком «новых».
- **Окно по `date_request` НЕ отсекает партиции — нужен дубль по `date_create`.** Таблица логов
  партиционируется `toYYYYMM(date_create)`, а фильтр времени read-path строился только по
  `date_request` (второе поле ключа сортировки). Казалось, что монотонная цепочка
  `toUnixTimestamp64Milli(toDateTime64(...))` даст ClickHouse достаточно для прунинга. Замер на
  200 000 строк (окно в сутки): **без сужения 200 000 прочитанных строк и 3 части, с сужением —
  8 192 строки и 1 часть**. Поэтому `searchConds` дублирует то же окно по `date_create` с запасом
  ±1 день (`dateCreateConds`, [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go)).
- **Грабля замера: на маленькой таблице выигрыш не виден и вывод получается обратный.** Первая
  версия проверки создавала 900 строк в трёх партициях и показывала «одна часть читается и так» —
  на этом основании оптимизацию едва не выбросили как бесполезную. Дело в объёме: пока данных
  меньше гранулы, статистика частей ничего не решает. Мерять прунинг ClickHouse на сотнях строк
  бессмысленно; в `TestClickHouse_DateCreatePruning_E2E` объём набивается одним
  `INSERT ... SELECT FROM numbers` (batch-writer на 200k строк упирается во flush).
- **Гейт `external_table` §64 обязателен, и это проверено падением.** Сужение опирается на
  инвариант write-path Nexus (`DateCreate == DateRequest`, send.go/dlq_reprocess.go). В чужую
  таблицу пишет посторонний сервис, и подтест с записью, у которой `date_create` не связан с
  `date_request`, показывает: без гейта она пропадает из выборки. Флаг доезжает до адаптера как
  **capability** (`LogQuery.DateCreateAligned`), а не как копия поля узла: порт описывает, что
  адаптеру разрешено, а нулевое значение безопасно.
- **Границы считаются в Go как UTC** (`dayUTC`): `date_create` — UTC-день записи, а `DateTime`
  рендерится в часовом поясе сервера, поэтому `toDate(date_request)` в SQL дал бы сдвиг.
- **Сужение работает только когда задано окно.** На вкладке «Логи» без периода первая страница
  по-прежнему читает всю таблицу; со второй страницы верхняя граница приходит из keyset-курсора, и
  прунинг включается сам. Вкладка «Очередь» всегда шлёт период, поэтому выигрывает целиком.
- **Keyset-пагинация живёт в одном хуке** — [useInfiniteLogs.ts](../web-ui/src/lib/useInfiniteLogs.ts)
  (курсор `to`+`before_id`, дедуп по `id`, потолок §44.K, догрузка по скроллу и при недоборе высоты).
  Кроме журнала логов его использует секция «Неудачные доставки» вкладки «Очередь»: там стоял
  обычный `useQuery` с жёстким `limit=50` **без пагинации вообще** — KPI показывал 340 неудач, а
  посмотреть можно было 50. Симптом тот же, что у §72.2, но код был другой, поэтому первым фиксом
  не закрывался.
- **Возврат к верху схлопывает кеш до первой страницы.** `useInfiniteQuery` при авто-рефетче
  перезапрашивает ВСЕ накопленные страницы: пользователь, пролиставший список до потолка (20
  страниц) и вернувшийся наверх, давал 20 запросов к ClickHouse каждые несколько секунд — с одной
  открытой вкладки. Штатный `maxPages` не подходит: он выбрасывает ПЕРВЫЕ страницы при
  `fetchNextPage` и ломает накопительный список §44.K. Вниз страницы догружаются обычным путём.
  Схлопывание закрыто гейтом «страниц больше одной»: `setQueryData` уведомляет подписчиков даже
  когда данные не изменились, а событий скролла десятки в секунду — без гейта каждое движение у
  верха перерисовывало бы весь список (найдено ревизией §72.7).
- **Размер страницы — отдельный параметр хука, а не поле `params`.** Признак конца истории
  сравнивает `items.length < pageSize`; если бы `limit` жил в общем словаре параметров, его
  пропажа дала бы `NaN`, сравнение — всегда `false`, и список молча догружался бы до потолка на
  пустом месте. Тоже находка ревизии: ошибка была бы не в падении, а в тихой лишней нагрузке.

### 4.54 §73 — реестр инстансов: что неочевидно

- **Ничего добавлять на соседнюю ноду не потребовалось.** Первый вопрос раздела был «что нужно,
  чтобы получить ping и версию без токена» — ответ: ничего. `GET /api/version` (§30/§34.3/§70.8) и
  `GET /ready` (§9.6) уже зарегистрированы **вне** auth-группы, на корневом движке
  ([app.go](../internal/web/app.go), `r.GET("/api/version", ...)` и `healthcheck.Register`). Вся
  работа §73 — на стороне опрашивающей ноды. Если будете «закрывать» эти эндпоинты — сначала
  посмотрите сюда: реестр инстансов сломается молча, все соседи станут `error`.
- **Проба обязана быть серверной.** Соблазн сделать её из SPA (не надо бэкенда!) разбивается о два
  барьера: CORS в проекте не настроен вообще (0 совпадений по `Access-Control`), а CSP жёстко задаёт
  `connect-src 'self'` ([security_middleware.go](../internal/web/adapter/in/http/security_middleware.go)).
  Плюс https-SPA не может обратиться к http-инстансу (mixed-content). Серверная проба снимает всё
  разом.
- **`peer_instances` ≠ `instance_identity`.** Вторая (миграция 0029) — синглтон `id=1` про
  идентичность САМОЙ ноды, на ней держатся стартовые гейты §70.5; любой её рост их сломает. Реестр
  соседей — отдельная таблица, у каждой ноды своя. Свой инстанс в неё не пишется.
- **`team_id` в реестре нет — и это не забывчивость.** У соседнего инстанса свой PostgreSQL и свой
  список команд, так что «инстанс команды vika» лишён смысла. Следствие для фронта: ключ
  `["instances"]` обязан быть в `TEAM_INDEPENDENT_KEYS`
  ([teams.ts](../web-ui/src/lib/teams.ts)) — иначе смена команды в шапке запускала бы повторный
  СЕТЕВОЙ опрос всех соседей ради тех же самых данных.
- **Недоступность соседа — статус, а не ошибка.** `InstanceProber.Probe` не возвращает `error`
  вовсе, а ручки `check`/`check-one` отвечают 200 с `last_status = unreachable`. Если сделать
  наоборот, вкладка покажет ошибку вместо таблицы, а один мёртвый адрес уронит опрос остальных.
  Закреплено тестами `TestPeerInstanceHandlerCheckAllReturnsOKForDeadPeers` и `TestPeerInstanceCheckAll`.
- **Что НЕ кладётся в `last_error`** (иначе это утечка): сырое тело ответа, текст ошибки
  `json.Unmarshal` (он вклеивает кусок входа — тот же класс, что утечка §68) и карта `checks` из
  `/ready` — в ней тексты вида `"down: dial tcp 10.11.12.13:5432: connect: refused"`, то есть
  внутренние адреса соседа. Причины сведены к фиксированному набору строк, а транспортные ошибки
  классифицируются через `errors.As` по ТИПАМ (`net.DNSError`, `tls.CertificateVerificationError`,
  `net.OpError`) — не по подстрокам: сообщения stdlib не контракт. Тесты
  `TestProbeErrorDoesNotLeakBody` и `TestProbeDoesNotLeakReadyChecks` держат это.
- **Редиректы отключены намеренно** (`CheckRedirect` → `http.ErrUseLastResponse`). Следование за
  30x увело бы серверный запрос на адрес, которого администратор не вводил и который не проходил
  валидацию; 30x трактуется как «по адресу не Nexus».
- **SSRF закрыт не полностью, и это записано в ТЗ (§73.8), а не умолчано.** Приватные диапазоны
  НЕ блокируются — соседний инстанс вполне может жить в `10.0.0.0/8`, и запрет сделал бы раздел
  бесполезным. Значит admin может заставить сервер постучаться на внутренний адрес и по коду
  ответа/таймингу узнать, открыт ли порт. Компенсации: admin-only, аудит мутаций, только http/https,
  запрет userinfo, таймаут, лимит чтения тела, наружу — только распарсенные поля.
- **Проба не трогает `updated_at`, а `PATCH` не сбрасывает кеш пробы.** Первое: опрос идёт при
  каждом открытии вкладки и превратил бы «изменено вчера» в «изменено только что». Второе: пустой
  статус после смены адреса читался бы как «не отвечает», хотя проверки ещё не было.
- **Опрос при открытии вкладки — один раз на монтирование** (ref-guard в
  [Instances.tsx](../web-ui/src/pages/settings/Instances.tsx)). Без него любой рефетч списка
  (возврат фокуса на вкладку браузера) тянул бы новые сетевые пробы всех соседей.
- **Версия инстанса = версия его Web Service.** Receiver и Sender эндпоинта версии не имеют вовсе
  (сборочные поля идут только в Sentry-release и стартовую лог-запись). В штатном развёртывании все
  три собраны из одного тега, но раздел этого не проверяет.
- **`created_by`/`updated_by` получают отдельные плейсхолдеры при одном значении.** Повторная
  подстановка одного `$N` в `INSERT` роняет pgx с 42P08 — те же грабли, что в §66 и §71.

### 4.56 Ревью кодовой базы (2026-07-31): что исправлено и почему так

Разовое ревью всей кодовой базы после §74. Автоматические гейты были чисты
(`go vet`, `golangci-lint`, `govulncheck`, `eslint --max-warnings=0`), поэтому находки —
там, куда линтеры не смотрят.

- **Конверт async продублирован намеренно, синхронность держит тест.** Sender не может
  импортировать `receiver/usecase` (нарушение Clean), поэтому копий конверта три: канон у
  Receiver, полная копия у Sender, минимальная проекция у Web (вкладка «Очередь»). Копии
  разошлись молча: у Sender не было блока `rmq`, и происхождение RabbitMQAsync-сообщений
  терялось при доставке — `encoding/json` неизвестные поля просто игнорирует, ни один тест
  этого не видел. Теперь [tests/contract/envelope_test.go](../tests/contract/envelope_test.go)
  сверяет JSON-теги обеих полных копий через `reflect`, а тест внутри `kafkaadmin` держит
  проекцию подмножеством канона. **Вывод на будущее:** дублирование структуры между сервисами
  допустимо, но обязано сопровождаться контрактным тестом — комментарий «держать синхронным»
  ничего не гарантирует.
- **У HTTP-сервера Web намеренно нет read/write-таймаутов.** Через него идут SSE-стримы
  live-tail и проксирование sync-запросов с таймаутом узла до 600 с; общий `WriteTimeout`
  рвал бы и то, и другое (ровно так вёл себя боевой `receiver.write_timeout_ms=10000`).
  Задан только `web.idle_timeout_sec` (дефолт 120) — он ограничивает простаивающие
  keep-alive соединения, а не активные запросы. Раньше это выглядело как упущение, теперь
  записано комментарием в [web/app.go](../internal/web/app.go).
- **`platform/clock` внедряется опцией, а не параметром конструктора.** `WithXxxClock` с
  дефолтом `System()` — иначе пришлось бы править десятки вызовов `New…` ради поля, которое
  в проде всегда одно. Переведены только места, где время влияет на РЕШЕНИЕ (срок токена,
  граница retention, окна метрик, курсор live-tail, сессии). **Не переведены и не должны
  быть**: замеры длительности (`start := time.Now()` → `time.Since`) и штампы
  `created_at`/`ReceivedAt` — подмена часов там ничего не проверяет, а diff растит втрое.
- **miniredis покрыл то, что раньше требовало Docker.** `ratelimit`, `queuecancel`,
  `nodestatus` и `circuitbreaker` принимают `*goredis.Client`, поэтому unit-тестов у них не
  было вовсе. Риск «miniredis не исполнит Lua half-open-пробы breaker'а» не подтвердился —
  gopher-lua отрабатывает скрипт, и свойство «после cooldown проходит ровно один пробный
  запрос» теперь проверяется без контейнеров. Многоинстансные сценарии остаются в
  integration-тестах.
- **Ветка 504 в Receiver была недостижима.** `if errors.Is(err, context.DeadlineExceeded)`
  стояла после `if err != nil { return }`, то есть `err` там всегда `nil`: таймаут внешнего
  узла ВСЕГДА отдавался клиенту как 502, вопреки комментарию в proto. Признак теперь приходит
  от Sender'а полем `SendResponse.timeout` (аддитивное, старый Sender его не заполняет →
  прежнее поведение), а Sender определяет таймаут через `errors.Is` по цепочке ошибки —
  `http.Client` оборачивает дедлайн контекста в `*url.Error`, `Unwrap` сохраняется.
  **Урок:** мёртвую ветку в конце длинной функции не видит ни один линтер — её нашла
  декомпозиция.
- **Что осознанно НЕ дробилось.** `applyDefaults` (269 строк), `metrics.New` (233),
  `Node.Validate` (160) — линейные списки без ветвлений; в godoc каждой записано, почему
  разбиение ухудшит код (у `metrics.New` — риск рассинхрона объявления ряда и его
  регистрации в `reg.MustRegister`).

### 4.55 §74 — безопасный откат: что неочевидно

- **Откат кода ломался не из-за схемы, а из-за гейта библиотеки.** `golang-migrate` в `readUp`
  первым делом зовёт `versionExists(curVersion)` и, не найдя версию в источнике, возвращает
  `no migration found for version N`. Для `AutoMigrate` это была обычная ошибка → `os.Exit(1)` →
  crash-loop под `restart: unless-stopped`. Сама схема при этом совместима: все `ADD COLUMN` в
  репозитории nullable либо `NOT NULL DEFAULT`, `SELECT *` в коде нет. Проверено эмпирически
  (PostgreSQL 12, схема 32, каталог до 0028) — и именно поэтому послабление безопасно.
- **Решение принимается сравнением версий, а не разбором текста ошибки.** `EnsureUp` считает
  `MaxLocalVersion()` по источнику миграций и сравнивает с `schema_migrations` ДО `Up()`. Ловить
  подстроку «no migration found» нельзя: это не контракт библиотеки.
- **`Migrator` держит `source.Driver` сам** (`source.Open` + `migrate.NewWithSourceInstance`
  вместо `migrate.New`): у `*migrate.Migrate` нет способа спросить максимальную версию каталога.
  `NewWithSourceInstance` при ошибке переданный источник **не закрывает** — закрываем сами,
  иначе каждая неудачная попытка оставляла бы открытый драйвер.
- **Уровень записи про «схему новее» — `error`, а не `warn`, и это осознанно.** Sentry-хендлер
  в цепочке логгера имеет собственный порог `sentry.level` (по умолчанию `2` = error), от
  runtime-уровня §51 не зависящий: `warn` в Sentry не попал бы вовсе.
- **Признак `nexus_pg_schema_ahead` держится до конца жизни процесса.** Он снимается только
  следующим стартом на согласованной схеме — потому и алерт `for: 15m`, а не мгновенный: за это
  время штатный откат успевает завершиться.
- **`--migrate-force` сверяет номер с каталогом.** `migrate.Force` сам примет любое число, и
  опечатка объявила бы базу в состоянии, которого не существует; `versionExistsLocally`
  обрабатывает `os.ErrExist` от file-драйвера как «версия есть, но без up-файла» (только down).
- **`docker compose` не даёт образам приложения имён с версией**: без `image:` в compose имя
  собирается как `<project>-<service>` (`nexus-web`), тег всегда `latest`, а прежний образ после
  `up --build` остаётся `<none>`. Отсюда `scripts/deploy/images.sh` — он лишь расставляет теги,
  чтобы `up --no-build` поднял прежний образ за секунды.
- **`images.sh rollback` проверяет ВСЕ образы до первой перестановки тега.** Половина сервисов на
  старой версии, половина на новой — худший из возможных исходов аварийного отката.
- **`rollback_info.py` печатает в UTF-8 принудительно.** Консоль Windows по умолчанию cp1251, и
  вывод со стрелкой/длинным тире (они же идут в CHANGELOG) падал с `UnicodeEncodeError`.
- **Новые integration-тесты пришлось внести в `-run` цели `test-int-pg`.** Фильтр перечисляет
  префиксы имён (`^TestMigrations` → расширен до `^TestMigrate`); иначе тест существует, но
  локально не гоняется ни одной целью — ровно то, что случилось с `TestMultiInstance*` в §70.

### 4.57 §75 — размер топиков Kafka: что неочевидно

- **`kafka_exporter` размер НЕ отдаёт — это главная ловушка задачи.** Интуитивный ход «поставим
  kafka_exporter, он же про Kafka» не работает: его метрики (`kafka_topic_partitions`,
  `kafka_topic_partition_{current,oldest}_offset`, `kafka_consumergroup_lag*`) — ровно то, что
  Nexus уже считает сам через Admin API, а размера среди них нет вовсе (README экспортёра сам
  отсылает к JMX). Единственный источник — MBean `kafka.log:type=Log,name=Size` брокера.
- **Имя метрики выбрано не произвольно.** `kafka_log_log_size{topic,partition}` — то, что даёт
  официальный `examples/kafka-2_0_0.yml` при `lowercaseOutputName: true` (`type=Log` + `name=Size`
  → `log_log_size`). Держим это имя, чтобы готовые Grafana-дашборды Kafka работали без переделки.
- **Агент — javaagent в процессе брокера, не отдельный контейнер.** Standalone-экспортёру нужен
  JMX RMI, а он в Docker требует `-Djava.rmi.server.hostname` и второго порта; промах выглядит как
  «подключились, метрик нет». Плюс официального Docker-образа `jmx_exporter` не существует (только
  jar), community-образы в прод-стек тащить не хотелось.
- **Битый `deploy/kafka-jmx.yml` роняет БРОКЕР, а не метрики.** JVM с невалидным javaagent не
  стартует. Правки конфига агента проверять на стенде до боя — это не «мониторинг сломается», это
  «Kafka не поднимется».
- **`KAFKA_OPTS` наследуют CLI-утилиты → healthcheck обязан сбрасывать переменную.** Это поймал
  стенд: `kafka-topics.sh --list` (наш healthcheck) запускается тем же `kafka-run-class.sh`,
  подхватывает `-javaagent` и пытается занять порт 7071, уже занятый брокером → «Prometheus JMX
  Exporter exiting», `exit 1`. Брокер оставался бы вечно `unhealthy`, а `sender` с
  `depends_on: kafka: condition: service_healthy` не поднялся бы вообще. Лечится префиксом
  `KAFKA_OPTS= ` в healthcheck и `docker exec -e KAFKA_OPTS= …` для ручных вызовов (в DEPLOYMENT
  §5 примеры `kafka-configs.sh` поправлены). Ни один go-тест и ни один линтер этого не видят.
- **То же правило распространилось на `KAFKA_HEAP_OPTS`.** Когда heap брокера стал явным
  (`KAFKA_HEAP_OPTS: ${KAFKA_HEAP_OPTS:--Xmx1G -Xms1G}` в трёх compose — раньше значение молча
  подставлял `kafka-server-start.sh`, и бюджет памяти не был виден нигде), переменная точно так же
  досталась бы CLI-утилите healthcheck: `kafka-run-class.sh` использует её как есть и лишь при
  ПУСТОМ значении подставляет свой дефолт `-Xmx256M` (строки 279–281 скрипта). Без сброса
  проверка каждые 10 секунд поднимала бы JVM с `-Xms1G` поверх работающего брокера — на сервере
  2 ГБ (DEPLOYMENT §5.4) это гарантированный OOM. Отсюда префикс
  `KAFKA_OPTS= KAFKA_HEAP_OPTS=` в healthcheck всех трёх compose-файлов.
- **Ноль означает ДВЕ разные вещи — отсюда флаг `sizes_available` (нашла ревизия).** Пустой топик
  даёт `kafka_log_log_size = 0` (это норма для `nexus.async.dlq`/`nexus.logs.retry`), и первая
  версия рисовала ему тот же прочерк с подсказкой «JMX-экспортёр не настроен», что и полностью
  отсутствующему источнику, — то есть отправляла оператора чинить исправный мониторинг. Теперь
  usecase возвращает `SizesAvailable` (Prometheus отдал хотя бы одну серию), и UI различает: «0 B»
  для пустого топика, «—» с подсказкой — когда источника нет.
- **Размеры НЕ кешируются вместе с метаданными.** В Redis кладутся топики без размеров, мерж идёт
  на каждый запрос (instant-запрос дешевле, чем разбор «почему размеры отстали»). Иначе после
  пропажи метрики UI до конца TTL показывал бы размеры из кеша, противореча `sizes_available=false`
  в том же ответе. Регрессионный тест — `TestKafkaTopics_SizesNotFrozenByCache`.
- **Величина суммирует реплики.** `sum by(topic)` складывает все партиции всех брокеров, то есть
  отвечает «сколько занято на дисках кластера»; при RF=3 это втрое больше объёма сообщений. У нас
  RF=1, поэтому расхождения не видно — тем легче прочитать неверно, отсюда явная оговорка в godoc.
- **Новый scrape job не подхватывается сам — Prometheus надо перезапустить (нашёл вопрос с
  приёмки релиза).** Конфиг монтируется bind'ом и читается только при старте, а
  `--web.enable-lifecycle` в compose не включён, то есть `/-/reload` не работает. Штатная
  процедура обновления (`up -d --build web receiver sender` + шаг 3a для брокера) Prometheus не
  трогает вовсе, поэтому после §75 обязателен `docker compose restart prometheus` — именно
  `restart`: `up -d` видит неизменную спецификацию сервиса («Container … Running») и оставляет
  старый конфиг в памяти, что и выяснилось на боевом выкате v1.22.2. Симптом
  пропуска коварен: брокер исправен и отдаёт метрику, но её никто не снимает, а UI показывает
  тот же прочерк с подсказкой «не поднят JMX-экспортёр» — оператор идёт чинить работающий
  брокер. Документировано в DEPLOYMENT (§9.5, раздел Kafka), в ТЗ §75.2 и в шаблоне описания
  релиза.
- **Образ Kafka стал собираемым — и собирается без сети.** `docker compose up -d --build` теперь
  строит и брокер; jar агента лежит в репозитории (`deploy/vendor/`, Apache-2.0, 2.8 МБ), потому
  что боевой сервер собирает образы сам и «нет доступа к Maven Central» означало бы аварию при
  обновлении брокера. Подмена версии или зеркала — `--build-arg JMX_AGENT_SRC=<путь-или-url>`.
  Применение требует **пересоздания контейнера** — javaagent грузится только при старте JVM, то есть простой брокера 30–60 с. В
  версионировании образов §74.5 (`NEXUS_SERVICES = web receiver sender`) брокер не участвует.

### 4.58 §76 — ссылка на команду: почему зеркало и чем оно отличается от §58

- **Бэкенда не потребовалось вообще.** Первый инстинкт — «нужен резолвер команды по slug, как
  `GET /api/nodes/{id}/team` в §58». Не нужен: членства с полем `slug` уже приезжают в
  `GET /api/me/teams`, а переключение делает существующий `POST /api/me/switch-team` по `team_id`.
  Клиентский резолв заодно бесплатно даёт no-leak — мы никогда не спрашиваем сервер про чужой
  slug, поэтому «такой команды нет» и «я не её член» неотличимы by design.
- **Ссылка по slug, а не по UUID — в отличие от §58.** У узла путь уникален лишь внутри команды,
  поэтому там пришлось брать id. У команды slug глобально уникален и **неизменяем**
  (`updateTeamRequest` содержит только `Name`), так что ссылка читается человеком и не протухает.
- **Зеркало, а не «применил и удалил».** Параметр обязан быть постоянным: он и есть ссылка на
  команду, и требование «при выборе команды добавлять параметр» иначе не выполняется. Мигающий
  параметр периодически врёт, и это нельзя проверить одним утверждением; у зеркала инвариант один —
  значение параметра равно slug'у текущей команды сессии.
- **Приём `isFetchedAfterMount` из §58 здесь НЕ работает.** В §58 резолвер помечен
  `refetchOnMount:"always"`, поэтому «свежий ответ» гарантирован. У ключа `["me-teams"]` такого
  флага нет: при тёплом кеше (`staleTime` 30 с) `isFetchedAfterMount` навсегда остался бы `false`,
  и зеркало не сработало бы ни разу. Решение принимается по кешу, протухание лечится refetch'ем по
  фокусу окна. Если когда-нибудь захочется «как в §58» — сперва добавьте рефетч ключу членств.
- **Второй ref (`awaiting`) — не перестраховка.** Между `POST /api/me/switch-team` и перечитанными
  членствами «текущая команда» — ещё прежняя. Без заморозки зеркало записало бы её в URL и затёрло
  ровно ту ссылку, которую в этот момент применяет; ссылка «самоуничтожалась» бы при открытии.
- **Состояние баннера входит в зависимости эффекта.** На 403 меняется только оно: данные членств
  react-query отдаёт той же ссылкой (structural sharing), поэтому без этой зависимости эффект не
  перезапустился бы, и параметр залип бы на недостижимой команде до следующей навигации. Поймано
  тестом, а не рассуждением.
- **Одноразовость — на значение параметра, а не «навсегда».** Явный повторный переход по той же
  ссылке переключает команду снова: это осознанное действие пользователя. Драки с ручным
  переключением в шапке при этом нет — зеркало сразу переписывает параметр на выбранную команду, и
  применять становится нечего (в §58 ту же роль играет залипание `ready`).
- **Сосуществование с §54/§71 проверено по коду, а не предположено.** `applyFilters` копирует
  чужие ключи и удаляет только `FILTER_PARAM_KEYS` → `team` переживает сброс периода при смене
  команды; `saveFilters` сериализует из объекта фильтров, а не из URL → `team` не попадает в
  sessionStorage-зеркало §54; `hasFilterParams` смотрит только свои ключи → `/?team=beta` остаётся
  «пустым URL» и восстановление фильтров из зеркала работает как раньше. Все три контракта
  закреплены тестами — правка §54 их не сломает молча.
- **Белый список маршрутов, а не чёрный.** `/nodes/*` исключены не из-за эстетики: там командой
  владеет `useEnsureNodeTeam` (§58), и два механизма переключения на одном экране дрались бы за
  сессию. Новый маршрут по умолчанию параметра не получает — это осознанный выбор в пользу
  предсказуемости.
- **Гонка двух писателей URL — единственный дефект, который нашёл только стенд.**
  `setSearchParams(prev => …)` react-router отдаёт в `prev` снимок своего рендера, а не актуальную
  строку. При смене команды в одном коммите пишут двое: зеркало §76 и рабочий стол (§71 сбрасывает
  период), и второй возвращает прежний `?team=`. Итоговая строка совпадает с исходной → location не
  меняется → повторного рендера нет → чинить некому: «переключил команду в шапке, а параметр от
  прежней». Лечение — двухфазная запись: намерение кладётся в state (гарантирует рендер после всех
  записей коммита), запись выполняется следующим рендером по актуальному `prev`. Юнит-тесты дефекта
  не видели: в модели сосед реально удалял ключ, строка менялась, рендер случался и зеркало
  чинилось само — регресс пришлось строить так, чтобы запись соседа НИЧЕГО не меняла
  (`teamShare.test.tsx`, «второй писатель URL не откатывает параметр»).
- **Мультивкладочность стала видимой.** Сессия одна на все табы (§4.15), поэтому переключение
  команды в одном табе через `staleTime` перепишет параметр в другом. Поведение не новое — данные
  в соседнем табе и раньше принадлежали новой команде, — но теперь это заметно в адресной строке.

### 4.62 Гейт владения §70.4 спрятал все логи внешней таблицы §64 (боевой инцидент, фикс в 1.22.4)

**Симптом.** Узел `PDT_PDTExchange` (команда `gate`) на внешней таблице §64 показывал пустой журнал
и нулевые KPI, `GET /api/nodes/{id}/logs` отдавал `{"items":[]}` при `logs_available: true`.
В ClickHouse при этом лежало 10 222 523 записи, и `GROUP BY node_id` давал ровно одну группу — `''`.
Раньше логи были: регресс приехал с §70 (v1.21.0).

**Механика.** [nodeFilter](../internal/web/adapter/out/clickhouse/log_reader.go) выбирает между
послаблением `(node_id = ? OR node_id = '')` и строгим `node_id = ?`. §70.4 добавил второе основание
для строгости — «таблица не принадлежит этой ноде», где владение определяется маркером
`__nexus_owner` в базе. У внешней таблицы маркера нет и быть не может: ею распоряжается посторонний
сервис. Значит для неё `!ownsTable` истинно **всегда** — а весь её поток идёт с пустым `node_id`,
потому что проставить его может только Sender. Строгий фильтр отсекал 100 % содержимого таблицы.

Два раздела ТЗ при этом противоречили друг другу, и код следовал более позднему: §64.2 обещает, что
«записи постороннего писателя имеют `node_id = ''` и по правилу §61 засчитываются узлу, пока на
таблицу ссылается ровно один узел Nexus», а §70.4 включил строгость по владению без оговорки про
§64. Поэтому фикс — приведение кода к §64, а не смена правила.

**Почему не поймали.** Ни один тест в пакете не подставлял стаб `Ownership`: все кейсы
`log_reader_attribution_test.go` шли с `ownership == nil`, при котором `ownsTable` отвечает `true`
(поведение до §70), то есть ветка «чужая таблица» не исполнялась вовсе. Тесты §70 проверяли гейт на
разрушающих операциях, а не на чтении. Integration-тест `TestLogReader_NodeIDFilter_E2E` создавал
читателя без гейта — то же слепое пятно. Урок: **включение нового гейта требует теста на КАЖДОМ
пути, куда он подмешивается, а не только на том, ради которого вводился.**

**Что сделано.** В `nodeFilter` добавлено исключение
`(!r.ownsTable(...) && !r.tableIsExternal(...))`; порядок проверок сохранён, поэтому общая таблица
остаётся строгой, даже будучи внешней. Признак читается новым методом порта
`NodeTableUsage.ExternalCHTables` — зеркалом существующего `ListClickHouseTables`
(`AND external_table` вместо `AND NOT external_table`).

**Правило деградации: строгость включают только доказанные факты.** `tableIsExternal` при
отсутствующем снимке фактов возвращает `true`. Это не «считаем внешним всё подряд», а симметрия с
`tableIsShared`, которая в той же ситуации уже уходит в мягкую ветку. Правило читает оба факта, и
если один деградирует в мягкую сторону, а другой в строгую, недоступность PostgreSQL превращается в
пропажу логов.

**Кеш: один снимок вместо двух карт.** `usageCounts map[string]int` заменён на `usageFacts
*tableFacts` (счётчики + множество внешних таблиц), оба запроса идут одним циклом под общим
single-flight, таймаутом и анти-штормовой паузой; ошибка любого отменяет обновление целиком. Причина
— полуснимок (свежие счётчики + протухшее множество внешних) читается правилом как согласованный, а
его цена — молча спрятанные логи. Снимок неизменяем: обновление кладёт новый указатель, а не правит
карты на месте.

**Границы фикса.** Смягчена только видимость записей. Разрушающие операции гейт держит по-прежнему:
`DeleteFailed` («Очистить неудачные») упирается в `AssertOwnsTable` до построения условий и на чужой
таблице отказывает — закреплено ассертом в `TestLogReader_ExternalTableAttribution_E2E`. Общая
таблица (в том числе смешанная — внешний узел плюс обычный) читается строго: счётчик §61 срабатывает
раньше признака внешности. Послабление включает оператор, пометивший узел внешним, а не эвристика по
состоянию ClickHouse.

**Продолжение в 1.22.5: послабление снято везде, кроме §64.** По итогам замера (см. ниже) правило
перевёрнуто: `nodeFilter` строгий по умолчанию, мягкий — только на одиночной внешней таблице.
Следствия: `ownsTable` в read-адаптере стал не нужен и удалён (владение на выбор фильтра больше не
влияет, гейт остался на `DeleteFailed`); очистка неудачных у одного узла перестала сносить записи
без идентификатора — раньше `DELETE … WHERE (node_id = ? OR node_id = '')` удалял и их.

**Замер, на котором основано решение** (5 млн строк, стенд, 7 прогонов, `use_uncompressed_cache=0`):
строгий и мягкий варианты читают **одинаковые** 1.40 млн строк и 59.72 МиБ — байт в байт; время
15/130/38 мс против 15/126/38 мс (счётчик/KPI/список), то есть в пределах шума. Причина: `node_id`
не входит в `ORDER BY` (`date_create, date_request, method`) и не покрыт индексами пропуска, поэтому
гранулы им не отсекаются ни в одном варианте — оба сводятся к сравнению уже прочитанной колонки.
В отдельном прогоне с `node_id` первым в `ORDER BY` мягкий вариант читает больше (942 тыс. против
1.30 млн строк), но это стоимость самих дополнительных записей, а не оператора. Вывод: **убирать
`OR` ради скорости смысла не было**, решение принято ради корректности атрибуции.

### 4.63 §77 — логи: автоокно, tail-poll, автопоиск. Что неочевидно

**Задержка прокрутки не зависела от глубины скролла — и это ключ.** Замер на боевом узле
`gate/PDT_PDTExchange` (11.1 млн записей, внешняя таблица §64) дал одинаковые 870–980 мс на всех 12
страницах подряд. Значит дело не в курсоре и не в накоплении страниц, а в стоимости одного запроса:
без временно́го окна `ORDER BY date_request DESC LIMIT 50` заставляет ClickHouse прочитать широкие
колонки (`url`, `parameters`, `reason`) практически по всей таблице. Любое сужение — окно по дате
(75–185 мс) или **селективный** фильтр, включающий PREWHERE (113–141 мс на «Ошибках»), — даёт
6–12×. Неселективный фильтр не помогает: `method=` на половине таблицы — те же 938 мс.

**Почему автоокно живёт в usecase, а не в адаптере.** Это бизнес-правило «как читать историю», а не
деталь SQL: адаптер по-прежнему выполняет ровно один запрос с переданными границами, а решение
«окно узкое → расширить» принимает usecase. Плюс так оно тестируется без ClickHouse
(`logs_autowindow_test.go`, 7 unit-тестов на моке порта).

**Самая опасная часть — не скорость, а честность конца истории.** Фронт считает историю
исчерпанной по недобору страницы (`items.length < pageSize`, §72.2). Наивный цикл «сузил окно —
вернул что нашлось» врал бы «записей больше нет» каждому узлу, у которого свежих записей нет.
Отсюда два инварианта, закреплённых тестами: **последняя попытка всегда без нижней границы**, а
окно, ушедшее ниже `min(date_request)`, — последнее (перебор не продолжается). Integration-тест
`TestClickHouse_AutoWindow_E2E` специально сидит узел, чья история закончилась полгода назад: ни
одно окно 1ч..90д от «сейчас» его записей не видит, и первая страница обязана всё равно прийти
полной.

**`DateRange` кешируется, потому что дёргается на каждую страницу.** TTL 30 с; протухший `max`
лишь сдвигает якорь первой страницы на секунды назад, что автоокно само и покрывает. Пустая
таблица (`0,0`) и ошибка `DateRange` — сразу прямой запрос без перебора окон: на пустой таблице он
дёшев, а классификацией недоступности CH занимается обычный путь `Search`.

**Прунинг `date_create` §72.4 на внешние таблицы расширять не стали.** Гипотеза «внешней таблице
поможет измеренный запас вместо `partitionMarginDays`» проверена замером и отклонена: у `gate`
окно в сутки уже даёт 75 мс, потому что `date_request` вторым полем ключа сортировки отсекает
гранулы через minmax первичного ключа. Выигрыша нет, а риск потерять строки постороннего писателя
с `date_create ≠ день(date_request)` — реальный (ровно это показывает третий подтест
`TestClickHouse_DateCreatePruning_E2E`).

**Тела логов закрывались по двум независимым причинам, и чинить надо было обе.** Первая —
`refetchInterval` у `useInfiniteQuery` перезапрашивал ВСЕ страницы и пересоздавал список; заменён
на tail-poll («что новее самой свежей строки» → вставка сверху первой страницы с дедупом по `id`).
Вторая — `collapseToFirstPage` (§72.5) при возврате к верху выбрасывал страницы 2+ вместе с
раскрытой строкой; теперь схлопывание отложено, пока строка раскрыта (`collapseEnabled`). Починка
только первой оставила бы симптом живым для строк со второй страницы.

**Полный ответ tail-poll — не «много новых записей», а сигнал дыры.** Если за тик появилось больше
`limit` записей, между хвостом и первой страницей образуется провал; склейка молча потеряла бы
записи, поэтому такой случай честно инвалидирует первую страницу.

**Отмена поиска обязана доходить до ClickHouse.** `api.get` до §77.3 не принимал `AbortSignal`, и
react-query отменял запрос только логически — CH продолжал выполнять 28-секундный полнотекст.
Теперь signal доезжает до axios; разрыв соединения отменяет `c.Request.Context()` хендлера, и
clickhouse-go снимает запрос на сервере. Это же делает «сброс текущего поиска» при смене фильтра
настоящим, а не косметическим.

**Идемпотентный коммит фильтра — не микрооптимизация.** Без сравнения «черновик ≠ применённое»
Enter+blur давали два одинаковых запроса, а клик по «Сбросить» сначала запускал поиск по
черновику (blur), и только потом сбрасывал. Второе лечится ещё и `onMouseDown preventDefault` на
кнопках панели.

**«Завершено/В работе» — доказанный дубль, а не подозрение.** `count` по 7 комбинациям на 17
боевых узлах трёх команд: `err ≡ done=no`, `ok ≡ done=yes`, пересечения нулевые везде. Корень в
write-path: `send.go` пишет `Done = (2xx)`, поэтому предикат §72.1 `done=1 AND 200≤status<400`
вырождается в `done=1`, а «В работе»+«OK» была заведомо пустой комбинацией. Параметр API оставлен
(дип-линк §47 из «Очереди»), но активный фильтр теперь виден чипом — невидимый фильтр хуже лишнего
переключателя.

**Пресеты периода в UI рассматривались и отклонены.** При непрерывной прокрутке (автоокно едет за
скроллом) видимый пресет не менял бы ни содержимое списка, ни счётчик — только скорость страницы,
которую и так обеспечивает сервер. Переключатель без наблюдаемого эффекта путает; период как
фильтр остался в полях «Дата с/по».

Замер масштаба (`make test-int-logs-scale`): 50 млн строк — 7 мс средняя страница против 1153 мс
без автоокна; 1 млн — 7 мс против 86 мс. Смотреть надо на форму зависимости: с автоокном время
страницы от объёма таблицы не зависит, без него растёт линейно.

Файлы: [usecase/logs.go](../internal/web/usecase/logs.go) (`searchAutoWindow`, `dateRangeCached`),
[usecase/logs_autowindow_test.go](../internal/web/usecase/logs_autowindow_test.go),
[web-ui/src/components/node/LogsTab.tsx](../web-ui/src/components/node/LogsTab.tsx),
[web-ui/src/lib/useInfiniteLogs.ts](../web-ui/src/lib/useInfiniteLogs.ts),
[web-ui/src/api/client.ts](../web-ui/src/api/client.ts),
[LogsTab.autorefresh.test.tsx](../web-ui/src/components/node/LogsTab.autorefresh.test.tsx),
[tests/integration/log_autowindow_test.go](../tests/integration/log_autowindow_test.go).

### 4.64 §78.6 — «Показано 17 из 40». Единица счёта в логах

**Строка таблицы ≠ запись.** Одна запись лога живёт столькими строками, сколько раз запускался
ПРОГОН доставки: async-redelivery, DLQ-репроцессор §36, финальный `ttl_expired`, replay, дренаж
retry-топика §38 — каждый вызывает `SendUsecase.Send`, а тот пишет ровно одну строку с тем же `ID`.
Внутренние ретраи одного прогона (`retry_count` узла) строк не плодят — они схлопнуты в
`attempts`/`attempts_details`. Модель уже была зафиксирована в коде (`NodeKPI` — `countDistinct(ID)`,
`FailedIDs` — `DISTINCT ID`, `GetByID` — свежайшая строка), из ряда выпадали только `Count` и
`CountFailed`, и именно они стоят рядом со списком в UI.

**Почему баг пережил все гейты.** Ни один тест логов не сеял дубли `ID`: синтетический сид
интеграционных тестов — `concat('s-', toString(number))`, то есть строго уникальные. Фронтовый тест
«список и счётчик спрашивают один набор фильтров» сверяет query-параметры, а не числа. Поэтому
регрессионный тест §78.6 начинается с сеятеля дублей, а не с ассертов.

**Автоокно §77.2 к дефекту отношения не имело** — проверялось первым: недобор строк в окне никогда
не отдаётся наружу, последняя попытка всегда без нижней границы. Симптом «17 из 40 и скролл
кончился» объясняется целиком единицами: 40 строк < `pageSize` 50, значит сервер честно отдал всё,
что есть, а 17 — это те же записи после дедупа.

**`LIMIT 1 BY ID` + keyset дают повтор записи на стыке страниц — и это нормально.** Курсор строится
по свежайшей попытке последней записи страницы, поэтому её же старые попытки лежат НИЖЕ курсора и
попадут в следующую страницу. Клиентский дедуп по `id` в `useInfiniteLogs` их снимает — поэтому он
и оставлен (его исходный повод — включительная граница курсора — исчез ещё в §44/45-fix, когда
курсор стал строгим). Контракт конца истории §72.2 не нарушается: пока непрочитанные строки есть,
страница набирается полностью.

**Что осталось на строках сознательно:** `ListSince` (live-tail — повтор `ID` там означает новый
прогон доставки уже показанной записи, оператор должен его видеть) и `CountErrors` §20.3 (порог
Telegram-уведомлений; он ни к какому списку не приставлен, а перевод на записи молча изменил бы
смысл всех настроенных порогов).

**Стоимость.** `uniqExact(ID)` дороже `count()` — страховкой остаётся действующий `countTimeout`
10 с (§67) с мягкой деградацией до «Показано N» без «из M». План «Б», если на боевых объёмах пойдут
систематические таймауты, — переиспользовать существующий флаг `metrics_approx_counts` §44
(`uniq` вместо `uniqExact`, HLL, ошибка ~0.3 %), как это уже сделано в `NodeKPI`. В §78 флаг
намеренно НЕ вводится.

Файлы: [log_reader.go](../internal/web/adapter/out/clickhouse/log_reader.go) (`Search`, `Count`,
`CountFailed`, комментарии `ListSince`/`CountErrors`/`DeleteFailed`),
[usecase/logs.go](../internal/web/usecase/logs.go) (комментарий автоокна),
[tests/integration/log_dedup_count_test.go](../tests/integration/log_dedup_count_test.go).

### 4.65 §78.1–78.5 — короткий адрес узла. Что неочевидно

**Почему один catch-all, а не отдельный маршрут.** `/api/v1/*path` рядом с существующим
`/api/v1/request/*path` — паника gin ПРИ СТАРТЕ, а не 404 в рантайме:
`catch-all wildcard '*path' in new path '/api/v1/*path' conflicts with existing path segment
'request' in existing prefix '/api/v1/request'` (проверено на v1.12.0). Отсюда всё остальное
устройство: три маршрута схлопнуты в один, первый сегмент разбирает `SplitVerb`.

**Почему не `NoRoute`.** Туда не доходят middleware группы `/api/v1`. Два последствия, каждое
тихое: rate-limit (`receiver.rate_limit_per_node`) не применялся бы к короткой форме вовсе, а
`metrics.GinMiddleware` выходит досрочно при пустом `c.FullPath()` — боевой трафик по новым
адресам просто не появился бы в `nexus_requests_total`, и это заметили бы не сразу.

**Метка `method` — самое хрупкое место.** Она выводилась из имени маршрута, а маршрут теперь один
на все формы адреса. Значения обязаны остаться прежними: на `method="requestAsync"` стоит
переменная и два графика дашборда Kafka (`deploy/grafana/nexus-kafka.json`), на `method="request"` —
алерт латентности (`deploy/prometheus.alerts.yml`). Поэтому метку кладёт handler в контекст
(`metrics.RootMethodLabelKey`), и кладёт её ВЫЗЫВАЮЩИЙ, а не общий `handleAsyncFromInput`: иначе
sync-узел на паузе, уходя в async-ветку (§3.6), сменил бы ряд с `request` на `requestAsync`.

**Ключ rate-limit.** С catch-all в `c.Param("path")` приходит путь ВМЕСТЕ с сегментом метода.
Не срезав его тем же `SplitVerb`, получили бы две корзины на один узел (`request/webhook/sbp-qr` и
`webhook/sbp-qr`) — клиент удваивает квоту сменой формы адреса, — и разом сменившиеся ключи Redis
у всех узлов при выкате.

**Первый сегмент неоднозначен by design.** Резолвер §18 сначала читает его как слог команды, потом
как часть пути в `default` — короткая форма ничего тут не меняет, но означает, что узел
`default`-команды с путём `webhook/sbp-qr` и узел `sbp-qr` команды `webhook` по короткому адресу
неразличимы. Однозначность даёт полная форма со слогом; в UI это учтено обратной стороной: у
`default` слог опускается, КРОМЕ путей, чей первый сегмент — `request`/`requestAsync`/`callback`
(иначе показанный адрес прочитался бы как legacy-форма).

**Резерв слогов — только на создании.** `Team.Validate()` вызывается и при переименовании, поэтому
запрет внутри него сломал бы правку уже существующей команды с таким слагом. Проверка живёт в
`TeamUsecase.Create`; регрессия закреплена тестом `TestTeamValidate_AllowsReservedSlug`.

**Защита от самоссылки §32.2 могла ослабнуть молча.** Признак «путь ведёт во вход шины» проверял
префикс `/api/v1/request`; узел с `target_url` на собственный короткий адрес под него не подпадал.
Расширено до всего `/api/v1/` (и legacy `/v1/`) — под этим префиксом у шины нет ничего, кроме
боевого входа.

**Sentry-middleware ловится тем же ножом, что и метрики** — и его легко пропустить. Он тоже
выводил теги `node`/`root_method` из `c.FullPath()`, а сырой `c.Param("path")` теперь содержит
сегмент метода. Без правки теги транзакций разъехались бы: `root_method` исчез бы вовсе, а `node`
у legacy-трафика стал бы `request/webhook/sbp-qr` вместо `webhook/sbp-qr`. Теги переехали на тот же
источник, что и метки Prometheus, и ставятся **после** `c.Next()` — до него контекст ещё пуст
(span финиширует в `defer`, так что поздние теги в него попадают).

**Тестовая грабля:** прокси-маршруты Web нельзя проверять через `httptest.ResponseRecorder` —
`httputil.ReverseProxy` с `FlushInterval > 0` требует `CloseNotifier`, которого у рекордера нет
(паника `interface conversion`). Тест поднимает настоящий `httptest.NewServer` поверх gin-движка.

Файлы: [handler.go](../internal/receiver/adapter/in/http/handler.go) (`Register`, `SplitVerb`,
`handleIngress`, `handleAuto`), [middleware.go](../internal/receiver/adapter/in/http/middleware.go),
[route.go](../internal/receiver/usecase/route.go) (`NodeRootMethod`),
[metrics/gin.go](../internal/platform/metrics/gin.go),
[receiver_proxy.go](../internal/web/adapter/in/http/receiver_proxy.go),
[team.go](../internal/web/usecase/team.go), [domain/team.go](../internal/domain/team.go),
[node_selfref.go](../internal/web/usecase/node_selfref.go),
[nodeUrl.ts](../web-ui/src/lib/nodeUrl.ts), [ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx).

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
make test-int-logs-scale LOG_SCALE_ROWS=50000000  # §77.5: замер прокрутки логов (вручную)
make loadtest TARGET_RPS=500 DURATION=10m NODES=50 ADMIN_PASSWORD=…

# Миграции
make migrate-up
make migrate-down N=1
make migrate-status
make migrate-force V=28                        # §74.4: объявить версию и снять dirty (SQL НЕ выполняет)

# Образы приложения и откат (§74.5) — на сервере скрипт зовут напрямую
make images-tag V=1.21.1                       # сохранить текущие nexus-*:latest под версией
make images-list                               # какие версии сохранены на хосте
make images-rollback V=1.21.1                  # вернуть :latest без пересборки
python scripts/release/rollback_info.py v1.20.2 v1.21.0   # §74.6: строка «Откат» для CHANGELOG
cp scripts/release/release_notes_template.md desc_1.0.0.md # §9.5-E: заготовка описания GitLab-релиза
python scripts/release/create_release.py v1.0.0 v1.0.0 desc_1.0.0.md
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

**Скриншоты README (`docs/img/`).** Галерея интерфейса в [README.md](../README.md) снимается на
живом стенде (docs/STAND_TESTING.md, Вариант B) браузером через Playwright MCP: вьюпорт 1440×900,
тёмная тема, имена файлов совпадают с экранами (`overview.png`, `node-logs.png`, `kafka.png`, …).
Правила при пересъёмке: (1) стенд должен быть с трафиком — пустые графики и «—» в KPI выглядят как
неработающая фича, поэтому перед съёмкой гоняется `scripts/stand/seed_and_test.ps1` и «размазанный»
по минутам трафик (иначе на графике один столбец); (2) **из кадра убираются реальные данные** —
адреса боевых инстансов и e-mail пользователей стенда подменяются в DOM (`browser_evaluate`) на
`nexus.example.com` / `user@example.com` перед `browser_take_screenshot`, снимок правится не в БД;
(3) маска `.gitignore` `/*.png` действует только на корень репозитория, `docs/img/*.png`
отслеживается штатно.

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
      - security-заголовки `SecurityHeaders()`: CSP (только self +
        'unsafe-inline' для style; с §89.1 внешних источников нет вовсе —
        шрифты локальные), nosniff, X-Frame-Options DENY,
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

0. **Горизонтальное масштабирование Receiver/Web (нулевой простой при обновлении)** — вынесено
   из §74 отдельным ТЗ. Сейчас у каждого сервиса один экземпляр, и `docker compose up` даёт
   короткий перерыв даже на плановом обновлении, а падение экземпляра — полный простой. Что уже
   готово: сервисы stateless, миграции под advisory-lock (одновременный старт безопасен), сессии
   в Redis. Что нужно продумать: балансировщик и health-gate перед переключением трафика,
   поведение singleflight/L2-кеша Receiver'а на нескольких экземплярах, одиночные фоновые
   процессы (sweeper'ы DLQ/paused, housekeeping ClickHouse, нотификатор — сейчас их лидерство
   держится Redis-локом только у части), метрики с меткой экземпляра.
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
- 7.5 Grafana dashboards + Prometheus alert rules:
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
- 7.6 Per-node мониторинг + фикс дашборда (расширение 7.5):
  · **Фикс `nexus.json`.** Панели «Go runtime: heap/goroutines» фильтровались
  по `service=~"$service"`, но у `go_*`/`process_*` метрик нет const-label
  `service` (он навешивается только на `nexus_*` в
  [metrics.go](../internal/platform/metrics/metrics.go), `ConstLabels`) — панели
  показывали «No Data». Заменено на `job=~"nexus-.*"` (label `job` из scrape-конфига).
  Верифицировано на TSDB стенда: на одном timestamp новый expr = 24 серии,
  старый = 0. Панель latency p50/p95/p99 переведена на разрез `sum by (service, le)`.
  · **Новый `deploy/grafana/nexus-nodes.json`** (uid `nexus-nodes`, 13 панелей):
  счётчики Nodes DOWN/DEGRADED/OK + таблица «Problem nodes» (мгновенно видно
  проблемные узлы по `nexus_node_last_request_error` 0/1/2), сводная таблица всех
  узлов за период (requests/errors/error%/p95/status, join через `joinByField` по
  `node`, подсветка порогами), per-node timeseries (RPS, ошибки top-15, p95),
  state-timeline истории статуса, секция RabbitMQAsync pull-узлов (conn state,
  queue depth, pulled by status, degraded). Исходы доставки берутся со стороны
  **Sender** (`service="sender"`). Переменная `node`.
  · **Новый `deploy/grafana/nexus-kafka.json`** (uid `nexus-kafka`, 9 панелей):
  consumer lag по group/topic/partition (порог 10k), in-flight, produce p95 by
  topic, конвейер produced→delivered→failed (async восстанавливается из
  `nexus_requests_total{method="requestAsync"}`: produced=receiver,
  delivered=sender), ошибки async по узлам, cancelled, DLQ reprocess outcomes +
  p95, loop detections. CH-панели намеренно не дублируются (живут в Overview).
  · **Новая группа алертов `nexus.nodes`** (8 правил) в `prometheus.alerts.yml`:
  NexusNodeDown/Degraded/HighErrorRate (per-node по sender-метрикам),
  RMQNodeDisconnected/PullNodeDegraded/RMQQueueBacklog (pull-узлы),
  DLQReprocessFailing/MessagesLost. `promtool check rules` → SUCCESS 17 rules.
  Все per-node метрики уже существовали — Go-код не менялся, только deploy-артефакты.
- 7.4 CI pipeline в [.gitlab-ci.yml](../.gitlab-ci.yml) — параллельные jobs
  `go-test` (race -short), `go-build` (`go build ./...`), `go-lint`
  (`golangci-lint v2.12`), `swagger-drift` (regen `swag init` → `git diff`),
  `ui-build` (Node 20 + `npm ci` + `npm run lint --if-present` + `vite build`),
  `integration` (testcontainers, по MR-label `run-integration` или master/dev/tag).
  `.golangci.yml` с набором bodyclose/rowserrcheck/errcheck/govet/revive/staticcheck/
  **modernize**. Авто-апдейты зависимостей — [renovate.json](../renovate.json) (weekly
  schedule), группировка minor/patch в один MR.
  · **`modernize`** (анализаторы x/tools gopls) включён как гейт: `slices.Backward/
  Contains/Sort`, `maps.Collect`, `min/max`, `atomic.Int32` вместо
  `atomic.AddInt32(&int32)`, range-over-int, `fmt.Appendf`, `strings.CutPrefix`,
  `testing.Context`, `wg.Go`. Настройками поддерживается **только**
  `settings.modernize.disable` — ключа `enable` в JSON-схеме нет, по умолчанию
  включены все анализаторы. Исключён на `docs/` и `proto/` (генерируемый код) и на
  `web-ui/node_modules` (сторонняя заглушка `flatted.go` — единственная находка вне
  нашего кода). Job `go-lint` дополнительно гоняет `golangci-lint config verify`
  (валидация конфига по схеме именно того образа, что в CI) и явную проверку, что
  `modernize` присутствует в разделе «Enabled» вывода `golangci-lint linters`.
  Проверка нужна потому, что `run` с выпавшим линтером всё равно печатает
  «0 issues» — тихую деградацию иначе не заметить; грепать надо строго по разделу
  Enabled (в выводе есть и раздел Disabled с тем же именем).
- 7.3 Integration suite: Redis + ClickHouse через testcontainers.
  Generic-контейнер (`testcontainers.GenericContainer`) — без отдельных
  модулей `modules/redis`/`modules/clickhouse`. CH: native-handshake
  готовится позже `ForListeningPort`, поэтому в helper'е активный retry-ping
  до 60 сек. Покрытие: SessionRepoRedis (CRUD + DeleteByUser + TTL expire),
  NodeCacheRedis (Set/GetByPath/Invalidate), chlog.Writer → реальная
  MergeTree-таблица → LogReaderCH (`GetByID`, `Search` с фильтрами
  status/IP/Done/full-text, защита от SQL-инъекции в имени таблицы).
  Весь интеграционный набор (PG + Kafka + Redis + CH) проходит за ~200s.

### 4.35 §55 — dry-run с реальным вызовом: что здесь неочевидно

- **`logging_enabled=false` НЕ достаточно, чтобы «не оставить следов».** Это главная ловушка раздела.
  Обычный `Send` помимо ClickHouse трогает: метрики §6 (`requests_total`/`duration`/`incomplete`),
  gauge исхода §41/§52, персист статуса узла в Redis §46 и **circuit breaker §9.5/§50.4**. Погасив
  только логи, получаешь инструмент, который при диагностике зависшего узла **красит его в Down на
  дашборде и открывает breaker → 503 боевому трафику**. Отсюда отдельный флаг `dry_run` в
  [sender.proto](../proto/sender/v1/sender.proto), а не переиспользование существующего.
  Проверено на боевых данных: `vika_task` за сутки — 108 ошибок из 297, из них **58 уже
  `circuit_breaker_open`**; пара dry-run без гейта добила бы breaker и зарезала бы те 189 запросов,
  что ещё проходили.
- **`__dryrun_<path>` — не паранойя, а страховка.** Гейты живут в трёх местах (usecase, gRPC-адаптер,
  writer). Если будущая правка добавит четвёртое и забудет гейт, изолированный `node_path` уведёт
  метрику/breaker/Redis-ключ в чужое имя, а не на боевой узел. Дёшево и спасает от целого класса
  регрессий.
- **Порядок деплоя Sender → Web.** Старый Sender новое поле `dry_run` проигнорирует, и побочка не
  погасится. Web без Sender'а безопасен (реальный режим = `skipped`), Sender без Web — тем более.
  Зафиксировано в [../DEPLOYMENT.md](../DEPLOYMENT.md).
- **Креды сохранённого узла в браузере отсутствуют — сервер обязан их подмешать (§55.6).** Легко
  упустить: `GET /api/nodes/{id}` выглядит как «полный конфиг», но `auth_credentials` там нет
  никогда (только `*_credentials_set: bool`). Тест такого узла без подмешивания уходит с пустым
  `Bearer ` → target отвечает 401 → оператор чинит несуществующую поломку авторизации. Конвенция уже
  была в проекте (§5.5, `PUT /api/nodes/{id}`) — переиспользована, а не изобретена заново.
- **Подмешать креды в конфиг мало — надо подставить ПРЕДЪЯВЛЯЕМУЮ входящую креду (§55.6).** Грабли,
  вскрытые на стенде: узел с `incoming_auth_type=basic` и заведёнными кредами всё равно падал на
  `auth.incoming: authorization header missing`. `mergeStoredCreds` кладёт `incoming_auth_credentials`
  лишь в **эталон** сравнения (`CheckIncomingAuth` сверяет с ним то, что клиент **предъявил**), а
  предъявляемую сторону берёт из синтетического запроса оператора — которую тот собрать не может
  (`Basic base64(...)`/HMAC из скрытых кредов). Фикс симметричен `auth.outgoing`: новый
  `BuildIncomingAuthValue` в [receiver/usecase/auth.go](../internal/receiver/usecase/auth.go) —
  **обратная операция** к `checkIncomingBasic`/`checkIncomingToken`/`VerifyWebhookSignature` (та же
  схема/кодирование, поэтому тест не разойдётся с боем; покрыто round-trip тестом
  [incoming_auth_build_test.go](../internal/receiver/usecase/incoming_auth_build_test.go)), а
  [dry_run.go](../internal/web/usecase/dry_run.go) `autofillIncomingAuth` кладёт значение в
  header/query по `incoming_auth_dynamic_source`/`field`. **Ручной ввод побеждает** (`IncomingAuthPresented`
  ≠ "" → не трогаем) — негатив §55.8 остаётся проверяемым. Секрет **маскируется в отчёте**
  (`headers.forwarded` — отдельная копия для отчёта, реальный `fwd` уходит в Sender; `url.resolve`/
  `clickhouse.would_log` — `displayURL` с замаскированным query-параметром), чтобы сохранённая креда
  не утекла в браузер. Метод формы теперь по умолчанию = `incoming_method` узла (`defaultDryRunMethod`
  в [dryRunHint.ts](../web-ui/src/lib/dryRunHint.ts)) — узел с `incoming_method=GET` иначе падал на
  `method.incoming` при дефолтном POST.
- **Mock живёт в Web, а не в Sender** — сознательное отступление от буквы §7.5.1 («Sender вызывает
  встроенный mock»): ради синтетики gRPC-хоп не нужен, и зависимость Web→Sender остаётся
  **опциональной** (`web.sender_grpc.addr` пуст → mock работает как прежде). Не «упрощение» —
  условие того, что dev-стенд без Sender'а не ломается.
- **§55.9: тест обязан повторять бой, иначе он врёт.** Пока ответ был синтетическим mock-200,
  расхождения с pipeline никого не жгли, и накопились три: не проверялся входящий метод
  (`incoming_method=GET` «принимал» POST), не вычислялся исходящий (узел с `outgoing_method=PUT`
  тестировался POST'ом), не клеился хвост §39 (**passthrough-узел тестировался по базовому адресу**).
  Последнее особенно коварно: боевой `vika_task` — как раз `path_passthrough=true`, и падают у него
  хвосты `vika.openLink`/`vika.getScriptCall`, а базовый `/task2/hs/vika` мог бы отвечать нормально →
  dry-run показал бы «всё хорошо» при лежащем узле. Лечится не копией логики, а **экспортом**
  `MethodMatches`/`EffectiveOutgoingMethod`/`AppendPathSuffix` из `receiver/usecase`: одна реализация
  на бой и на тест — расхождение не сможет вернуться незаметно.
- **`AppendPathSuffix` через `url.JoinPath` режет `../`** — хвостом нельзя выйти за пределы
  `target_url` и обойти allowlist §23. Покрыто тестом (`TestDryRun_RealCall_PathTailCannotEscapeTarget`),
  потому что это единственное, что отделяет поле ввода от SSRF-примитива.
- **Отчёт маскирует `Authorization`, но тело ответа — это ответ ВНЕШНЕЙ системы.** Заголовки эха
  маскируются (`maskAuthMap`), однако target, который эхом возвращает запрос (как `echosrv`), покажет
  кред в теле. Замаскировать произвольное тело нельзя — неизвестно, что там секрет. Это внутри уже
  принятого риска §55.5/§55.6: manager+ и так может направить узел на свой сервер и снять
  `Authorization` с боевого трафика; аудит фиксирует. Важно не считать, что «отчёт безопасен by
  design» — он безопасен ровно в объёме своих собственных шагов.
- **Тест на breaker должен содержать контроль.** `TestDryRun_DoesNotOpenBreaker` после проверки
  «breaker закрыт» гоняет ту же серию **без** `dry_run` и требует, чтобы breaker открылся. Без этой
  половины тест был бы зелёным и в случае «breaker вообще не работает» — то есть не проверял бы
  ничего.

### 4.36 §58 — поделиться узлом: почему отдельный резолвер и как уживается с §4.34

- **Команду узла нельзя узнать через `Get` — он team-scoped.** Чтобы шаренная ссылка на узел из
  другой команды открылась, фронт должен переключить сессию на команду узла, но `GET /api/nodes/:id`
  при несовпадении команды отдаёт 404 (§4.34, no-leak) — команды узла из него не вытащить. Отсюда
  отдельный `GET /api/nodes/:id/team` (`ResolveTeam`), который резолвит команду **по членству
  пользователя**, а не по текущей команде сессии. Единый 404 на «узла нет» и «не член команды узла» —
  та же анти-утечка, что у `Get`; фронт показывает «Узел не доступен».
- **Почему не ослабить `Get` до кросс-командного.** Детальная страница узла тянет team-scoped данные
  (логи, метрики, CH-БД per team) — они всё равно требуют совпадения текущей команды. Дешевле и
  честнее переключить сессию, чем городить кросс-командное чтение в каждом эндпоинте.
- **Переключение РОВНО ОДИН РАЗ + залипание `ready` — чтобы не воевать с §4.34.** §4.34 намеренно
  сохранил редирект-на-404 (просмотр) и баннер `foreign_team` (форма) для случая «сменил команду в
  шапке, НЕ уходя со страницы». Если бы `useEnsureNodeTeam` реагировал на любое несовпадение, он
  откатывал бы ручное переключение пользователя назад. Поэтому авто-switch срабатывает один раз на
  **пару (узел, его команда)** (guard по `${id}:${target}`), а достигнув `ready`, статус залипает
  (`latchedKey` по той же паре): последующая ручная смена команды уходит по старому пути §4.34
  (детальная 404→редирект; форма — баннер с сохранением правок). Узел грузится только при `ready` —
  иначе team-scoped `GET` отдал бы 404 ещё до переключения. Почему пара, а не один `id` — §4.51.
- **Replay был дырой RBAC.** Аудит кнопок узла (§58.5) нашёл, что `POST /logs/:id/replay` жил в
  группе `authed` (не `authedManager`), а комментарий рядом уже заявлял «mutating, session-cookie» —
  код не соответствовал собственному намерению. Replay пере-отправляет запрос на внешнюю цель, то
  есть сайд-эффект; viewer не должен его инициировать (ТЗ §7.4.1). Перенос в `authedManager` +
  `RequireSessionOnly` закрыл и роль (viewer→403), и канал (API-токены реплеить не могут). Кнопки на
  фронте (LogsTab и таблица неудач QueueTab) для viewer теперь disabled, а не скрыты — чтобы было
  видно, что действие существует, но требует прав.

### 4.37 Async-consumer: почему HandleRetry обрабатывается на месте (потеря paused-сообщений)

- **kafka-go НЕ передоставляет незакоммиченное сообщение в живой сессии.** `Reader.FetchMessage`
  двигает внутренний курсор; отсутствие commit'а влияет только на *committed offset* в Kafka, но не
  возвращает сообщение этому же reader'у — оно вернётся лишь при rebalance/рестарте. Это уже было
  известно в репозитории (см. комментарий в [clog_retry_consumer.go](../internal/sender/adapter/in/kafka/clog_retry_consumer.go)
  — именно поэтому CH-retry сделан retry-in-place), но основной async-consumer жил по неверному
  комментарию «сообщение будет прочитано снова при следующем FetchMessage».
- **Последствие — не задержка, а ПОТЕРЯ.** Старый код на `HandleRetry` (paused-узел, сбой чтения узла
  из PG, не записавшийся DLQ) шёл к следующему сообщению. Если следующее сообщение той же партиции
  получало Ack, `CommitMessages` коммитил offset «включительно» — committed offset прокатывался мимо
  незакоммиченного сообщения, и после ребаланса оно исчезало навсегда. Партиционирование по
  `kafka.Hash(node.Path)` не спасает: разные узлы регулярно попадают в одну партицию.
- **Первый фикс — `processWithRetry`:** то же сообщение переобрабатывается до терминального исхода
  (Ack/DLQed/Requeued), только после этого commit; при отмене ctx (shutdown) — выход без commit'а.
  Это закрыло потерю, но ценой head-of-line blocking. Механизм остался в коде и работает для
  транзиентных Retry (недоступен PostgreSQL при чтении узла, не записался DLQ).
- **Второй фикс (§3.6, delay-топик) — изоляция узлов.** Head-of-line оказался неприемлем: партиций
  4, узлов десятки, значит каждая партиция обслуживает примерно четверть всех узлов, и пауза одного
  узла тормозила бы соседей. Теперь paused-сообщение переносится в `nexus.async.paused` и offset
  основного топика коммитится — партиция свободна. См. 4.38.
- **Паузы двухуровневые.** Выдержку для paused даёт usecase (`pausedRetryAfter`, дефолт 30s);
  `asyncRetryPause` (1s) в адаптере — только защита от busy-loop для прочих Retry-веток. Тестам
  30s не подходят → функциональная опция `WithPausedRetryAfter` у `NewAsyncProcessor`.
- **Попутно найден второй баг: `ConsumerGroup.Stop()` не завершал горутины.** У него, в отличие от
  `ChLogRetryConsumer`, не было собственного производного контекста. После `Close()` reader'а
  `FetchMessage` возвращает не `context.Canceled`, а «reader closed» → цикл уходил в `continue` и
  крутил busy-loop до отмены ВНЕШНЕГО ctx. В проде маскировалось тем, что при shutdown внешний ctx
  обычно уже отменён; в тестах проявилось как ровно-240-секундные прогоны. Исправлено по образцу
  `ChLogRetryConsumer`: `cancel` в структуре, `Start` создаёт производный ctx, `Stop` его отменяет.
- **Грабли тестирования этого места.** Первая версия integration-тестов ЗЕЛЕНЕЛА на сломанном коде:
  между `Start` и первым `FetchMessage` проходит join+sync group (секунды), поэтому «сообщение не
  доставлено» означало не блокировку очереди, а то, что consumer ещё не читал. Лечится warmup-фазой
  (`warmupConsumer`): сначала доставляем сообщение enabled-узла и ждём его хита — это доказывает, что
  consumer активен, и только потом проверяем отсутствие доставки. Любой новый тест этого контура
  обязан начинаться с warmup.

### 4.38 §3.6 delay-топик `nexus.async.paused`: изоляция paused-узлов

- **Зачем.** После 4.37 сообщение paused-узла держало голову партиции. Партиций 4, узлов десятки —
  значит каждая партиция обслуживает ~четверть всех узлов, и пауза одного узла тормозила соседей.
  Это не «редкая коллизия хешей», а арифметика: ключ партиционирования — `node_path`, число партиций
  конечно. Теперь основной consumer переносит такое сообщение в `nexus.async.paused` и коммитит
  offset основного топика ([async.go](../internal/sender/usecase/async.go) `handlePaused`,
  результат `HandleRequeued`), а бэклог обслуживает
  [paused_sweeper.go](../internal/sender/adapter/in/kafka/paused_sweeper.go).
- **Почему sweeper проходами, а не обычный ConsumerGroup с паузой после сообщения.** Разобрано и
  отвергнуто на этапе плана: пауза после каждого переноса усыпляет горутину партиции, и узел,
  снявший паузу, ждёт за бэклогом долго-paused соседа (10k сообщений × 1 с ≈ 3 часа) — тот же
  head-of-line, просто переехавший в delay-топик. Пауза должна быть МЕЖДУ проходами: внутри прохода
  работаем на полной скорости, `interval_sec` (30 с) ограничивает темп циркуляции и задаёт
  максимальную задержку доставки после `unpause`.
- **Инварианты прохода скопированы из DLQ-репроцессора (§36):** wrap-detect `seen[id]` (иначе бэклог
  крутится внутри одного прохода бесконечно) и **commit строго ДО break** (иначе коммит последующих
  прокатывает offset мимо незакоммиченного — та же потеря, что в 4.37).
- **`paused_since` — метка первого откладывания**, не затирается на кругах циркуляции: по ней виден
  возраст бэклога (нужно для баннера §3.6 «узел в паузе X дней»). `last_requeue_at` обновляется.
- **Известные ограничения (приняты осознанно):**
  - *Дубли.* Копия в delay-топик публикуется ДО коммита исходного сообщения; падение между шагами
    даёт вторую копию. Контракт остаётся at-least-once (тот же класс, что у DLQ-репроцессора);
    устранимо только Kafka-транзакциями — вне scope.
  - *Порядок.* На границе `unpause` строгий FIFO рвётся: прочитанное до снятия паузы уезжает в хвост
    и доставляется позже прочитанного после. Обещание «порядок сохраняется» из §3.6 п.4 снято.
  - *Выключенный sweeper* = бэклог не доставится вообще и протухнет по retention. Наблюдаемость —
    `nexus_kafka_lag` по группе `<group>-paused` (публикуется из `reportKafkaLag`).
- **§35 обязан знать оба топика.** Иначе у paused-узла KPI «Ожидают отправки» показывает 0, а
  «Очистить все ожидающие» не находит сообщений — отваливается главный юзкейс §34.4. `List`/`ScanIDs`
  ходят по обоим источникам ([async_queue.go](../internal/web/usecase/async_queue.go) `sources()`),
  **с дедупом по id**: циркулирующая копия встречается под разными offset'ами. `Body` принимает
  topic с allow-list-проверкой (эндпоинт не должен стать универсальным ридером Kafka) — и по
  delay-топику это best-effort: координата устаревает при переносе в хвост.
- **Имя группы sweeper'а — общая константа** `config.PausedGroupSuffix` + `KafkaSection.PausedGroup()`:
  её одинаково вычисляют Sender (читает топик) и Web (§35 считает peek от committed offset этой
  группы). Разъедутся — вкладка «Очередь» покажет неверный бэклог.
- **Грабли тестов sweeper'а.** `Run()` завершается по отмене контекста, а `Stop()` лишь закрывает
  reader (так же устроен DLQ-репроцессор; в проде ctx отменяет runner при shutdown). Тестовый хелпер,
  ждавший `done` после одного `Stop()`, вешал прогон до таймаута — нужен собственный производный ctx.

### 4.39 Сквозные таймауты sync-запроса: кто реально может оборвать 600-секундный вызов

Разобрано при фиксе боевого бага «узел с timeout_ms=300000 рвётся на 30с» (Sentry NEXUS-8).
Полный путь: клиент → Web-прокси → Receiver → gRPC → Sender → внешний узел. Обрывать могут:

- **`http.Client` Sender'а** — БЫЛ главный виновник: `http.Client.Timeout` из
  `sender.http_client.timeout_ms` (30000) молча капал per-node `timeout_ms` (срабатывает меньший из
  Timeout и context-дедлайна). Исправлено: у клиента больше нет глобального `Timeout`, конфиг стал
  fallback'ом для запросов без per-node значения
  ([httpclient/client.go](../internal/sender/adapter/out/httpclient/client.go), регресс-тест
  `TestClient_Do_PerRequestTimeoutExceedsConfig`).
- **`receiver.write_timeout_ms`** — второй виновник (боевое значение было 10000): net/http
  WriteTimeout отсчитывается от чтения заголовков и включает всё время handler'а; дедлайн истекал
  на 10-й секунде, handler дописывал ответ на 30-й → write fail → conn closed → ReverseProxy Web
  ловил `EOF` → клиент получал 502 (это и есть Sentry NEXUS-8: события совпадали с CH-логом узла
  секунда в секунду со сдвигом +30с). Дефолт поднят до 610000 (600с макс. узла + 10с шина).
- **Web replay-dispatcher** — был хардкод 30с, теперь константа `replayDispatchTimeout = 610s`
  ([web/app.go](../internal/web/app.go)).
- **gRPC Receiver→Sender ОБРЫВАЕТ — но не таймаутом, а разрывом транспорта (§82.1).** Пункт ниже
  про «мёртвые параметры» верен и остаётся в силе, но раньше из него делали вывод «внутри Nexus
  дедлайна на этом участке нет ни одного» — и этот вывод НЕВЕРЕН. Дефолтная keepalive-политика
  gRPC-сервера Sender'а считала штатные ping'и клиента нарушением и на третьем закрывала соединение
  целиком (`GOAWAY ENHANCE_YOUR_CALM / "too_many_pings"`), давая потолок sync-вызова
  `4 × keepalive_time_sec` — 121 с напрямую в Receiver и 91 с через Web при `timeout_ms=600000`
  узла. Исправлено в §82.1 (`ServerKeepalivePolicy` +
  `sender.grpc_keepalive`); регресс — `TestSender_GRPCKeepalive_LongUnaryCallSurvives`
  ([tests/integration/sender_grpc_keepalive_test.go](../tests/integration/sender_grpc_keepalive_test.go)).
  **Мораль для следующего разбора:** «таймаута нет» и «оборвать не может» — разные утверждения;
  ищи не только дедлайны, но и всё, что закрывает соединение.
- **gRPC-таймауты из конфига НЕ обрывают**: `receiver.sender_grpc.timeout_ms` и
  `web.sender_grpc.timeout_ms` — **мёртвые параметры**, `grpcsender.New/Send`
  ([platform/grpcsender/client.go](../internal/platform/grpcsender/client.go)) их не читает и
  deadline не ставит — вызов наследует контекст входящего HTTP-запроса. Не удалены, чтобы не менять
  формат конфига; знай, что менять их значения бесполезно. Это **написано прямо в конфигах**
  (`config.example.yml`, §8 ТЗ) и в godoc `ReceiverSenderGRPCConfig.TimeoutMs` — раньше комментарий
  обещал «Таймаут одного gRPC-вызова» и провоцировал «чинить» им боевые обрывы. Страховка от
  наивной «починки» — `TestGRPCSender_ConfigTimeoutDoesNotCapCall`
  ([client_timeout_test.go](../internal/platform/grpcsender/client_timeout_test.go)): применить
  параметр как дедлайн = обрезать узлы с `timeout_ms=600000` на 30-й секунде, т.е. вернуть NEXUS-8.
- **`context canceled` в логе ≠ таймаут узла — это ЧУЖОЙ дедлайн.** Разобрано на боевом инциденте
  30.07.2026 (узел `task_vika`, `timeout_ms=600000`, обрыв на 49999 мс). Различай по тексту reason:
  - `context deadline exceeded (Client.Timeout exceeded ...)` — сработал НАШ таймаут (per-node
    `timeout_ms`, а до фикса NEXUS-8 — `http.Client.Timeout`);
  - `context canceled` — родительский контекст отменён СНАРУЖИ: вызывающая система закрыла
    HTTP-соединение → gin отменил `c.Request.Context()` → gRPC-вызов CANCELED → `httpclient.Do`
    бросил исходящий запрос. Ни один таймаут Nexus здесь не участвует.

    Диагностический признак: **одинаковая длительность** у серии обрывов (в инциденте 18 записей
    уложились в 49993–50000 мс) — это дедлайн клиента или прокси перед Receiver, искать надо вне
    Nexus. Разброс длительностей означал бы обратное. Тестами контракт закреплён в
    `TestGRPCSender_CallerContextCancelsRPC`.
- **Ретраи не продолжаются после смерти контекста** ([send.go](../internal/sender/usecase/send.go),
  цикл попыток): при отменённом `ctx` цикл выходит с ошибкой той попытки, на которой контекст умер —
  она и есть настоящая причина. Иначе узел с `retry_count>0` домалывал все попытки, каждая падала
  мгновенно тем же `context canceled`, раздувая `attempts` в CH и вытесняя первопричину из `reason`.
  Проверка стоит в двух местах, потому что контекст может умереть в двух разных фазах:
  - **во время попытки** — `ctx.Err()` после `httpc.Do` (тест `TestSend_ContextCanceled_StopsRetrying`,
    красный на старом коде: 4 попытки вместо 1);
  - **во время паузы** — backoff ждёт через `sleepCtx` (таймер + `ctx.Done()`), а не `time.Sleep`:
    иначе отмена в начале паузы всё равно оплачивалась бы полным сном (jitter — до
    `retry_backoff_ms × 2^(n-1)`) и лишней заведомо провальной попыткой. Тест `TestSleepCtx`.

  Для узлов с `retry_count=0` (как `task_vika`) поведение не менялось — попытка всего одна.
- **Kafka-ребаланс при async 600с НЕ грозит**: `kafka.consumer.max_poll_interval_ms` — декларативный,
  segmentio/kafka-go его не применяет (heartbeat consumer-group идёт в фоновой горутине
  generation-loop независимо от обработки сообщения; ребаланс — только по `session_timeout_ms` при
  смерти процесса).
- **Прод-чек-лист** при поднятии таймаутов узлов: `receiver.write_timeout_ms` ≥ 610000 в боевом
  config.yml (см. DEPLOYMENT.md), гистограмма `nexus_request_duration_seconds` имеет бакет 600.

### 4.40 §62 — глобальный поиск узлов + история: неочевидности

Разобрано при реализации §62 (кросс-командный поиск в шапке + персональная история поиска).

- **Почему `/api/search/nodes`, а не `/api/nodes/search`.** gin-роутер (httprouter) не допускает
  одновременно static-сегмент `search` и wildcard-параметр `:id` на одной позиции: `/nodes/search`
  рядом с существующим `/nodes/:id` — паника при регистрации маршрута. Тот же приём уже применён в
  §7.4 (`/nodes/:id/log/:logId` vs `/nodes/:id/logs/stream` — разведены `log`/`logs`). Отдельный
  префикс `/api/search/*` не только обходит конфликт, но и оставлен расширяемым (будущий поиск по
  логам и т.п.).
- **Расширяем `ListNodesFilter`, а не добавляем метод в `NodeRepo`.** Кросс-командный поиск отличается
  от обычного `List` только множеством команд, поэтому поле `TeamIDs []string` (непустой → `team_id =
  ANY($1)`, `TeamID` игнорируется) не меняет интерфейс `NodeRepo` — ни один стаб в unit-тестах не
  ломается (тот же аргумент ISP, что в комментарии к `NodeTableUsage`/§60). Отдельный метод/порт был
  бы избыточен.
- **Имена команд обогащаются в usecase, не джойнятся в SQL.** `SearchAcrossTeams` уже вызвал
  `ListUserTeams` (для набора team_id), поэтому map `id → slug/name` строится из него — ноль лишних
  запросов и никакого JOIN с `teams` в горячем поисковом запросе.
- **Выбор результата НЕ переключает команду явно.** Достаточно `navigate('/nodes/:id')`: существующий
  `useEnsureNodeTeam` (§58/§4.36) одноразово переключит сессию на команду узла и обработает
  «unavailable». Явный `switchTeam` перед navigate дублировал бы логику и конфликтовал с one-shot-guard
  хука (переключение РОВНО ОДИН РАЗ на открытие узла).
- **Текст глобального поиска — в `sessionStorage`, а не в URL** (в отличие от фильтров Overview §54).
  Топбар глобален (виден на всех страницах), а URL принадлежит конкретной странице — писать в него
  из шапки некорректно. Ключ `nexus.globalsearch.q`, запись на каждый change; переживает уход/«Назад»
  и после выбора результата НЕ очищается (пользователь ищет ещё раз).
- **История — одна общая, в PG, без audit.** Оба поля (шапка + Overview) ищут в одном домене
  (`path`/`target_url` узлов), поэтому список общий (набрал на Overview — доступно в шапке). Хранение
  в PostgreSQL (образец §49), а не web-storage: строго per-user, переживает смену браузера. **Без
  audit-записи** (в отличие от §49): запись истории — высокочастотное действие, зашумило бы журнал.
  Дедуп — `PK (user_id, query)` + `ON CONFLICT DO UPDATE searched_at` (повтор всплывает); cap 10 —
  server-side DELETE в транзакции upsert'а.
- **Триггеры записи — не префиксы.** Наивная запись «на каждый ввод» засорила бы историю префиксами
  («no», «nod», «node»). Пишем только по завершённому намерению: глобальный поиск — при выборе
  результата; Overview — по Enter и по blur непустого поля (≥ 2 рун). Сервер дедупит — повторы
  безвредны; `pickingHistory`-флаг гасит запись «на blur» при клике по пункту истории (иначе
  сохранилась бы начатая-но-не-завершённая строка вместо выбранной).
- **cmdk-в-popover через `PopoverAnchor`.** Инпут шапки всегда видим (не в `PopoverContent`, как у
  combobox'ов формы §41), поэтому `<Command>` оборачивает и инпут (в `PopoverAnchor`), и список (в
  `PopoverContent`). Radix-портал сохраняет React-контекст, поэтому клавиатурная навигация cmdk
  работает сквозь него; `onOpenAutoFocus={preventDefault}` удерживает фокус в инпуте.
- **Открытие попапа — `onFocus`/`onClick` на ОБЁРТКЕ `PopoverAnchor`, не на инпуте (поймано стендом).**
  Две грабли, каждая рушила дропдаун: (1) `CommandPrimitive.Input` (cmdk) управляет фокусом сам и НЕ
  пробрасывает наш `onFocus` — вешать его надо на обёрточный `<div>` `PopoverAnchor` (React-события
  focus всплывают с инпута); (2) даже с рабочим `onFocus` Radix закрывает только что открытый попап по
  `pointerdown` того же клика (dismiss-слой считает инпут-якорь «вне контента»). Лечится `onClick` на
  обёртке — он приходит на `mouseup`, после дисмисса, и переоткрывает. Оба места (`GlobalSearch` и поле
  «Поиск» Overview) ставят `onFocus`+`onClick` на обёртку. Обычный `<Input>`-атом §21 `onFocus`
  пробрасывает штатно, но pointerdown-дисмисс всё равно требует `onClick` — поэтому единый приём.

### 4.41 §63 — автор изменения узла: неочевидности

Разобрано при реализации §63 (кто создал / последним изменил узел).

- **Колонка `updated_by`, а не аудит-лог.** Аудит хранит логин денормализованно и уже фильтруется по
  `target_id`, но три причины против него: (1) `DeleteOlderThan` (retention) чистит старые записи → у
  давно не менявшегося узла автор бы исчез; (2) `/api/audit` — scope `audit:read` (manager+), а инфо
  узла видят и `viewer` → пришлось бы обогащать `NodeResponse` доп. запросом; (3) один запрос на каждый
  `Get` узла. Колонка `updated_by` пишется в том же UPDATE, что бампает `updated_at` → автор всегда
  соответствует показанной дате, отдаётся в `NodeResponse` без джойна (логин — не секрет, как
  `auth_login`), переживает retention.
- **«Меняли или нет» — по `updated_at !== created_at`, без отдельного флага.** На INSERT обе даты
  получают ОДИН `now()` транзакции (Postgres `transaction_timestamp()` стабилен в пределах транзакции) →
  строки идентичны → фронт скрывает «Обновлено». Первый `Update` бампает `updated_at` → строки
  различаются → «Обновлено» показывается. **Тонкость с Copy:** клон создаётся и его снимок allowlist
  (`UpdateAllowedHostsSnapshot`, бампает `updated_at`) пишутся в ОДНОЙ UoW-транзакции — тот же `now()`,
  поэтому `created_at==updated_at` у копии сохраняется, «Обновлено» скрыто (иначе свежая копия сразу
  показывала бы «Обновлено»).
- **`cloneNodeForCopy` копирует поля источника** (`clone := *src`) — включая `CreatedBy`/`UpdatedBy`.
  Их надо СБРОСИТЬ (как ID/таймстемпы), иначе автор копии = автор источника. `Copy` затем ставит
  `clone.CreatedBy = actor.UserLogin` (копировщик).
- **Интерфейсная правка `UpdateAllowedHostsSnapshot(+updatedBy)`.** Снимок allowlist бампает
  `updated_at`, поэтому и `updated_by` обновляем в лад (иначе дата новее автора). Добавление параметра в
  метод `port.NodeRepo` ломает все стабы — обновлены 5 (memNodeRepo, notif/replay/orphan/aqNodeRepo).
  Альтернатива (не трогать автора в этом пути) дала бы рассинхрон даты и автора — отвергнута.
- **`Update` переносит `created_by` с исходного узла** (`n.CreatedBy = old.CreatedBy`): PUT приходит без
  `created_by` (его нет в форме), а SET его не трогает — но чтобы ответ `nodeToResponse` содержал верного
  создателя, переносим из прочитанного `old`.

### 4.42 §64 — внешняя (ручная) таблица логов: неочевидности

Разобрано при реализации §64 (Nexus как фронт логирования над чужой таблицей).

- **Перенос узла ребейзил имя таблицы — для внешней это ломает всё.** `Move`/`MovePreview` считали
  новое имя как `rebaseCHTable(table, target.CHDatabase)` → `nexus_<целевая команда>.<имя>`. Для
  таблицы, которой владеет посторонний сервис, это уводит узел на несуществующее имя в чужой БД, а
  сама таблица остаётся без читателя (и без владельца-узла — её никто не покажет в UI). Хелпер
  `targetCHTable` возвращает имя как есть при `ExternalTable`. Заметили только при трассировке Move —
  ни один существующий тест этого не ловил; новый `TestNodeUC_Move_ExternalTable` красный на старом
  коде.
- **Гейт в SQL, а не в usecase — потому что Sender не читает флаг.** `ListClickHouseTables` и
  `ListForHousekeeping` возвращают `[]string` / узкий скан без `external_table`, а `selectByPath`
  Sender'а поле вообще не выбирает (рантайму оно не нужно: писать во внешнюю таблицу можно). Значит
  отличить внешний узел по загруженному `domain.Node` на стороне Sender'а нельзя — фильтр обязан быть
  в `WHERE`.
- **Фильтр на уровне узла, а не таблицы.** `SELECT DISTINCT clickhouse_table … AND NOT external_table`
  оставляет имя в выборке, если на него ссылается хотя бы один НЕ-внешний узел. Это осознанно:
  такой таблицей всё равно управляет Nexus (через второй узел), и не мигрировать её схему было бы
  хуже. Полная неприкосновенность гарантируется только когда ВСЕ узлы таблицы внешние.
- **Legacy-узлы пришлось мигрировать, иначе retention молча выключился бы.** До §64 пустой
  `clickhouse_template_id` не мешал housekeeping'у: он смотрит только на `clickhouse_table` и
  `retention_days`. Если бы «ручная = неприкосновенная» применилось к существующим узлам, их партиции
  перестали бы дропаться без единого сообщения, а таблицы росли бы до конца диска. Поэтому миграция
  0027 разово проставляет им дефолтный шаблон. Down не реверсит: до-миграционное состояние (кто был
  `NULL`) невосстановимо, а шаблон фиксирует ровно прежнее поведение.
- **`external_table` + `clickhouse_template_id` — валидируемый конфликт, а не «шаблон побеждает».**
  Молчаливое игнорирование одного из полей дало бы узел, про который оператор думает одно, а Nexus
  делает другое. `provisionTable` дополнительно защищён порядком проверок: гейт external стоит ПЕРЕД
  веткой явного шаблона.
- **Расхождения структуры — это 200, а не 4xx.** `POST /api/ch-tables/verify` отвечает `ok=false` с
  телом; ошибкой считается только несостоявшийся вызов (400 кривое имя, 503 нет ClickHouse). Иначе
  фронту пришлось бы разбирать «ошибка запроса или ответ по существу» по коду.
- **Типы колонок сверяются с нормализацией, но без разворачивания обёрток.** `DateTime('UTC')` ==
  `DateTime` (таймзона — атрибут отображения, представление то же), пробелы в параметрах убираются. А
  `Nullable(String)` против `String` — честный mismatch: Nullable меняет представление и поведение
  вставки, и «ок» здесь ввело бы в заблуждение. `DateTime64` тоже не подменяет `DateTime`.
- **`Bool` == `UInt8` — иначе ложное срабатывание на реальных таблицах (поймано пользователем на
  стенде).** Проверка `nexus_default.loadtest` (623 972 строки) выдавала «Не совпадают типы: done:
  Bool → UInt8», хотя Nexus читает и пишет эту таблицу без ошибок. Причина: в ClickHouse `Bool` —
  алиас `UInt8` с тем же физическим представлением, а поле `domain.LogRecord.Done` объявлено в Go как
  **`bool`** (не `uint8`), поэтому `chlog.Writer` делает `Append(bool)`, а `LogReaderCH` —
  `Scan(*bool)`, и драйвер одинаково работает с обеими формами колонки. Проверено разовой программой
  на живом CH: запись и чтение через `clickhouse-go` проходят и в `UInt8`-, и в `Bool`-колонку.
  **Тонкость, из-за которой сперва был сделан неверный вывод:** та же программа с `uint8` вместо
  `bool` падает на обеих операциях (`converting uint8 to Bool is unsupported`), что выглядело как
  доказательство несовместимости — пока не сверили Go-тип поля с кодом писателя и читателя.
- **`readCodecs` §56 не годился для проверки — он не берёт `type`.** Инспектор читал из
  `system.columns` только `name` + `compression_codec`. Понадобился отдельный `ReadTableColumns`
  (`name, type ORDER BY position`), а не расширение существующего снимка `CurrentTableSchema`: тому
  типы не нужны, и раздувание структуры ради одного потребителя нарушило бы ISP.
- **Typed-nil при wiring verify-usecase.** `var insp webport.CHSchemaInspector` + присвоение
  nil-указателя дало бы интерфейс, не равный `nil`, → проверка `columns == nil` в usecase пропустила
  бы вызов и упала паникой вместо 503. В `app.go` ветка без ClickHouse передаёт явный `nil`.
- **Дефолты формы создания подставляются с guard-ref, а не в `emptyForm`.** Шаблон, префикс БД и
  лимит тела приходят из трёх асинхронных источников (список шаблонов, членства, публичные настройки).
  Без «применить один раз и только пока поле в исходном значении» медленный ответ затирал бы то, что
  оператор уже успел ввести.
- **Гейт владения §70.4 спрятал ВСЕ логи внешней таблицы (боевой инцидент, фикс в 1.22.4).** См.
  §4.62 — там разбор механики и того, почему ни один гейт этого не поймал.

### 4.43 Basic-auth узла: расследование «правильный пароль не подходит»

Жалоба пользователя «не мог ввести правильный пароль» разобрана по всей цепочке + прогнана матрица
из 13 сценариев на живом стенде (скрипт-матрица: создание узлов с incoming/outgoing basic через API,
вызовы через единый вход с разными Authorization). **Бэкенд корректен байт-в-байт**: спецсимволы,
двоеточия в пароле, literal `***`, «пустой пароль при правке = оставить старый», исходящий заголовок
дословно — всё работает (`CredentialsMask` в dto.go — мёртвая константа, для узлов маскирования
`***` НЕТ, контракт «оставить старые» = пустая строка).

Реальные причины — на уровне браузера/формы, все закрыты в UI:

- **Автозаполнение браузера (главный подозреваемый).** `SecretInput` и логин-инпуты basic-блоков не
  имели `autoComplete` — менеджер паролей мог молча подставить креды пользователя в пустое поле
  «пароль» (= «оставить старый» при правке), и сохранение затирало рабочий пароль узла. Фикс:
  `SecretInput` теперь по умолчанию `autoComplete="new-password"` (переопределяемо), логин-инпуты —
  `autoComplete="off"` ([SecretInput.tsx](../web-ui/src/components/ui/SecretInput.tsx),
  [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)).
- **Пробел по краям из copy-paste.** Сервер НЕ триммит и сравнивает байты
  (`subtle.ConstantTimeCompare`, [auth.go](../internal/receiver/usecase/auth.go)) — это осознанный
  контракт (пароль с пробелом внутри легален). Стенд подтвердил: пароль с хвостовым пробелом
  сохраняется и «тот же» пароль без пробела получает 401. Фикс: клиентская валидация блокирует
  ведущие/хвостовые пробелы в логине и пароле basic
  ([nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts) `basicCredsError`, коды
  `login_whitespace`/`password_whitespace`).
- **Пароль без логина** склеивался в `":password"` (логин молча терялся — актуально для
  legacy-кред без `:`, где `auth_login` префиллится пустым). Фикс: код
  `login_required_with_password` в той же валидации.

### 4.44 Team-scoped ключи react-query ОБЯЗАНЫ содержать teamId (алиасинг кеша)

Жалоба «стёр поле поиска — узлы открылись не той команды». Корень: `Overview` держал
`queryKey: ["nodes", search]` **без id команды**, а сервер фильтрует `/api/nodes` по команде
СЕССИИ — один и тот же ключ (`["nodes",""]`) хранил список «какой команды он был при последнем
запросе». Сценарий: глобальный поиск §62 → выбор узла чужой команды → `useEnsureNodeTeam`
переключает сессию → возврат на Overview (фильтр `?q=` жив по §54) → очистка поиска → ключ меняется
на `["nodes",""]` → react-query **мгновенно отдаёт закешированный список прежней команды**
(stale-while-revalidate). `invalidateTeamScoped` тут не спасает: он лишь помечает stale, а от
алиасинга записей разных команд в один слот не защищает.

Фикс: хук `useCurrentTeamID()` ([lib/teams.ts](../web-ui/src/lib/teams.ts)) + teamId в ключах
`["nodes", teamId, search]`, `["metrics-overview", teamId]`, `["metrics-nodes", teamId, period]`
([Overview.tsx](../web-ui/src/pages/Overview.tsx)) и `["audit", teamId, filter]`
([AuditLog.tsx](../web-ui/src/pages/AuditLog.tsx)) — единственные team-scoped ключи без
node-id-компонента. `enabled: teamId !== ""` — пока членства не загрузились, запрос не шлётся
(иначе ответ лёг бы под пустой teamId и алиасился). Префиксные инвалидации `["nodes"]` работают
как раньше. **Правило на будущее: ключ эндпоинта, фильтруемого по команде сессии, обязан включать
teamId** (node-scoped ключи вида `["logs", id, ...]` не алиасятся — узел живёт в одной команде).

Смежный фикс из той же сессии: админ-страница «Команды» инвалидировала после create/delete только
`["teams"]`, а переключатель шапки читает `["me-teams"]` (`MY_TEAMS_KEY`) — новая команда (создатель
добавляется owner'ом на бэке, `team.go`) не появлялась в дропдауне до F5. Теперь create/delete и
операции с участниками (админ мог добавить/убрать/переролить себя) инвалидируют оба ключа
([settings/Teams.tsx](../web-ui/src/pages/settings/Teams.tsx)).

### 4.45 §65 — команда узла на форме и вкладке «Конфиг»: только имя

Форма узла ([NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx)) показывает **отдельную
карточку** в правой колонке под «Предпросмотром маршрута» (итог трёх итераций фидбэка: блок над
карточкой опускал предпросмотр, строка/низ внутри карточки — сливались с ней) со строками в
формате «Конфига» (лейбл слева, разделители, 13px): «Команда» + для сохранённого узла даты §63
(«Создано» всегда, «Обновлено» только при `updated_at ≠ created_at`, пустой автор не выводится;
на форме создания дат нет — узла ещё нет). Источники разные по режиму: правка —
`useNodeTeam(id).team_name` (резолвер §58; ключ `["node-team", id]` уже закеширован
`useEnsureNodeTeam`, второго запроса нет), создание — имя текущей команды сессии из `useMyTeams()`
(узел будет создан именно в ней). Вкладка «Конфиг»
([ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx)) приведена к тому же формату: **только
имя** — убраны `(slug)` и фолбэк на UUID (сырой идентификатор в UI не показываем; имя недоступно →
«—», с фолбэком членства → резолвер §58). ТЗ — [sections/65-*.md](sections/).

### 4.46 §66 — отображаемое имя пользователя: снапшоты и фолбэки

Миграция `0028_user_name`: колонка `users.name` + разовый backfill `name = login`. Обязательность —
на API (`binding:"required"` в create/update DTO), в хранилище — страховочный
`COALESCE(NULLIF(name,''), login)` (внутренние вызовы вроде bootstrap admin имени не передают).

Неочевидности:

- **Снапшоты автора пишутся именем, а не резолвятся на чтении.** `nodes.created_by/updated_by` и
  `user_audit.user_login` — снапшоты строки; динамический резолв логин→имя невозможен для
  не-админов (список пользователей admin-only) и ломается при удалении пользователя. Поэтому
  `Actor.UserLogin` теперь заполняется `Session.DisplayName()` (имя, фолбэк логин) в `actorFromCtx`
  и `userActor` — все существующие потребители (аудит, §63) получают имя без правок. Старые записи
  остаются с логином — он равен имени на момент миграции, расхождения нет.
- **Redis-сессии переживают деплой без поля Name.** JSON-десериализация даст `Name==""` →
  `DisplayName()` фолбэчит на `Login` до следующего входа. Аналогично активная сессия
  переименованного пользователя несёт старое имя до перелогина (осознанно — сессии из-за смены
  подписи не рестартуем; след переименования есть в аудите: `name` + `prev_name`).
- **`userActor` раньше вообще не заполнял `UserLogin`** — записи user-CRUD в аудите подписывались
  "system". Попутно исправлено (теперь имя актёра).
- **UI: имя — основное, логин — вторичное на админ-страницах.** Sidebar-чип и участники команд —
  имя; таблица Settings → Users и select кандидатов — имя + логин рядом (логин = кредентиал, админ
  обязан его видеть; тёзки различимы). `PUT /api/users` теперь требует `name` — не забыть его в
  любом новом вызове (грабля: toggleActive слал PUT без name и получал бы 400).
- Поиск пользователей ILIKE расширен на `name`. ТЗ — [sections/66-*.md](sections/).

### 4.47 §21.5.1 — модалки не закрываются кликом по подложке

Жалоба: диалоги создания команды/пользователя закрывались кликом в любое пустое место — введённые
данные молча терялись. Radix Dialog в проекте нет; закрытие по оверлею жило ровно в **4 местах**
(один общий атом + две локальные копии + один inline), исправлены все:
[components/ui/Modal.tsx](../web-ui/src/components/ui/Modal.tsx) (накрывает 9 диалогов:
DryRun/Replay/CopyNode/AllowedHosts/Headers/DeleteNode/MoveNode/CHSchemaSync/Confirm),
локальные `Modal` в [settings/Users.tsx](../web-ui/src/pages/settings/Users.tsx) и
[settings/Teams.tsx](../web-ui/src/pages/settings/Teams.tsx), inline-оверлей
[OrphanTablesPanel.tsx](../web-ui/src/components/OrphanTablesPanel.tsx) (у него не было и Esc —
добавлен). Правило: закрытие ТОЛЬКО явным действием (Отмена/X/Esc), убраны `onClick={onClose}` с
подложки и ставшие ненужными `stopPropagation`. Radix Popover (дропдауны/меню/поиск) не трогали —
там закрытие по клику мимо ожидаемо. При добавлении нового диалога подложке `onClick` не давать
(ТЗ §21.5.1).

### 4.48 Валидация полей «Логирования» гейтится по LoggingEnabled (черновик имени таблицы)

Жалоба: при ВЫКЛЮЧЕННОМ тумблере «Логирование включено» форма отвергала сохранение из-за
недозаполненного имени таблицы (дефолтный префикс `nexus_x.` формы создания §64) — а поля карточки
при выключенном тумблере задизейблены (`<fieldset disabled>`), исправить их нельзя, не включив
логи. Любая блокирующая ошибка по задизейбленному полю — ловушка.

Фикс — согласованно на трёх уровнях:

- **domain.Node.Validate**: формат `ClickHouseTable` (§42) и `MaxBodySizeEnabled && MaxBodySize<=0`
  теперь проверяются только при `LoggingEnabled` ([node.go](../internal/domain/node.go)).
  Недозаполненное имя у узла с выключенными логами — **черновик**: хранится, но не используется;
  при включении логов валидация снова потребует корректное имя. Диапазон MaxBodySize (0..10M) и
  конфликт §64 external+template проверяются по-прежнему всегда.
- **Черновик не должен дойти до CH**: `provisionTable` рано выходит на невалидном имени (иначе
  CREATE по шаблону — он выполняется и при выключенном логировании — упал бы и завалил сохранение,
  [node.go](../internal/web/usecase/node.go)); `ListClickHouseTables` обоих сервисов и
  `ListForHousekeeping` фильтруют невалидные имена через `domain.IsValidCHTableName`
  ([node_repo.go](../internal/web/adapter/out/postgres/node_repo.go),
  [nodepg/reader.go](../internal/sender/adapter/out/nodepg/reader.go)) — стартовые ALTER'ы и
  housekeeping черновики не трогают. Read-слой и так отсеивает их мягко (§43.1).
- **Клиент** ([nodeValidation.ts](../web-ui/src/lib/nodeValidation.ts)): те же две проверки под
  гейтом `logging_enabled` (новое поле `NodeFormLimits`).

Тесты: `TestNode_Validate_ClickHouseTableFormat` (кривые имена при logging on → ошибка, при off →
ок), `TestNode_Validate_MaxBodySizeRequired_GatedByLogging`,
`TestNodeUC_Create_InvalidTableDraft_SkipsProvision` (шаблон + черновик → CreateTable не зовётся),
vitest-кейсы nodeValidation.

**Follow-up (метрики не были под §43.1-гейтом).** Утверждение «read-слой отсеивает черновики
мягко» было неполным: `IsValidCHTableName` стоял только в `resolveNode` логов
([logs.go](../internal/web/usecase/logs.go)), а метрики дашборда шли мимо. Симптом на стенде:
узел с выключенными логами и черновичным именем (`nexus_default.`) давал
`WRN nodes overview: node kpi failed ... invalid table name` на **каждом** поллинге
`/api/metrics/nodes`. Фикс: тот же гейт в `nodesOverviewCH` и `NodeMetrics`
([metrics.go](../internal/web/usecase/metrics.go)) — деградация до нулей с Debug-строкой
(§51.9) вместо WRN-флуда. `PurgeFailed`/replay не трогали: это разовые действия по кнопке,
явная ошибка там уместна. Тесты: `TestMetricsUsecase_NodesOverview` «draft table name → нули
без CH-запроса», `TestMetricsUsecase_NodeMetrics` «draft table name → chart unavailable»
(фейк возвращает ненулевой KPI — на старом коде тесты красные).

### 4.49 §68 — multipart/form-data: плейсхолдер вместо тела, детект для replay

Три пункта ТЗ решаются очень асимметрично по объёму кода, и это стоит помнить будущему агенту:

- **Проброска (п.1) и учёт размера (п.2) уже работали.** Тело везде — непрозрачный `[]byte`,
  `Content-Type` c `boundary` копируется дословно (sync/async/RMQ/dry-run), а `request_size`/
  `response_size` (§42.10) считаются по полному телу в байтах ДО усечения лог-копии. Так что «сделать
  проброску» и «учитывать вложение в размере» — это не написать код, а зафиксировать инвариант
  тестами и матрицей §68.4. Не ищите то, что менять, — там нечего.
- **Единственная содержательная правка (п.3) — подмена лог-копии в `SendUsecase.Send`.** Это точка,
  где строится `LogRecord`, поэтому подмена автоматически покрывает и CH INSERT, и Kafka-retry §38
  (он сериализует уже готовый `LogRecord`). Делать подмену в ресивере/адаптере было бы неверно —
  тело там ещё нужно для внешнего вызова.
- **«Освобождение от `max_body_size`» — это ОТСУТСТВИЕ вызова `truncateRunes`, а не новый флаг.**
  Жёсткого reject'а по `max_body_size` в коде нет (§43 первую редакцию с 413/502 откатили — см.
  историю в 43-body-size-hard-limit.md), лимит лишь режет лог-копию. Для multipart лог-копия — это
  короткий плейсхолдер, его резать незачем. Транспортные капы (32 МиБ receiver, 64 МиБ Kafka)
  остаются и действуют на multipart тоже — по решению заказчика.
- **Детект placeholder'а для replay — структурный, а не по флагу-колонке.** `IsMultipartLogPlaceholder`
  проверяет, что 1-я строка — валидный multipart media type, а 2-я начинается с `- part`/`[`. Выбор
  сделан ради «без миграций»: колонка-флаг «это плейсхолдер» была бы надёжнее, но требовала бы ALTER
  всех таблиц. Цена — два известных зазора: (а) экзотическое `text/plain`-тело, имитирующее формат,
  даст ложную 422 (последствие мягкое, ручной override разрешён); (б) **multipart-записи, сделанные
  ДО §68 (сырое тело в CH), детект не ловит** — replay отправит сохранённые байты как раньше.
- **Первая строка fallback'а обязана быть валидным multipart media type.** При неразобранном теле
  (нет boundary / битый Content-Type) `MultipartLogPlaceholder` НЕ печатает сырой Content-Type первой
  строкой — иначе `IsMultipartLogPlaceholder` не распознал бы собственный вывод, и replay такой
  записи не заблокировался бы. Подставляется дефолт `multipart/form-data`. Есть отдельный тест.
- **`headerGet` регистронезависим намеренно.** Все текущие пути кладут канонический `Content-Type`,
  но полагаться на это хрупко (envelope/gRPC/RMQ могут прислать иначе) — детект не должен зависеть
  от регистра ключа. `resp.Headers` из httpclient тоже проходит через `headerGet` для симметрии.
- **TS-зеркало детекта в `ReplayDialog` — предупреждение, не защита.** Оно лишь показывает warn и
  дизейблит кнопку заранее; авторитетно решает бэкенд (422). При расхождении зеркала с Go-детектом
  максимум пропадёт подсказка — сервер всё равно отклонит. Держать в синхроне (комментарий-ссылка на
  `domain/multipart.go` есть с обеих сторон).

### 4.50 §69 — адрес доставки фиксируется в конверте; вкладка «Очередь» у sync-узлов

Раздел вырос из боевого инцидента 2026-07-27. Полезное будущему агенту — не «что сделано», а
почему симптом выглядел именно так:

- **Async-сообщение несёт ЗАФИКСИРОВАННЫЙ адрес, sync резолвит его каждый раз.** Receiver кладёт в
  конверт уже готовый `target_url` (`BuildEnvelope`), поэтому после исправления конфига узла
  синхронные вызовы починились мгновенно, а принятые async-сообщения продолжали лететь по старому
  битому адресу. Диагностический признак в логах узла: у sync-записей URL со схемой и 200 OK, у
  async-записей за то же время — URL без схемы и `unsupported protocol scheme ""`. §69.3 это
  чинит для `url_mode=static` (пересборка в `buildSendInput`), но **для `from_request` симптом
  остаётся принципиально**: исходное значение url-параметра вырезано из query на приёме и в
  конверте есть только в составе самого адреса — восстановить «а какой url_base прислал клиент»
  нечем.
- **Повтор «раз в 5 минут» — это `dlq_retry_delay_seconds` узла, а не клиентский cron.** Признак,
  по которому это различается в логах: у повторов ОДИН И ТОТ ЖЕ `ID` записи и одинаковая query
  (`date_since`/`date_till` не сдвигаются), а интервал ровно равен per-message backoff. Проход
  sweeper'а идёт раз в 60 с (`sender.reprocessor.interval_sec`), поэтому эффективная пауза =
  `max(interval, delay)`. Жить это будет до `dlq_ttl_seconds` (сутки по умолчанию), отсчёт — от
  `received_at`, а не от последней попытки.
- **Peek очереди (`/async-queue/messages`) НЕ показывает DLQ.** Он читает основной топик и
  delay-топик paused-узлов (§3.6). Циркулирующее в DLQ сообщение там невидимо — «очередь пуста, а
  повторы идут» это нормально, а не расхождение. Смотреть надо секцию «Неудачные доставки»
  (ClickHouse `done=0`) и `nexus_dlq_reprocess_total{result}`.
- **`purge-failed` и `replay-failed` — операции ClickHouse, а не Kafka.** Именно поэтому они
  корректно работали для sync-узла ещё до §69, но были недостижимы: вкладку скрывал фронт. Обратная
  сторона — `List`/`purge` (настоящая Kafka) для sync-узла бессмысленны, отсюда ранние выходы
  §69.4: без них каждое открытие вкладки sync-узла стоило бы скана двух топиков до `peekCap`.
- **Валидацию схемы нельзя вешать на «любой непустой `target_url`».** У `from_request` поле на форме
  скрыто, но значение из состояния формы уходит на бэк всегда — остаточный мусор от переключения
  режима заблокировал бы сохранение ошибкой на невидимом поле. Проверка привязана к режиму, где
  адрес реально используется (`static`, в него же нормализуются pull-узлы).
- **`url.Parse` — не валидатор.** `url.Parse("example.com/hook")` возвращает `err == nil`, кладя всю
  строку в `Path` (`Scheme` и `Host` пустые). Любая проверка URL обязана смотреть `Scheme`/`Host` —
  для этого и заведён `domain.AbsoluteHTTPURL`, а три прежних инлайн-копии этой логики сведены к
  нему.
- **Диагностика 404 на `/api/metrics/nodes/{id}`.** Такое сочетание («узел отдаётся по
  `/api/nodes/{id}`, а метрики 404») из кода невозможно: оба резолвят узел одинаково. В инциденте
  это оказался артефакт квотинга в PowerShell (`"$id?range=1h"` — `$id?` съедено как имя
  переменной), URL уходил битый. Прежде чем искать дефект — проверьте фактический URL запроса.

### 4.51 После переноса узел открывался в СТАРОЙ команде (кеш резолвера §58)

Багфикс 2026-07-28. Симптом пользователя: «перенёс узел в другую команду, потом открываю этот узел —
открывается старая команда». Бэкенд был не при чём (`Move` меняет `team_id` в PG в той же UoW,
`ResolveTeam` читает PG без кеша) — дефект целиком в кеше фронта:

- **Решение о переключении команды принималось по УСТАРЕВШЕМУ ответу.** `useEnsureNodeTeam`
  переключал сессию один раз на открытие узла (guard `handledId.current === id`), а `target` брал из
  `useNodeTeam` — обычного `useQuery(["node-team", id])`. react-query отдаёт закешированное значение
  первым же рендером (stale-while-revalidate, глобальный `staleTime: 30_000`), и эффект успевал
  сработать на команде узла ДО переноса. Дальше guard залипал: пришедший позже свежий ответ уже
  ничего не менял. Итог — сессию уводило в исходную команду, а team-scoped `GET /api/nodes/:id`
  отдавал 404 → редирект на «/» (§4.34). Через полную перезагрузку страницы (F5) бага не было —
  отсюда «иногда воспроизводится, иногда нет».
- **Фикс из двух частей, обе нужны.** (1) `useNodeTeam` получил `refetchOnMount: "always"`, а
  `useEnsureNodeTeam` доверяет только ответу, пришедшему после монтирования (`isFetchedAfterMount`):
  побочный эффект такого масштаба (смена команды сессии) не имеет права опираться на кеш.
  (2) guard и залипание `ready` ключуются парой `${id}:${target}`, а не одним `id` — тогда узел,
  переехавший при открытой странице (мы сами из другой вкладки или другой админ), переключает сессию
  повторно, а ручная смена команды в шапке (меняет `current`, не `target`) по-прежнему не
  откатывается. Плюс `MoveNodeDialog` выбрасывает ключ `["node-team", id]` из кеша после успешного
  переноса (иначе строка «Команда» на форме/вкладке «Конфиг» ещё 30 с показывала прежнее имя).
- **Почему это не поймали тесты.** Баг живёт в связке «кеш react-query + одноразовый эффект»: юнит
  на `ResolveTeam`, integration и любой прогон с перезагрузкой страницы его не видят. Регрессия
  закрыта хук-тестом [nodeShare.test.tsx](../web-ui/src/lib/nodeShare.test.tsx) — он поднимает
  `QueryClient` с прод-ным `staleTime` и предзаполняет `["node-team", id]` командой ДО переноса
  (2 из 5 кейсов красные на старом коде). На стенде проверено на обоих бандлах: старый — узел
  «отскакивает» на «/» с командой Default (GET узла 404), новый — открывается, шапка показывает
  новую команду.
### 4.54 CA внешних узлов: смена УЦ у контрагента ломает узел без единого нашего изменения

Боевой инцидент 29.07.2026 (узел `qr`, эквайринг Альфа-Банка): в 16:05 MSK узел, не менявшийся с
22.07, начал отдавать 100% ошибок. Причина целиком снаружи — `pay.alfabank.ru` переехал на цепочку
`leaf → Russian Trusted Sub CA → Russian Trusted Root CA` (НУЦ Минцифры), которого нет в бандле
`ca-certificates` образа alpine. Что здесь стоит знать наперёд:

- **Симптом двухслойный и второй слой маскирует первый.** Настоящая ошибка
  (`tls: failed to verify certificate: x509: certificate signed by unknown authority`) видна лишь в
  части записей: после пяти неудач открывается circuit breaker, и дальше 90% логов — безликое
  `circuit_breaker_open`. Диагностируя «узел встал», ищите в логах **самую раннюю** причину, а не
  преобладающую.
- **Правило «клади промежуточный CA, а не корневой» — не общее.** Оно родилось из дефекта PKI
  Vozovoz (у корня нет `basicConstraints`, см. [deploy/certs/README.md](../deploy/certs/README.md))
  и на нормальный УЦ не переносится. Для НУЦ кладём **корневой**: он оформлен корректно, живёт до
  2032 и переживает перевыпуск Sub CA. Промежуточных у НУЦ к тому же несколько — на Госуслугах
  опубликован Sub с отпечатком `BB:BD:E2:10:…`, а Альфа отдаёт `21:55:78:50:…`.
- **Проверять надо не только сломанный хост.** Прогон всех целевых адресов узлов трёх команд по
  издателю (`openssl s_client … | openssl x509 -noout -issuer`) показал, что под НУЦ только
  `pay.alfabank.ru`; остальные — GlobalSign / Let's Encrypt / TrustAsia / Vozovoz Issuing. Это
  превращает «что ещё завтра упадёт» из догадки в проверенный список.
- **Дырка в наблюдаемости.** Ни метрика, ни алерт не отличают «контрагент сменил УЦ» от любой другой
  ошибки доставки — инцидент обнаружил пользователь, а не мониторинг. Кандидат на будущее: алерт на
  «узел, у которого error_rate прыгнул с ~0 до ~100% за один интервал».
- **CI не собирает Dockerfile'ы** (job'ы только test/lint/build/security). Правка образа проверяется
  исключительно локальной сборкой — см. процедуру проверки в
  [deploy/certs/README.md](../deploy/certs/README.md).
- **Область доверия расширяется на весь Sender.** `update-ca-certificates` кладёт CA в системный
  бандл, то есть НУЦ становится валидным якорем для **любого** исходящего запроса, а не только к
  Альфе. Точечного «CA только для этого узла» в Nexus нет; если такое понадобится — это отдельное
  ТЗ (поле у узла + свой `tls.Config` в [httpclient](../internal/sender/adapter/out/httpclient/client.go)).

### 4.63 Линтер `modernize` включён; кеш golangci-lint даёт ПРИЗРАЧНЫЕ находки

Включение `modernize` в [.golangci.yml](../.golangci.yml) — гейт на устаревшие идиомы Go
(подробности набора и настроек см. в пункте 7.4 выше). Что здесь стоит знать следующему агенту:

- **На всей кодовой базе нашлось всего 4 места** — `slicesbackward` в
  [service_logs_handler.go](../internal/web/adapter/in/http/service_logs_handler.go) (выгрузка
  лог-файла шла обратным индексным циклом → `slices.Backward`) и три `atomictypes` в тестах
  (`int32` + `atomic.AddInt32(&x, 1)` → `atomic.Int32` + `x.Add(1)`) в
  [reloader_test.go](../internal/platform/reloader/reloader_test.go) и
  [loop_e2e_test.go](../internal/receiver/adapter/in/http/loop_e2e_test.go). Мало — потому что по
  репозиторию регулярно гоняется `go fix` (Go 1.26). Ценность гейта именно в том, что он ловит
  регресс в CI, а не полагается на то, что кто-то вспомнит про `go fix`.
- **`settings.modernize` принимает ТОЛЬКО `disable`.** Ключа `enable`/`checks` в JSON-схеме нет
  (`golangci-lint config verify` отвергает их как `additional properties … not allowed`), по
  умолчанию включены все анализаторы. Список имён анализаторов — enum `modernize-analyzers` в
  `https://golangci-lint.run/jsonschema/golangci.v2.jsonschema.json`; он **отстаёт** от бинаря
  (`atomictypes`/`slicesbackward` в онлайн-схеме отсутствуют, а v2.12.2 их выдаёт). Отсюда правило:
  не вписывать в `disable` имена по онлайн-справочнику вслепую — CI-образ rolling, и падение будет
  на `config verify`.
- **ГЛАВНАЯ ГРАБЛЯ: кеш анализа golangci-lint выдаёт находки, которых нет.** При отладке этой
  задачи прогон стабильно (воспроизводилось) показывал 8 issues `SA5011: possible nil pointer
  dereference` в [sentry_test.go](../internal/platform/sentry/sentry_test.go) и
  [logs_test.go](../internal/web/usecase/logs_test.go) — на коде вида
  `out := f(); if out == nil { t.Fatal(…) }; out.Field…`, где разыменование заведомо безопасно.
  Находки появлялись/исчезали от смены НАБОРА линтеров (полный конфиг → 8; те же 13 линтеров через
  `--enable-only` → 0; `staticcheck`+`modernize` → 0), что выглядело как взаимодействие линтеров, а
  это чистый артефакт кеша: после `golangci-lint cache clean` полный конфиг даёт `0 issues` два
  прогона подряд. **Вывод для отладки:** прежде чем править код под неожиданную находку
  staticcheck — сперва `golangci-lint cache clean` и повторный прогон. Иначе легко «починить»
  правильный код под фантом. В CI это не стреляет (кеш golangci-lint между jobs не переносится —
  `.go-cache` тянет только `.cache/go-build` и `.cache/go-mod`).
- **Проверка «линтер жив» в job `go-lint`.** `golangci-lint run` с выпавшим линтером печатает
  «0 issues» и завершается успешно, то есть тихая деградация (переименовали линтер, откатили
  конфиг, образ v2.12-alpine уехал вперёд) выглядит как зелёный CI. Поэтому job грепает вывод
  `golangci-lint linters` на `^modernize:` **строго в разделе Enabled** — вывод содержит и раздел
  «Disabled by your configuration» с теми же именами, грep по всему выводу дал бы ложный успех.
  Рядом добавлен `golangci-lint config verify` — валидация конфига по схеме именно того образа,
  что стоит в CI.

### 4.66 §79 — единица «неудачной доставки», семантика столбца графика, вкладка в адресе

**Строка ≠ запись — и это стоило 15 дублей в 1С.** Предикат «неудачной доставки» смотрел на строки
(`uniqExact(ID) WHERE done = 0`), а строка — это ПРОГОН доставки: успешный повтор дописывает новую
строку с тем же `ID`, старая `done=0` остаётся навсегда. Поэтому доставленная запись висела в списке
вечно, а «Повторить все сейчас» брало её же и отправляло повторно. Боевой аудит 2026-08-05:
`op=replay_all total=16 replayed=16`, из них 15 записей были доставлены авто-репроцессором до
нажатия кнопки. Диагностический признак, который стоит помнить: **KPI узла и KPI вкладки «Очередь»
расходились** (1 против 13 на одном окне) — они считали разные единицы.

**Почему два запроса, а не один красивый.** Набор неудачных ID строится как «кандидаты `done=0`
минус те, у кого есть `done=1`». Соблазнительные однозапросные формы отвергнуты по стоимости:
`ID NOT IN (SELECT ID … done=1)` и `GROUP BY ID HAVING max(done)=0` строят структуру по ДОСТАВЛЕННЫМ
записям — то есть по большой стороне (на боевой таблице §64 это 10 млн записей на каждый вызов, а
список поллится раз в 15 с). `argMax(done, date_request)=0` — вообще другая семантика («последний
прогон неуспешен») и недетерминирован на плотных секундах.

**Кламп разности обязателен.** В приблизительном режиме (`metrics_approx_counts`) `delivered`
считается по HLL независимо от `total` и может оказаться больше него. Голое `total - delivered` на
`uint64` показывает пользователю 1.8e19. Отсюда `subUnsigned` — общий для `NodeKPI`, `CountFailed` и
столбцов графика.

**Очистка убирает ровно то, что видно.** Первая редакция §79.1 предписывала строгий `node_id = ?` в
`DELETE` как защиту второго порядка. Integration-тест показал, что это ломает штатный случай:
на внешней таблице §64 пустой `node_id` нормален для каждой строки, такие записи ВИДНЫ в
«Неудачных доставках», и кнопка «Очистить» молча переставала работать (упал
`TestAsyncQueue_PurgeFailed_E2E`). Действующее правило: node-фильтр разрушающей операции совпадает с
читающим, а данные постороннего писателя защищают гейт §70.4 (на чужой БД DML запрещён целиком) и
точный список ID. Урок общий: сужение фильтра в DML выглядит безопасным ровно до первого штатного
сценария, где пользователь видит строку, но не может её убрать.

**Столбец графика: интервал прихода + итоговый статус.** Раньше столбец считал СТРОКИ в интервале
прогона, и на узле с недоступным приёмником завышался кратно числу повторов. Теперь запись относится
к интервалу своего ПЕРВОГО прогона, а цвет берётся по итоговому состоянию в окне — поэтому после
успешного повтора красный сегмент исчезает из столбца сам, как и запись из «Неудачных доставок».
Следствие, которое обязано быть в подсказке: **исторические столбцы меняются задним числом**
(вчерашний красный может позеленеть сегодня). Отвергнутая альтернатива (считать по прогонам
интервала) читается как хроника аварии, но расходится с KPI над графиком и списком под ним.

**Точная форма графика не сжимается приблизительным режимом.** Она требует свернуть строки в записи
по всему окну (`GROUP BY ID`), то есть держать строку на каждую запись (~60–80 байт): 24 ч на
нагруженном узле — единицы МБ, 30 дней на таблице в 10 млн записей — под гигабайт. HLL здесь не
помогает: он сжимает счётчики уникальных, но группировка обязана хранить сами ключи. Отсюда порог
`web.metrics_exact_chart_max_records` (дефолт 2 млн) и пометка `chart_unit=attempts` в ответе —
размер окна известен бесплатно из `kpi.Total`, который считается раньше графика.

**Вкладка в адресе тянет за собой окно журнала.** Если вкладку сделать производной URL, а фильтр
логов оставить в `useState` родителя, «Назад» разводит их в разные состояния. Поэтому `from`/`to`
(в МИЛЛИСЕКУНДАХ на этом маршруте — в отличие от рабочего стола, где те же имена значат RFC3339),
`status` и `done` живут в адресе, а уход с «Логов» их чистит. Условный рендер вкладок трогать
нельзя: `LogsTab` читает `initialFilter` только при монтировании, и переход на CSS-скрытие молча
сломает переходы «Обзор/Очередь → Логи».

**Панель фильтров общая, но её форма живёт не в файле компонента.** `LogsAdvForm`/`emptyAdvForm`/
`advFormEqual` лежат в `lib/logsQuery.ts`: правило react-refresh требует, чтобы компонентный файл
экспортировал только компоненты (тот же приём, что у `period.ts`). Доказательство чистоты выноса
панели — 13 существующих тестов `LogsTab` остались зелёными без единой правки.

**goleak: сначала чиним, потом игнорируем.** Исключение допустимо только для чужих горутин, которые
мы не можем остановить (keep-alive пул `net/http`, фон `grpc-go`). Пограничные пакеты (`otel`,
`bootstrap`, `web/adapter/out/clickhouse`) включались не «на всякий случай с ignore», а после
прогона — все оказались зелёными без единого исключения. `tests/integration` вне политики осознанно:
testcontainers, pgxpool и CH-idle-closer дают 15+ исключений, ломающихся на каждом апгрейде.

### 4.67 §82 — потолок sync-вызова прячется не в таймаутах, а в разрыве транспорта

**Главный урок разбора: «таймаута нет» и «оборвать не может» — разные утверждения.** §4.39 честно
перечислял все места, где мог бы стоять дедлайн, находил их пустыми и подводил к выводу «внутри
Nexus такого дедлайна нет ни одного». Вывод продержался несколько месяцев и увёл боевой разбор
06.08.2026 во внешнюю инфраструктуру больше чем на сутки. Оборвать вызов может всё, что **закрывает
соединение**, а не только то, что ставит срок.

**Механизм.** gRPC-сервер без объявленной `KeepaliveEnforcementPolicy` берёт дефолт grpc-go:
`MinTime = 5 минут`. Клиент с `keepalive.ClientParameters{Time: 30s}` пингует чаще, сервер считает
это нарушением политики (`http2_server.handlePing`) и при `pingStrikes > maxPingStrikes` (2) шлёт
`GOAWAY ENHANCE_YOUR_CALM / "too_many_pings"` **с закрытием соединения**. Счётчик обнуляется флагом
`resetPingStrikes`, который выставляется при создании нового стрима, — но в unary-RPC сервер до
самого ответа не пишет в стрим ничего, поэтому во время долгого вызова нарушения копятся
беспрепятственно: первый ping счётчик обнуляет, следующие три доводят до GOAWAY. Отсюда
**`4 × keepalive_time_sec`**.

**Почему потолок плавающий и почему это сбивало с толку.** Ping-таймер принадлежит СОЕДИНЕНИЮ пула,
а не запросу, и клиент усыпляет keepalive, пока активных стримов нет. Вызов, начавшийся в середине
цикла или на соединении с накопленными нарушениями, умирает раньше: измерено 121,013 с в Receiver
и 91,024 с через Web в одном и том же прогоне. §4.39 при этом учит, что **одинаковая** длительность
серии обрывов указывает на дедлайн вне Nexus, а разброс — на обратное. Правило верное, но одного
разброса мало: он же бывает и у внутреннего разрыва транспорта.

**Как воспроизводить.** Только сквозным прогоном с клиентом БЕЗ собственного таймаута: узел с
`timeout_ms = 600000`, приёмник, отвечающий дольше потолка (`cmd/echosrv` `/delay/300/x`), и серия
не менее трёх запросов подряд — на одиночном запуске плавающий потолок может не проявиться.
Признак в логе Receiver'а — строка `Client received GoAway ... "too_many_pings"` от самого grpc-go.
В юнит-тестах не ловится в принципе: каждая половина договора keepalive по отдельности корректна.

**Что снято с подозрения попутно** (чтобы не проверять заново):

- `receiver.read_timeout_ms` контекст запроса НЕ отменяет — проверено на Go 1.26.5 отдельной пробой:
  при `ReadTimeout = 5 с` handler отработал 20 с с живым `r.Context()`;
- `receiver.write_timeout_ms` и `sender.http_client.timeout_ms` — разобраны в §4.39, к этому потолку
  отношения не имеют.

**Регресс живёт в `tests/integration`, а не в юнитах, намеренно.** Минимальный клиентский ping,
который принимает grpc-go, — 10 секунд (`internal.KeepaliveMinPingTime`), ниже он поднимет значение
сам и тест перестанет что-либо проверять. Значит воспроизведение занимает ~50 секунд, а `make test`
и CI-job `test` гоняют с `-short`. Integration-job запускает каталог целиком без `-short`, поэтому
тест там реально исполняется; имя начинается с `TestSender_`, чтобы попадать и в `make test-int-sender`.

### 4.68 §82.3 — послабление, выданное наружу вместо внутреннего пути

`RouteAsync` не проверял `root_method`, и в коде это было записано явным комментарием: «любой
root_method можно отправить через async — §3.6 при paused все запросы превращаются в async».
Обоснование верное, **вывод неверный**: §3.6 идёт ВНУТРЕННИМ путём (`handleSync` → `ErrNodePaused` →
`handleAsyncFromInput` с тем же `RouteInput`), внешний эндпоинт ему не нужен вовсе. Внутренняя
потребность превратилась в разрешение для любого клиента из интернета, и настройка узла `request`
перестала что-либо значить.

Проверить это стоило одного прогона на стенде — четыре комбинации «узел × эндпоинт», из которых
подозрительной была ровно одна. **Приём на будущее:** если послабление объясняется внутренним
сценарием, спроси, каким путём этот сценарий реально приходит; чаще всего — не тем, для которого
послабление выдано.

Диагностируемость отказа важнее его информативности: наружу отдаётся `404 node not found` (как у
несуществующего узла), а `root_method` и IP клиента уходят в `warn` + метрику
`nexus_async_ingress_rejected_total`. Иначе интегратор ломается молча, а по 404 причину не
восстановить.

Границы гейта перечислены в ТЗ и **закреплены тестами, а не словами** — иначе сужение до «любой
async-вызов требует async-узла» выглядело бы безобидным и сломало бы §3.6 и callback §16.

### 4.69 §83.0 — что происходит при смене `root_method` узла (матрица переходов)

Разбор сделан ПЕРЕД реализацией §83: оператор, переключающий узел между sync и async, должен
понимать судьбу накопленной очереди и своих настроек. Ниже — фактическое поведение на момент
разбора (чтение кода, не догадки).

**Судьба сообщений, уже принятых в очередь.**

| Что | Где | Поведение до §83.7 |
|---|---|---|
| `AsyncProcessor.Handle` — гейты | [async.go:145-271](../internal/sender/usecase/async.go) | узел не найден → ack (:160), `disabled` → ack (:170), отменён оператором §34.4 → ack (:182), `paused` → delay-топик (:198). **`root_method` не проверяется ни разу** |
| delay-топик `nexus.async.paused` | [paused_sweeper.go:135](../internal/sender/adapter/in/kafka/paused_sweeper.go) | sweeper зовёт ТОТ ЖЕ `Handle` — отдельной ветки у него нет, поэтому любой гейт в `Handle` покрывает и отложенные сообщения |
| DLQ, репроцессор §36 | [dlq_reprocess.go:159-204](../internal/sender/usecase/dlq_reprocess.go) | статус узла + breaker + backoff; `root_method` тоже не проверяется |

Отсюда **дефект 83.0-1**: узел, переведённый обратно в sync, продолжает доставлять всё, что успело
накопиться, — и из основного топика, и из отложенных, и из DLQ. Настройка узла на судьбу уже
принятых сообщений не влияет никак. Закрывается §83.7.

**Дефект 83.0-2:** в журнале ClickHouse тип пишется константой `requestAsync` —
[async_envelope.go:148](../internal/sender/usecase/async_envelope.go) (`buildSendInput`) и
[dlq_reprocess.go:268](../internal/sender/usecase/dlq_reprocess.go) (`logTTLExpired`). То есть по
логу нельзя отличить «доставлено, пока узел был async» от «доставлено уже после перевода в sync».
Поэтому §83.7 обязан класть реальный `root_method` в `reason` — иначе отбой по гейту будет
неотличим от обычной ошибки доставки.

**Судьба настроек узла.**

| Настройка | Переход `request ↔ requestAsync` | Переход в `RabbitMQAsync` (pull) |
|---|---|---|
| Ack-спека §83 | **сохраняется** — `NormalizeForRootMethod` ([node.go:157-160](../internal/domain/node.go)) вычищает поля только у pull-узлов (`if !n.RootMethod.IsPull() { return nil }`) | сбрасывается вместе с прочими полями входящего HTTP, попадает в `cleared_incompatible_fields` аудита ([node.go:617-619](../internal/web/usecase/node.go)) |
| incoming auth, `url_mode=from_request`, динамическая исходящая авторизация, `path_passthrough` | сохраняются | сбрасываются там же |

То есть требование «спека не затирается при временном переводе в sync, а при возврате работает
снова» выполняется существующим кодом — но **держится на одной строке**
`if !n.RootMethod.IsPull()`. Поэтому в §83.3 оно закреплено отдельным тестом: правка
`NormalizeForRootMethod` под будущую задачу иначе затрёт настройку молча.

> **Требование отменено (§83.5, новая редакция).** Причина — интерфейсная: карточка «Ответ при
> постановке в очередь» показывалась и у sync-узла, где ответ формирует получатель, то есть
> предлагала включить настройку, которая ни на что не влияет. Теперь группа рендерится только у
> `requestAsync`, а сохранение узла как `request`/`RabbitMQAsync` шлёт `async_ack_spec: null`
> (`ackSpecApplies` в [ackSpec.ts](../web-ui/src/lib/ackSpec.ts) — одно правило и на показ, и на
> сборку payload'а; тип узла стал ОБЯЗАТЕЛЬНЫМ аргументом `ackSpecFromForm`, чтобы правило нельзя
> было обойти забывчивостью вызывающего). **Домен не менялся**: `NormalizeForRootMethod`
> по-прежнему чистит только pull, и тест §83.3 остаётся зелёным — запрет в домене сделал бы
> невалидными уже сохранённые строки и оставил бы §3.6 (спека работает, пока узел на паузе) без
> определения. Через API спеку sync-узлу выставить можно, как и раньше. **Две цены названы явно:**
> временный перевод в sync и обратно требует заполнить шаблон заново, и у переведённого узла
> перестаёт работать пересчёт §84.9 для его старых async-записей — пересчитывать нечем.

**Через сколько смена доезжает до Receiver'а.** `Update` делает write-through в Redis
(`cacheSet`, [node.go:641](../internal/web/usecase/node.go)) И публикует инвалидацию §57
(`publishInvalidate`, :648). Значит новый `root_method` виден практически сразу; окно
неопределённости — TTL L1-кеша (2 с, `l2.go`). Пятиминутный `redis.node_ttl_sec` работает только
как страховка, если отказали ОБА механизма, — в норме ждать 5 минут не нужно (это уточнение против
первоначальной оценки в плане §83).

**Внешние адреса после перехода.** Короткая форма §78.1 разводит вызов по `root_method`
([handler.go:151-161](../internal/receiver/adapter/in/http/handler.go)) — её клиенту менять не
надо. А клиент, ходивший на длинный `/api/v1/requestAsync/...`, после перевода узла в sync получит
`404` от гейта §82.3 ([route_async.go:102-113](../internal/receiver/usecase/route_async.go)). Это
ожидаемое поведение, а не регресс, и оно должно быть написано в §83 явно.

**Решение по недоставленным сообщениям (принято по итогам разбора).** Сообщение узла, переставшего
быть async, уходит в **DLQ с `reason="node_not_async root_method=<текущий>"`**, а не отбрасывается:

- ничего не теряется, запись видна в «Очереди»/«Неудачных доставках» и в журнале узла;
- при возврате узла в async в пределах `dlq_ttl_seconds` репроцессор доставит её сам — то же
  «вернулось само», что и у настройки ответа;
- в репроцессоре исход — `skipped` (как у `paused`), а не `failed`: это не ошибка приёмника.

Отброс без republish рассматривался и отвергнут: он требует ручного «Повторить все» §36.11, а тот
идёт через HTTP на Receiver и упрётся в гейт §82.3, пока узел sync, — то есть восстановление
оказалось бы недоступно ровно в тот момент, когда оно нужно.

### 4.70 §83 — почему ответ рендерится до Kafka, а гейт очереди смотрит в прошлое

Два решения раздела, которые выглядят произвольными, пока не столкнёшься с их альтернативой.

**Рендер ДО публикации.** Естественное место для сборки ответа — handler: там уже есть результат
`RouteAsync`, и метрики живут там же. Но политика `on_error=error` означает «запрос не принят», а в
handler'е сообщение уже в Kafka. Клиент, получивший 400, повторит пакет — и шина, чинившая дубли,
начнёт их создавать. Поэтому рендер стоит в usecase между `AppendPathSuffix` и `BuildEnvelope`, а
handler получает готовые байты. Побочно это же решило вопрос безопасности: в usecase доступны
`effBody`/`cleanQuery` (тело и query ПОСЛЕ вырезания кред), а в handler'е — исходный `in.Body`, из
которого шаблон `${ body.token }` вернул бы клиенту его же секрет.

**Гейт очереди сравнивает режим приёма, а не текущий.** Требование «узел вернули в sync — накопленное
не доставлять» напрашивается на проверку `node.RootMethod != requestAsync`. Она ломает §3.6: у
sync-узла в очереди законно лежат сообщения, попавшие туда, пока узел был на паузе, и их доставлять
обязательно. Различить два происхождения по узлу невозможно в принципе — признак есть только у
сообщения. Отсюда поле конверта `ingress_method`, заполняемое на приёме; пустое (конверты до §83)
трактуется как «доставлять», иначе выкат обнулил бы очередь.

**Приём на будущее:** если новое правило смотрит на текущее состояние объекта, спроси, не лежат ли
рядом артефакты, созданные при ДРУГОМ его состоянии. Для очередей это почти всегда так.

**Что осталось неприятного.** Тип в лог ClickHouse по-прежнему пишется константой `requestAsync`
([async_envelope.go](../internal/sender/usecase/async_envelope.go)) — поэтому реальный `root_method`
приходится дублировать в `reason`. Честнее было бы писать в колонку фактический режим, но это меняет
семантику существующей колонки на боевых данных и в §83 не входит.

### 4.71 §87 — четвёртая роль: почему линейный ранг, а не решётка прав

Роль «Оператор» выглядит как повод развести права по битам («очередь», «конфиг», «аудит»),
но этого не потребовалось, и причина в одном свойстве задачи: **возможности оператора —
строгое ПОДМНОЖЕСТВО менеджерских**. Нет ни одного действия, которое оператору можно, а
менеджеру нельзя. Пока это так, линейная иерархия §26 остаётся корректной, и вся правка
сводится к тому, что часть гейтов опускается с `RequireMinRole(manager)` на
`RequireMinRole(operator)`. Решётка прав понадобится в тот день, когда появится роль с
правом, которого нет у роли выше, — и вот тогда её и вводить, а не заранее.

**Почему сдвиг рангов (`manager` 1→2, `admin` 2→3) обошёлся без миграции данных.** Ранг
нигде не хранится: `grep -rn "rank" internal/ migrations/` не даёт ничего, кроме самого
`Rank()`. В `users.role`, в JSON сессии Redis и в ответах API живёт **строка** роли. Поэтому
живые сессии пережили выкатку, а старые данные не потребовали конверсии. Это свойство стоит
беречь: как только номер ранга где-нибудь запишется на диск, следующая роль в середине
иерархии станет ломающим изменением.

**Две границы, которые выглядят непоследовательно, пока не знаешь причины.**

1. `PATCH /nodes/:id/status` (пауза/отключение) **отдан** оператору, хотя формально это
   изменение узла. Кнопки физически живут на вкладке «Очередь» (`QueueTab.tsx` — это
   единственное место, откуда маршрут вызывается), меняется единственное поле, а без права
   оператор видел бы захлёбывающийся узел и не мог его притормозить.
2. `POST /nodes/dry-run` **не отдан**, хотя ничего не сохраняет. Эндпоинт принимает
   ПРОИЗВОЛЬНЫЙ конфиг в теле (не `id` узла) и в реальном режиме §55 бьёт по внешней цели
   через Sender — то есть это инструмент авторинга конфигурации и SSRF-примитив (§55.5), а
   не средство эксплуатации.

**Грабля выпуска.** `down`-миграция 0035 переводит операторов в `viewer` — иначе сужение
CHECK упало бы на существующих строках. Потеря необратима, а `scripts/release/rollback_info.py`
её **не заметит**: он ищет `DROP TABLE`/`DROP COLUMN`/`DELETE FROM`, а здесь `UPDATE`. Строку
«Откат» в CHANGELOG для этого релиза надо писать руками.

**Что осталось мёртвым и оживлять не надо.** `RequireRole` (точное равенство роли) в проде
по-прежнему не подключён — работает только `RequireMinRole`. Соблазн взять его для «ровно
оператора» будет, но он исключил бы из проверки менеджера и админа.

**Чего роль не закрывает** (перечислено в §87.6, здесь — как напоминание при ревью): это не
ACL на отдельный узел, объём повторов роль не ограничивает (только rate-limit §7.4.1/§85.9),
пауза остаётся отказом в обслуживании, очистка очереди необратима, а чтение тел запросов у
оператора такое же полное, как у наблюдателя.

### 4.72 Дефолт `KAFKA_TOPIC_REPLICATION_FACTOR` = 1: шаблон обязан совпадать с поставкой

Было: `.env.example` и `config.example.yml` предлагали `RF=3`/`ISR=2` («под prod-кластер»), а
единственная фактическая поставка — docker-compose с **одним** брокером в KRaft
(`KAFKA_NODE_ID: "1"`, `KAFKA_CONTROLLER_QUORUM_VOTERS: "1@kafka:9093"`, брокерные
`KAFKA_DEFAULT_REPLICATION_FACTOR: "1"` во всех трёх compose-файлах). То есть репозиторий
противоречил сам себе: `cp .env.example .env` по инструкции DEPLOYMENT §4 давал неработающий
async. Компенсировалось это четырьмя разными напоминаниями «не забудьте RF=1» в DEPLOYMENT —
признак того, что дефолт был выбран неверно.

Стало `1`/`1` везде (`.env.example`, оба `config*.yml`, ТЗ §8/§5), а инструкции инвертированы:
поднимать до `3`/`2` нужно осознанно, на кластере из 3+ брокеров.

**Почему именно так, а не «безопаснее оставить 3».** Асимметрия цены ошибки: RF выше числа
брокеров = полный отказ (`InvalidReplicationFactor` при создании топика, а `ISR=2` на одной
реплике валит каждую запись producer'а с `acks=all` → `NotEnoughReplicas`), тогда как RF=1 на
кластере = работающая, но не отказоустойчивая установка. Для кластерных инсталляций это тихая
деградация durability, поэтому она громко подписана в обоих шаблонах и в чек-листе DEPLOYMENT.

**Отложенность симптома — главная ловушка.** Значения применяются ТОЛЬКО при создании топика:
[admin.go:71](../internal/platform/kafka/admin.go#L71) глотает `TopicAlreadyExists` и не делает
`AlterConfigs`. На стенде с уже созданными топиками неверный RF **никак не проявится** — мина
сработает при появлении нового топика (например `nexus.async.paused` при обновлении версии).
Ровно та же природа, что у ложного следа с `partitions` из комментария
[admin.go:98-112](../internal/platform/kafka/admin.go#L98). Поменять RF существующему топику
правкой конфига нельзя — нужен `kafka-reassign-partitions`.

Найдено 2026-08-14 при восстановлении затёртого `.env` на бою: заполнение файла по свежему
`.env.example` подставило `3`/`2` вместо рабочих `1`/`1` (детали инцидента — memory
`project_env_recovery_from_containers`).

### 4.73 §89.1 — локальные шрифты: вариативность, инлайн и раздача из embed.FS

Четыре неочевидности, каждая из которых ломается молча.

**Google отдаёт РАЗНЫЙ ответ в зависимости от User-Agent.** На дефолтном UA (curl, PowerShell)
`css2` возвращает пять `@font-face` с `format('truetype')` — без woff2, без `unicode-range` и без
деления на сабсеты, причём с кодом 200. Поэтому
[scripts/fonts/update-google-fonts.sh](../scripts/fonts/update-google-fonts.sh) подставляет UA
Chrome и проверяет результат по наличию `unicode-range`, а не по коду ответа.

**Inter и JetBrains Mono — вариативные шрифты: на все веса одного сабсета отдаётся ОДИН файл.**
33 блока в ответе `css2` ссылаются всего на 13 разных URL; вес задаёт только объявление
`font-weight`. Наивное имя `<slug>-<weight>-<subset>.woff2` кладёт в репозиторий 12 побайтово
одинаковых копий — 626 КБ вместо 226 КБ, — и заметить это по выводу сборки нельзя: Vite
схлопывает их по хешу содержимого и печатает 8 файлов. Поэтому имя — `<slug>-<subset>.woff2`, а
скрипт умеет вернуть вес в имя, если у группы окажется больше одного URL.

**`unicode-range` обязателен, а не «оптимизация».** Четыре блока `@font-face` с одинаковыми
`font-family`/`font-weight` без него — это переопределения: побеждает последний, и кириллица
(или латиница) не подгружается вовсе.

**Инлайн шрифта запрещён на стороне сборки.** Правило Vite — «размер < `assetsInlineLimit`» без
исключений для `url()` внутри CSS, а `jetbrains-mono-cyrillic-ext.woff2` весит 1,6 КБ при пороге
4096: по умолчанию он уезжал бы в base64 внутрь render-blocking CSS. После ужесточения CSP
(`font-src 'self'`, без `data:`) это ломало бы рендер молча, поэтому
[vite.config.ts](../web-ui/vite.config.ts) отключает инлайн для `.woff2` явно.

**Раздача из embed.FS** ([spa.go](../internal/web/adapter/in/http/spa.go)): `.woff2`/`.map` нет во
встроенной таблице `mime/type.go`, а в alpine-образе Web нет `/etc/mime.types` (пакет `mailcap` не
ставится) — тип определялся бы сниффером по сигнатуре. Тип ставится явно, чистой функцией
`assetContentType` в middleware группы `/assets`, а не `mime.AddExtensionType` (запись в глобальный
реестр процесса запрещена, CLAUDE.md §4); `http.ServeContent` уважает заранее выставленный
`Content-Type` и тогда не сниффит. Заодно закрыты два соседних дефекта: у `/assets/*` не было
`Cache-Control` вовсе (embed.FS даёт нулевой `ModTime` → нет `Last-Modified`, `ETag` не ставит
никто → браузер качал весь бандл при каждом заходе), а запрос на удалённый хешированный ассет
проваливался в `NoRoute` и получал index.html с кодом 200 — браузер отказывался исполнять из-за
`nosniff` и писал невнятное «Refused to execute script» вместо честного 404.

**Ловушка теста.** Проверять MIME на настоящих байтах шрифта бесполезно: сниффер Go знает
сигнатуру `wOF2` и вернёт `font/woff2` сам, поэтому тест прошёл бы и без нашего заголовка. В
[spa_test.go](../internal/web/adapter/in/http/spa_test.go) полезная нагрузка намеренно без
сигнатуры — тогда `font/woff2` может взяться только из `assetContentType`.

### 4.74 §89.8 — клик после drop: React-обработчик до него не доходит

Найдено живым прогоном на стенде (все 608 фронт-тестов пропустили). Симптом: перетащил команду
в «Избранном» — оказался в другой команде, а сохранённый порядок откатился.

Диагностика (временные метки в `sessionStorage` прямо из компонента) дала неожиданную картину:
`pointerdown` → `dragstart` → `dragend` приходят, **React-обработчик клика не вызывается вообще**
(ни `onClick` строки, ни `onClickCapture` секции), но нативный `click` до `document` **доходит** —
это видно слушателем в фазе захвата, и его цель — перетащенная строка.

Причина: dnd-kit переставляет узлы списка, и к моменту доставки события React не находит для цели
путь по дереву фиберов — синтетическое событие не диспатчится. Действие по умолчанию остаётся:
браузер идёт по `href`, зеркало §76 применяет появившийся `?team=` как входящую ссылку и
переключает команду, а вызванная этим инвалидация затирает только что сохранённый порядок.

Правильное место гашения — [useSuppressClickAfterDrag](../web-ui/src/lib/suppressClickAfterDrag.ts):
НАТИВНЫЙ слушатель `click` на `document` в фазе захвата, взводится на `onDragStart`, гасит ровно
один следующий клик и снимается любым новым взаимодействием (иначе после драга без клика съел бы
чужой клик) и при размонтировании (иначе остался бы на `document` навсегда).

Две попытки, которые НЕ сработали и стоят упоминания, чтобы их не повторяли:

1. сброс флага макротаском (`setTimeout(0)`) — опирался на порядок «click раньше таймера»;
2. `onClickCapture` на секции — не вызывается по той же причине, что и `onClick`.

Дефект существовал и ДО §89.3: с `<button>` клик после драга так же переключал команду, ссылка лишь
добавила к этому переход по адресу. Регрессионные тесты: [suppressClickAfterDrag.test.tsx](../web-ui/src/lib/suppressClickAfterDrag.test.tsx)
(контракт хука) и [SidebarFavorites.drag.test.tsx](../web-ui/src/components/SidebarFavorites.drag.test.tsx)
(сквозь компонент, с подменой `DndContext` — в jsdom нет `PointerEvent`, сенсор не запускается).

### 4.75 §89.9 — `make test-integration` покрывал не все тесты (ложно-зелёный гейт)

Найдено выпуском 1.28.0: тег ушёл с красным CI, упал
`TestOneTimeTokens_MigrationDownUp_E2E`, хотя локальная `make test-integration`
двумя прогонами была зелёной.

**Две причины сложились.**

1. **Тест зашивал глубину отката единицей.** `mg.Down(1)` откатывает ПОСЛЕДНЮЮ
   миграцию, а тест проверял 0036 — это совпадало ровно до тех пор, пока 0036 была
   последней. §89 положил сверху 0037, и `Down(1)` стал откатывать её: таблица
   `one_time_tokens` оставалась на месте, а падение выглядело как «сломан down 0036».
   Исправлено `migrationsAtOrAbove(dir, 36)` — глубина считается по каталогу миграций
   и переживает любые добавления сверху.

2. **Локальный гейт слабее CI.** `make test-integration` гоняет восемь под-прогонов с
   фильтрами `-run` (группировка ради таймаутов), а CI-job `integration` запускает пакет
   ЦЕЛИКОМ. На момент находки **26 тестов из 145** не матчились ни одним фильтром — то
   есть локально не выполнялись вовсе. Среди них были и упавший тест, и свежий
   `TestTeamExternalURL_Repo` из этого же ТЗ (он проходил только потому, что я гонял его
   точечным `-run`).

**Что сделано, чтобы не повторилось:** группа `test-int-misc` (скоупы, статусы узла,
одноразовые ссылки §88, каталог полей, dry-run, rDNS, команды) и гейт
[check_integration_coverage.py](../scripts/ci/check_integration_coverage.py) — он
сверяет имена `func Test*` в `tests/integration` с объединением всех `-run`-фильтров
Makefile и падает, если тест не попал ни в одну группу. Стоит ПЕРВОЙ целью
`test-integration`: проверка не требует ни Docker, ни сети.

**Правило на будущее:** добавил integration-тест — либо он матчится существующей
группой, либо дополни `test-int-misc`. Зелёный `make test-integration` без этого гейта
не означает, что тесты выполнялись.

### 4.76 §90.1 — два читателя `app_settings` ведут себя при ошибке ПО-РАЗНОМУ

Секреты `app_settings` читают два независимых места, и деградация у них противоположная.
Это не недосмотр, а следствие того, что́ каждое из них делает с прочитанным.

**Web-репозиторий (`Get`) — строгий**: ошибка расшифровки возвращается наверх (запрос
настроек падает с 500, сервис жив). Причина — read-modify-write в
[usecase/app_settings.go](../internal/web/usecase/app_settings.go) `Update`: он читает
текущие настройки, накладывает патч и сохраняет **весь** документ. Мягкая деградация
(подставить `nil` или оставить шифротекст) означала бы, что первое же сохранение любой
соседней настройки затирает рабочий секрет — тихая порча данных вместо видимой ошибки.

**Bootstrap-overlay — мягкий**: битое поле зануляется, сервис поднимается на значении из
`.env`/YAML, на каждое поле пишется `Error`. Причина — контракт самого overlay'я
(комментарий в [bootstrap/app_settings.go](../internal/platform/bootstrap/app_settings.go)):
он опциональный слой поверх обязательного env-конфига. Уронить Receiver и Sender из-за
одного битого web-секрета — самопричинённый простой там, где рабочее значение обычно уже
лежит в окружении. Молчаливой деградации нет: `Error` на поле плюс видимые последствия
(ClickHouse не подключается, Sentry молчит).

**Порядок внедрения был обязателен.** Читатели научились понимать шифротекст (Phase 90.5)
**раньше**, чем включилась запись (Phase 90.6). В обратном порядке overlay залил бы
`v1:...` в `cfg.ClickHouse.Password`, и сервисы пошли бы подключаться шифротекстом вместо
пароля — без единой ошибки, просто «CH недоступен».

**Смешанное состояние вместо миграции данных.** Мигрировать значения SQL-миграцией нельзя
(у неё нет ключа), а утилита ротации работает вне сервиса. Поэтому
`Cipher.DecryptLenient` ([crypto/aesgcm.go](../internal/platform/crypto/aesgcm.go))
принимает и plaintext: не похоже на `v1:<4 части>` → отдать как есть. Различение по форме
безопасно для реальных значений (Sentry DSN с двоеточиями даёт другое число частей);
неотличим лишь гипотетический plaintext вида `v1:a:b:c` — записано в godoc.
`ErrDecryption` при этом НЕ проглатывается: несовпадение GCM-тега — доказанный факт чужого
ключа или порчи, и трактовать такое значение как plaintext значило бы отдать наверх
шифротекст под видом секрета.

**Ротация (§90.4) заодно чинит два старых дефекта**
([keyrotate](../internal/platform/keyrotate/keyrotate.go)): `nodes.rmq_password` в список
колонок не входил (после смены ключа pull-узлы молча переставали подключаться), а одно
историческое plaintext-значение валило весь прогон. Теперь plaintext шифруется и считается
отдельным счётчиком `rows_upgraded_plaintext`, поэтому прогон с `OLD_KEY == NEW_KEY`
работает как разовая миграция значений §90.1. Документ настроек разбирается в
`map[string]any`, а не в `domain.AppSettings`: round-trip через типизированную структуру
молча выбросил бы незнакомые ей поля (есть тест). Логика вынесена из `package main` —
из `cmd/` её не покрыть тестами.

### 4.77 §90.2 — `Secure` нельзя было держать статическим флагом

Web слушает только plain HTTP (TLS терминирует внешний nginx), поэтому `c.Request.TLS`
за прокси всегда `nil`, а `X-Forwarded-Proto`, который DEPLOYMENT велит прокидывать,
до §90.2 не читался в коде **ни разу**. `Secure`-кука, отданная по `http://`, молча
отбрасывается браузером (RFC 6265bis §5.5), и это давало петлю: логин отвечает 200,
`GET /api/auth/me` получает 401, SPA возвращает на форму входа — без единой ошибки в логах,
поэтому симптом читался как «пароль не подходит».

Теперь `requestIsHTTPS` в
[auth_handler.go](../internal/web/adapter/in/http/auth_handler.go) считает схему на каждый
запрос, а флаг конфига означает «ставить `Secure`, когда канал позволяет». Заголовку можно
верить: атрибут только **сужает** круг соединений, по которым браузер отправит куку —
подделавший навредит лишь себе. Этим `X-Forwarded-Proto` принципиально отличается от
`X-Forwarded-For`, где доверие ограничено `web.trusted_proxies` (подделка искажает аудит).

Границы, которые это НЕ закрывает: `samesite=none` требует `Secure` по правилам браузеров,
поэтому с ним вход по HTTP невозможен в принципе (предупреждение при старте осталось); а
если nginx не шлёт `X-Forwarded-Proto`, кука уедет без `Secure` даже по HTTPS.

### 4.78 §90.3 — автозаполнение форм чинится атрибутами, а не состоянием React

Жалоба «в параметрах ClickHouse сам подставляется пользователь» выглядит как баг
контролируемой формы, но состояние тут ни при чём: Chrome эвристически распознаёт пару
«текстовое поле + `type="password"` рядом» как форму логина и заполняет её из менеджера
паролей. Отсутствие тега `<form>` не помогает — у form-less разметки эвристика самая
агрессивная, браузер группирует соседние поля синтетически.

Лечится `autocomplete="off"` на текстовых полях рядом с паролями и
`autocomplete="new-password"` на самих парольных. Приём в проекте уже был — в атоме
[SecretInput.tsx](../web-ui/src/components/ui/SecretInput.tsx), но панели настроек его не
получили. Форма входа и экраны смены пароля намеренно оставлены с
`username`/`current-password`.

Регрессионный тест проверяет именно **атрибуты** полей
([ClickHouse.test.tsx](../web-ui/src/pages/settings/ClickHouse.test.tsx)): поведение клика
здесь ничего не докажет — автозаполнение выполняет браузер, а не приложение (тот же урок,
что §79.3 про `<button>` вместо ссылки).

### 4.79 §91 — «мало строк в аудите» оказалось тремя разными дефектами

Жалоба звучала как одна проблема, но при разборе распалась на три независимые, и каждая
сама по себе делала журнал бесполезным.

1. **Пагинации не было вовсе.** Страница просила `limit: 200` одним запросом, `offset` не
   передавала никогда, а ответ шёл без общего числа записей. Из-за отсутствия счётчика
   обрезка НИЧЕМ не проявлялась: для пользователя 201-я запись просто не существовала.
   Это же объясняет, почему дефект дожил до бою — «мало записей» выглядит как свойство
   данных, а не как ошибка интерфейса.
2. **Половина событий не попадала ни в один режим просмотра.** Входы, неудачные логины,
   восстановление пароля и действия над пользователями пишутся с `team_id IS NULL`
   (§18.1), а выдача скоупится `team_id = $1`, то есть отсекает их всегда. Единственным
   способом их увидеть был ручной адрес `?team_id=*`.
3. **`team.switch` вытеснял содержательные события.** Записи создавались не только явным
   переключателем команд, но и переходами по ссылкам §76/§89.3 — то есть без осознанного
   действия пользователя.

Отдельная находка про `team.switch`: запись была ещё и **неработающей**. `actor`
вычисляется в handler'е ДО вызова `SwitchTeam`, поэтому строка уходила со старым
`team_id`; пользователь, переключившийся в команду B, искал своё переключение в журнале
команды B и не находил — оно лежало в журнале A. Ни один тест этого не ловил: на
`SwitchTeam`/`ActionTeamSwitch` тестов не было вовсе.

**Курсор обязан быть кортежем.** `created_at` не уникален (несколько действий в одну
секунду — норма), поэтому `ORDER BY created_at DESC` оставляет порядок среди равных на
усмотрение PostgreSQL, и на границе страницы записи то дублируются, то теряются.
Сортировка и курсор используют пару `(created_at, id)`; integration-тест специально
штампует половине записей ОДИНАКОВУЮ метку — на равномерных данных дефект не
воспроизводится.

**Счётчик обязан жить на общих условиях со списком.** `auditWhere()` собирает `WHERE`
один раз для `List` и `Count`. Разъехавшись, они дали бы «показано N из M», где M
посчитано по другому множеству — ровно та ошибка, которую §72.2 уже ловил на логах узла
(там счётчик считал сервером по всей таблице, а список фильтровался в браузере).

**Рефакторинг фронта проверяется чужими тестами.** Общая механика подгрузки вынесена в
[useInfiniteList](../web-ui/src/lib/useInfiniteList.ts), а `useInfiniteLogs` оставлен
обёрткой с прежним API. Это сознательный выбор в пользу неизменности `LogsTab`/`QueueTab`:
их 15 тестов не переписывались и служат регрессионной проверкой самого выноса.

### 4.80 §93 — что оказалось неочевидным в развёртывании двух реплик

**Web стоял на боевом пути, и это не было видно из названия.** «Web Service» читается как панель, но
`/api/v1/*` проходит через его реверс-прокси в Receiver (§17.1), а в боевом профиле наружу
опубликован только он. Отсюда два следствия: дублировать Receiver и Web без Sender'а бессмысленно
(обновление Sender'а всё равно рвёт sync через `Unavailable` по gRPC), и правильное место для
боевого трафика в новой схеме — сразу в Receiver, мимо Web.

**`static_configs` в Prometheus превращается из «нормально» в дефект.** С парой реплик за общим
сетевым алиасом Docker DNS отдаёт два адреса вперемешку, и один статический таргет скрейпил бы то
одну реплику, то другую под ОДНОЙ меткой instance. Два независимых ряда счётчиков схлопываются в
один, `increase()` на каждом переключении видит «счётчик уехал назад» и считает это рестартом —
панель §21 показывает правдоподобный мусор. Лечится перечислением реплик поимённо, а не DNS-SD:
имена дают стабильные метки, по которым видно, какая реплика отвечает.

**Дефолтный `pick_first` в gRPC выглядит как работающая балансировка.** Пул из восьми соединений к
`sender:9190` при `pick_first` целиком укладывается в первую отвечающую реплику: вторая простаивает,
и по метрикам самого Receiver'а это неотличимо от нормы — запросы идут, ошибок нет. Нужны
`round_robin` и `healthCheckConfig`, причём последний **молча игнорируется** без пустого импорта
`google.golang.org/grpc/health`: конфиг при этом выглядит правильным.

**`instance` §70 не различает реплики, и это ломает главный вопрос выката.** Идентификатор
принадлежит НОДЕ (своя пара PostgreSQL/Redis), у двух реплик одной ноды он одинаков. «Работает на
одной, падает на другой» без имени реплики неразличимо ни в консоли §51, ни в Sentry. Попутно
выяснилось, что `instance` до интерфейса не доезжал вовсе: `domain.ServiceLogEntry` не разбирал это
поле JSON, хотя в кольце Redis оно писалось с §70.7.

**Повтор POST на второй реплике — не отказоустойчивость, а дубль доставки.** Соблазн включить
`proxy_next_upstream ... non_idempotent` велик: он делает падение реплики невидимым. Но запрос,
который апстрим успел принять и начать обрабатывать, уйдёт во внешнюю систему дважды, а шина именно
для того и существует, чтобы этого не происходило. Поэтому повтор оставлен только для GET панели, а
боевой POST его не получает; мёртвая реплика уходит из ротации по `max_fails`, и цена — единицы 502
вместо дублей.

**`nginx -s reload` нужен не только ради состава upstream'а.** Имена в `upstream` резолвятся в
момент старта или reload, а пересозданный контейнер получает новый IP. Без reload после каждой
реплики nginx продолжал бы слать на адрес, которого больше нет, — и выкат «без простоя» кончался бы
502 на всю ротацию.

**`docker image prune -a` отменяет быстрый откат.** Команда удаляет образы, на которые не ссылается
ни один контейнер, — то есть ровно сохранённые `images.sh tag` версионные `nexus-*:<версия>`. Уборка
после выката обязана ограничиваться build cache и dangling-слоями, и в скрипте это закреплено
комментарием, а не только выбором команды.

**Двойная вычитка RabbitMQ не создаёт дублей, но меняет темп.** Брокер отдаёт сообщение одному
потребителю, поэтому «дублей не будет» — верно и успокаивает; при этом две реплики вычитывают
очередь вдвое быстрее настроенных `pull_batch_size` × `pull_interval_sec`, а порядок доставки
перемешивается сильнее. Узел настраивали под конкретный темп, поэтому узел арендуется одной
репликой (аренда с владельцем и продлением), а не делится пополам: деление уполовинило бы
пропускную способность при падении соседа и не починило бы порядок.

**Лок берётся в `Run`, а не в `RunOnce`.** `RunOnce` уборки ClickHouse экспортирован специально для
integration-тестов, которые прогоняют проход детерминированно. Затащив лок внутрь, мы потребовали бы
от них живой Redis ради задачи, которая его не касается.

### 4.81 §52-доп — почему «последний вызов» оказался плохим определением статуса

**Список логов и статус узла отвечают на разные вопросы.** Журнал отсортирован по времени НАЧАЛА
запроса, а статус ставил тот вызов, который ЗАВЕРШИЛСЯ последним. При параллельных запросах это
разные записи: в боевом случае 20.08 в одной секунде были два ответа 200 по ~35 мс и один 500 за
1493 мс — последним закончился именно он. Оператор видел «два последних успешных» и Down, и по
экрану это было необъяснимо.

**Порог обязан считать ПОДРЯД идущие отказы, а не долю.** Доля за окно потребовала бы читать метрики
(ClickHouse/Prometheus) на каждый показ бейджа и лишила бы его мгновенности; счётчик подряд идущих
неудач живёт там же, где статус, и стоит одну команду Redis. Успех обнуляет — иначе редкие ошибки за
сутки накопились бы в ложный down.

**4xx не считается отказом узла.** Это ответ приёмника, а не его отказ: поток клиентских ошибок не
должен «ронять» живой узел. Счётчик такие ответы не трогает вовсе.

**Инкремент — обязательно Lua.** С двумя репликами Sender (§93) запросы одного узла идут через обе,
и последовательность «прочитал → посчитал → записал» теряла бы отказы: узел не дошёл бы до порога
никогда. Проверено тестом, где два независимых writer'а бьют в один Redis.

**Эффективный исход возвращается вызывающему.** Prometheus-гаудж выставлялся раньше записи в Redis и
получал бы СЫРОЙ исход, а бейдж — уже сглаженный порогом. Алерт сработал бы на down, которого
интерфейс не показывает. Поэтому `SetLastOutcome` возвращает решение, и порядок в вызывающем коде
переставлен: сначала Redis, потом метрики.

**Цвет графика и высота столбца — разные смыслы, и их нельзя смешивать.** Спарклайн красился по
статусу узла, а высота означала трафик. Из-за этого узел со статусом down выглядел как полностью
нерабочий, хотя ошибок было 8.6 %. Теперь статус живёт в бейдже и полосе карточки, а столбец
показывает долю ошибок — как график на странице узла. Пустой `spark_err` означает «разбивки нет»
(Prometheus-fallback) и рисуется одноцветным, а не «ошибок ноль».

### 4.82 §93-доп — балансировщик не прозрачен по умолчанию

**Четыре дефолта nginx меняют запрос молча**, и прямой вход в Receiver ничего из этого не делал:
заголовки с подчёркиванием отбрасываются (`underscores_in_headers off`), заголовок больше 8 КБ даёт
400, `//` в пути схлопывается, а `Host $host` теряет порт.

Последнее опаснее всего и найдено только стендом: CSRF-проверка (§90.2) сравнивает host из `Origin`
с заголовком `Host`, поэтому на порту 8000 выходило «localhost» против «localhost:8000» — 403 на
любой мутирующий запрос, то есть панель за балансировщиком не работала бы вообще. Лечится
`$http_host` (заголовок клиента дословно).

**Прозрачность проверяется сверкой, а не чтением конфига.** Каждый случай выполнялся дважды — через
балансировщик и напрямую в реплику — и эхо-ответы сравнивались побайтно. Из этого же вышел урок про
тестовое окружение: единственное расхождение (юникод в пути) оказалось артефактом кодировки —
Windows-curl слал CP1251, curl из контейнера UTF-8. Сравнивать надо запросы из ОДНОГО окружения,
иначе ловишь различие своего инструмента, а не системы.

**`truncate` не работает без `table-fixed`.** У таблицы с авто-раскладкой браузер игнорирует
`max-width` ячейки и расширяет колонку под содержимое. Карточка «Последние запросы» жила именно так,
и длинный подпуть §39 в колонке «Метод» распирал таблицу за край карточки — при том что класс
обрезки на ячейке URL стоял и выглядел рабочим.

### 4.83 §94 — журнал отказов: агрегат вместо ленты, и почему запись не может быть надёжной

**Агрегат — не оптимизация, а условие существования.** Отказ на входе — это в том числе
сканер по случайным путям: сырая лента дала бы миллион строк за минуту и вытеснила бы
полезные записи. Ключ группы `(team_slug, node_path, reason, http_method)` схлопывает
поток в один `UPDATE … count = count + N` за интервал сброса. Ровно поэтому пределы
(50 клиентов и 20 сэмплов на группу, 2000 групп в буфере) зашиты в код, а не вынесены
в настройки: это защита от разрастания, и «покрутить» её означало бы разрешить себе
выстрелить в ногу.

**Обрезка полей обязана происходить ДО агрегации.** `RejectedGroupKey.Normalize()`
вызывается в `Collector.Add`, а не в момент записи: два длинных пути, различающихся
только хвостом, дали бы РАЗНЫЕ ключи в памяти и ОДИН после усечения в БД — UPSERT
сложил бы их вместе, и счётчики экземпляра разошлись бы с сохранёнными. Там же пустой
слог разворачивается в `default`: короткую форму §78.1 маршрутизация резолвит именно
так, и без нормализации один узел давал бы две группы в зависимости от формы адреса, а
join с `teams` команду не находил бы.

**Причину обязан называть обработчик.** Вывести её из HTTP-кода нельзя: 404 отдаётся и
когда узла нет, и когда узел есть, но это pull-узел (§27) или async-эндпоинт на sync-узле
(§82.3) — наружу они намеренно неотличимы. Поэтому `classifyDomainError` возвращает
`reason` тем же switch'ем, что код и текст: разъехавшись, они назвали бы отказ не тем,
чем он был. Резерв `RejectReasonForStatus` работает только там, где отказ вернул не
доменный слой (413 из чтения тела, 429 из лимита).

**Точка перехвата ровно одна.** Middleware стоит после `c.Next()` на всю группу
`/api/v1` и ПЕРВЫМ в цепочке — иначе его `c.Next()` не охватывал бы rate-limit, и 429 в
журнал не попадал бы. Разложить запись по местам возврата ошибок нельзя: 413 и 429 до
доменного слоя не доходят вовсе.

**Запись — best-effort, и это записано в контракте.** `Add` кладёт в канал и возвращается;
переполнение — дроп со счётчиком. Буфер очищается и после ошибки записи: удержание пачки
до починки базы означало бы рост памяти ровно тогда, когда с инфраструктурой и без того
плохо. Единственный признак неполноты — `nexus_ingress_reject_dropped_total`.
Собственный тест нашёл здесь дефект: при остановке терялись записи, УЖЕ принятые в
очередь (`Add` для них вернулся), — `Run` теперь дренирует канал перед финальным сбросом.

**Ревизия перед сдачей нашла вторую утечку кардинальности — в собственной новой метрике.** Первый
вариант `nexus_ingress_rejected_total` нёс метку `team` со слогом ИЗ АДРЕСА, то есть произвольной
строкой от клиента: сканер по `/api/v1/<random>/x` плодил бы ряды ровно так же, как это делал
`node`. Метка убрана; разрез по командам даёт журнал.

**Второй находкой ревизии стал неограниченный рост таблицы.** `max_groups` ограничивает только
буфер экземпляра между сбросами (2000 групп на 10 секунд — это 12 тысяч в минуту), а в БД группы
копились до конца срока хранения. Добавлен предел `RejectedMaxStoredGroups` (20 000): чистка
оставляет самые свежие по `last_seen` и пишет `warn`, когда предел достигнут.

**Отметка «просмотрено» ставится открытием карточки, а не кнопкой.** Смысл счётчика —
«появилось новое», а не «разобрано»: гасить его вторым действием после того, как человек уже всё
посмотрел, — лишняя работа. Отсюда три следствия в UI. Первое: отметка шлётся ровно один раз на
группу (`markedRef`), иначе перерисовка панели на каждом тике автообновления слала бы её заново.
Второе: строка НЕ выдёргивается из выдачи сразу — она гаснет локально через `setQueryData`, а
инвалидируется только сводка; полная инвалидация перестраивала бы таблицу прямо под курсором.
Третье: массовая отметка (`resolve-all`) намеренно игнорирует фильтры экрана — кнопка обещает
обнулить счётчик, а он считается по всей области видимости; пометить видимое и оставить бейдж
гореть было бы худшим исходом.

**Панель карточки не модальная.** Подложки нет, `z-40` (ниже модалок приложения): список под
панелью остаётся кликабельным, и переключение между отказами не требует закрывать текущий. Тест
проверяет отсутствие подложки, а не «выглядит похоже».

**Журнал отказов не дублирует лог узла — проверено, а не предположено.** Вопрос законный: 401, 405,
403, 400 относятся к СУЩЕСТВУЮЩИМ узлам, и кажется, что они должны быть и в логах узла. Их там нет:
лог узла пишет Sender по факту исходящего вызова, а все проверки (статус, метод, входящая
авторизация, разрешение адреса) стоят в Receiver до `callSender`. Стендовая проверка на узле с
включённым логированием: успешный вызов и НЕУДАЧНАЯ доставка (`done=0`, транспортная ошибка) в
CH-таблице есть, а шесть отказов подряд (401/405/413/429/508 и «узел выключен») не добавили в неё
ни одной строки — при том что все они попали в журнал §94. Таблица по всем причинам — в §94.2.1.

**Метка `node` при 404 больше не приходит от клиента.** Путь несуществующего узла задаёт
кто угодно, а метка входит в идентичность ряда Prometheus: сканер плодил ряд на каждую
попытку, и они остаются навсегда. Теперь там `<unresolved>` (`metrics.NodeUnresolved`), а
детализацию по путям даёт журнал. Подмена касается ТОЛЬКО `ErrNodeNotFound` — у §82.3
узел существует, и его путь в метке законен.

**Ноль в сроке хранения — это выключатель.** Одна ручка отвечает и за срок, и за
выключение: пара «тумблер + срок» неизбежно расходится (выключено, но срок 30 — что
показывать?). Поэтому `0` останавливает сбор в Receiver и вычищает накопленное в Web,
а интерфейс объясняет пустой экран отдельным текстом — иначе выключенный журнал читался
бы как «всё спокойно».

**Уменьшение срока применяется сразу.** Чистка ходит раз в час (страховка для реплики,
пропустившей событие), но подписчик `SectionGeneral` запускает внеочередной прогон сразу
после сохранения настройки. Секция `general` до §94 публиковалась, но НЕ имела
подписчиков и не входила в разворот `SectionAll` — то есть «применить все настройки» её
молча не трогало; это чинится здесь же.

**Лок взят только на ПЛАНОВЫЙ проход — и это главное в связке §93 + §94.** После слияния
с §93 часовой тикер идёт под `redislock` с ключом `web-rejected-housekeeping` (свой, не
общий с аудитом: задачи с разными периодами под одним ключом отменяли бы друг друга), а
внеочередной прогон по смене настройки лока не берёт ВОВСЕ. Соблазн накрыть локом оба
пути ломает §94.5: TTL часового цикла отложил бы применение уменьшенного срока до
следующего часа ровно на второй реплике. Цена решения названа явно — двойной `DELETE`,
он безвреден, потому что условие идемпотентно. Ошибка Redis трактуется как запрет, как и
у чистки аудита. Тесты: `TestRejectedHousekeepingRun_SkipsCycleWhenLockTaken`,
`TestRejectedHousekeepingCycleNow_IgnoresLock`,
`TestRejectedHousekeepingRun_LockErrorSkipsCycle`.

**Чужая группа отдаётся как 404, а не 403.** Иначе по коду ответа восстанавливалось бы,
какие адреса дёргают в других командах. По той же причине фильтры `team_slug` и
`unknown_team` действуют только у администратора: у остальных скоуп задаёт сессия.
Контракт порта закреплён тестом — `nil` в `TeamSlugs` означает «без ограничения», пустой
не-nil список — «ни одной команды»; перепутать их значит молча показать чужое.

**Подсказка молчит при точном совпадении пути.** Расстояние 0 — это не опечатка: узел с
таким путём существует, а отказ случился по другой причине (узел выключен, гонка с
созданием). Подсказка «похоже на этот узел» там вводила бы в заблуждение.

**Доступность «Пометить все» считается ДРУГИМ запросом, чем счётчики экрана.** Нашла
ревизия перед слиянием: кнопка гасилась по `summaryQ` (сводка с фильтрами экрана), а
действует `resolve-all` по всей области видимости. При узком периоде получалось
неразрешимое состояние — бейдж в сайдбаре горит (у него своё окно 24 ч), в таблице
непросмотренных нет, кнопка неактивна, и обнулить счётчик нечем, пока человек не
догадается расширить период. Поэтому на вкладке живёт второй запрос сводки —
`["rejected","summary","scope"]`, БЕЗ параметров: множество, которым меряется кнопка,
обязано совпадать с множеством, которое она меняет. Регрессионный тест —
«Пометить все» активна, когда непросмотренные лежат вне периода экрана»
([RejectedTab.test.tsx](../web-ui/src/pages/logs/RejectedTab.test.tsx)).

**Бейдж пункта «Логи» обновляется тремя путями, и ни один не является пушем.** Тикер
`refetchInterval: 60_000` в [Sidebar.tsx](../web-ui/src/components/Sidebar.tsx), рефетч по
возврату видимости вкладки (`refetchOnWindowFocus`, гейт `staleTime: 30_000`) и
инвалидация `["rejected","summary"]` после отметки — префикс накрывает и ключ сайдбара
`["rejected","summary","sidebar"]`, поэтому счётчик гаснет сразу, а не через минуту.
Замерено на стенде: новый отказ доходит до бейджа за `флаш коллектора (10 с) + остаток
до тика (0…60 с)`; при скрытой вкладке (`visibilityState === "hidden"`) тики
пропускаются целиком и возобновляются по возвращении. Окно бейджа — жёсткие 24 часа по
`last_seen`, поэтому оно НЕ совпадает с периодом вкладки: расхождение чисел на экране и
на пункте меню — норма, а не сбой.

### 4.84 §99 — группировка узлов: почему группировка не трогает сортировку

Раздел [99-node-groups.md](sections/99-node-groups.md). Ветка `feature/node-groups`,
миграция [0042](../migrations/0042_node_groups.up.sql). Ниже — только то, чего по коду
не видно.

| Пункт | Статус | Где |
|---|---|---|
| Миграция `node_groups` + `nodes.group_id` (FK, ON DELETE RESTRICT) | ✅ Phase 99.2 | [0042](../migrations/0042_node_groups.up.sql) |
| Домен `NodeGroup` + `Node.GroupID` + ошибки + audit-действия | ✅ Phase 99.2 | [domain/node_group.go](../internal/domain/node_group.go), [domain/node.go](../internal/domain/node.go) |
| Порт + PG-репозиторий (usage on-read, Reorder) + `group_id` в `NodeRepoPg` | ✅ Phase 99.3 | [port/node_group_repo.go](../internal/web/usecase/port/node_group_repo.go), [postgres/node_group_repo.go](../internal/web/adapter/out/postgres/node_group_repo.go) |
| Usecase: идемпотентный Create, свободное переименование, guard удаления, Move | ✅ Phase 99.4 | [usecase/node_group.go](../internal/web/usecase/node_group.go) |
| HTTP `/api/node-groups` (+`/move`), DTO узла, routes, swagger, i18n | ✅ Phase 99.5 | [http/node_group_handler.go](../internal/web/adapter/in/http/node_group_handler.go), [routes.go](../internal/web/adapter/in/http/routes.go) |
| Контракты копии и переноса узла | ✅ Phase 99.6 | [node_copy_test.go](../internal/web/usecase/node_copy_test.go), [node_move_test.go](../internal/web/usecase/node_move_test.go) |
| UI: комбобокс группы, поле формы, строка вкладки «Конфиг» | ✅ Phase 99.7 | [node/GroupField.tsx](../web-ui/src/components/node/GroupField.tsx), [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx), [node/ConfigTab.tsx](../web-ui/src/components/node/ConfigTab.tsx) |
| UI: «Настройки → Группы» (manager) | ✅ Phase 99.8 | [settings/NodeGroups.tsx](../web-ui/src/pages/settings/NodeGroups.tsx), [Settings.tsx](../web-ui/src/pages/Settings.tsx) |
| UI: секции на «Узлах», сворачивание, фильтр по группе | ✅ Phase 99.9 | [lib/nodeGroups.ts](../web-ui/src/lib/nodeGroups.ts), [Overview.tsx](../web-ui/src/pages/Overview.tsx), [lib/overviewFilters.ts](../web-ui/src/lib/overviewFilters.ts) |

- **Группировка — разбиение, а не сортировка, и это главное решение раздела.**
  `groupNodes` получает УЖЕ отсортированный массив и стабильно раскладывает его по
  секциям. Поэтому правило «проблемные первыми» (§22.5) само собой начинает действовать
  внутри группы, а заморозка порядка сквозного режима (§86.10) продолжает работать без
  единой правки. Альтернатива — сортировать «сначала по группе, потом по статусу» —
  потребовала бы продублировать ОБЕ ветки сортировки Overview: живую (режим одной
  команды, пересчёт на каждом ответе) и замороженную (карта рангов, ключ
  `scope|period|search|epoch`). Тест на подмене это подтверждает: плоская раскладка
  роняет 4 из 7 сценариев, и деградировавший узел уезжает в самый верх экрана.

- **Ссылка на группу идёт по `id`, а не по имени — отсюда расходятся все правила с §24.**
  Заголовки узел хранит строками в `forward_headers`, поэтому там переименование
  используемой записи запрещено (ссылки осиротели бы), а страница управления админская.
  У групп переименование безопасно, поэтому мутации отданы `manager`, а поле имени в
  диалоге не блокируется. Общее с §24 осталось одно: `usage_count` считается on-read,
  триггера нет.

- **Защита от удаления используемой группы трёхуровневая, и каждый уровень нужен.**
  Кнопка в UI (единственный уровень, который видит оператор), guard `usage_count > 0` в
  usecase (понятный ответ без похода в обработчик ошибок СУБД) и `ON DELETE RESTRICT`
  (единственный, кто выдерживает гонку: между чтением счётчика и удалением узел мог быть
  привязан из другой вкладки). Репозиторий переводит FK-нарушение 23503 в
  `ErrNodeGroupInUse`, а не в 500.

- **Сдвиг порядка переприсваивает ВЕСЬ порядок, а не меняет местами два `sort_order`.**
  У записей, созданных до появления стрелок, порядок одинаков (дефолт 0) и фактически
  определяется именем — обмен равных значений был бы no-op, и кнопка молча ничего бы не
  делала. `Reorder` присваивает `(позиция+1)*10` одним `UPDATE ... FROM (VALUES …)`:
  атомарно (частичный порядок оставил бы справочник в состоянии, которого оператор не
  заказывал) и с разрежённой шкалой, оставляющей место для ручной вставки.

- **Справочник глобальный, и это меняет три вещи сразу.** Он не скоупится командой (как
  §23/§24/§41/§95): одна прикладная группа встречается в разных командах, а в сквозном
  режиме §86 команднозависимый справочник дал бы на одном экране несколько одноимённых
  групп. Отсюда же: ключ `"node-groups"` лежит в `TEAM_INDEPENDENT_KEYS`
  ([lib/teams.ts](../web-ui/src/lib/teams.ts)), а перенос узла между командами группу не
  сбрасывает. И отсюда же расхождение счётчиков, которое выглядит багом, но им не
  является: в Настройках `usage_count` считает узлы ВСЕЙ инсталляции (включая чужие
  команды), а заголовок секции на «Узлах» — только видимые после фильтров.

- **Узел со ссылкой на исчезнувшую группу показывается в «без группы».** Состояние
  достижимо гонкой (справочник не приехал, группу удалили в соседней вкладке). Прятать
  из-за этого сам узел нельзя — узел важнее своей метки.

- **Секция в таблице — `<tbody>` внутри ОДНОЙ таблицы.** Отдельная таблица на группу
  считала бы ширину колонок независимо, и столбцы разъехались бы между секциями.
  `colSpan` заголовка зависит от режима: 7 колонок, 8 со «Командой» в сквозном режиме.

- **Заголовок секции — кнопка с `aria-expanded`, а не ссылка.** Урок §79.3 («ссылки
  обязаны быть ссылками») здесь работает в обратную сторону: заголовок ничего не
  открывает, он переключает состояние, и `<a>` дал бы Ctrl+клик в никуда.

- **Сворачивание живёт в `localStorage`, фильтры — в `sessionStorage`.** Разные
  хранилища у соседних состояний одного экрана — намеренно: свёрнутая группа это
  настройка рабочего места (обязана пережить перезапуск браузера, как режим
  Таблица/Карточки §7.3), а фильтр — состояние сиюминутной задачи (§54.3). Мусор в
  ключе даёт РАЗВЁРНУТЫЙ список: свёрнутый по ошибке читался бы как пропавшие узлы.

- **`CommandEmpty` из cmdk в комбобоксе группы не работает — и это не опечатка.** Он
  рендерится, только когда в списке нет НИ ОДНОГО пункта, а «— Без группы —» стоит там
  всегда. Подсказка «ничего не найдено» поэтому выводится обычным `div`. Нашлось тестом,
  который ждал подсказку и не дожидался.

- **Ревизия перед сдачей нашла четыре вещи, которых не видели ни тесты, ни типы.**
  (1) Сохранение узла с группой, удалённой в соседней вкладке, отдавало 500: FK-нарушение уезжало в
  `fmt.Errorf`, и по ответу «внутренняя ошибка» чинить форму было нечем. Теперь 23503 по
  `nodes_group_id_fkey` превращается в `ErrNodeGroupNotFound` и показывается у поля «Группа» через
  ту же таблицу `nodeValidationErrors`, что и остальные ошибки полей; имя ограничения задано в
  миграции ЯВНО, потому что у `nodes` три внешних ключа и голый код 23503 не говорит, на что
  сослались. (2) Селект фильтра врал: при удалённой группе браузер показывал первый пункт «Все
  группы», хотя фильтр действовал и список был пуст — добавлен пункт «Группа удалена». (3) `Move`
  читал справочник дефолтным лимитом 200, и при большем числе групп порядок переприсвоился бы
  частично — теперь явный потолок 1000 и отказ `409` вместо молчаливой перетасовки. (4) Мёртвый
  параметр `grouped` в `NodeTable` (обходился `void grouped;`) убран.

- **Гейт покрытия integration-тестов (§88) поймал новые тесты.** Четыре теста
  `TestNodeGroup_Repo_*` не попадали ни в один под-прогон `-run` Makefile и в CI просто не
  запускались бы. Добавлены в `test-int-catalog` — там же, где `TestHeaderCatalog`.

- **jsdom не реализует `scrollIntoView`, а cmdk его зовёт.** Заглушка добавлена в общий
  [test/setup.ts](../web-ui/src/test/setup.ts) рядом с `ResizeObserver` — без неё падает
  любой тест, открывающий комбобокс справочника (§24 и §99 одинаково).
