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
| Главный поток           | `POST /v1/request/<team_slug>/{path}` → Receiver → gRPC Sender → внешний URL → лог в ClickHouse `nexus_<team_slug>.<table>` |
| Async                   | `POST /v1/requestAsync/<team_slug>/{path}` → Receiver → Kafka → Sender-consumer |
| Multi-tenancy           | Phase 10 ✅: команды через `/api/teams`, своя CH-БД per team (`nexus_<slug>`), team-switcher в Topbar, scope в nodes/audit/logs/replay/api_tokens. Legacy URL без слога продолжает работать как default-team. |
| Зависимости              | PostgreSQL 16, Redis 7, ClickHouse 24, Kafka 3.9 (KRaft), Prometheus  |
| Покрытие unit-тестами   | 14 пакетов (domain, crypto, i18n, sentry, receiver/usecase, chlog, web/usecase, metrics, healthcheck, config, clickhouse, reloader, nodecache, sender/usecase, **+ build / httpclient в Phase 7.13, + receiver/http + web/http middlewares в Phase 7.14**) + integration: circuitbreaker (Phase 7.13) |
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
| **`/v1/callback/*` (webhook с HMAC-SHA256, §16)** | ✅ Phase 8.1 | [handler.go](../internal/receiver/adapter/in/http/handler.go) `handleCallback`, [webhook_signature.go](../internal/receiver/usecase/webhook_signature.go) `VerifyWebhookSignature`, миграция [0007](../migrations/0007_webhook_signature.up.sql) |
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
| `team_id` колонки с DEFAULT 'default' (закладка multi-tenancy v2) | ✅ → ◐ Phase 10.1 | миграция 0002 (legacy) → миграция 0008 (UUID FK на `teams`, см. §16 Phase 10) |
| **`teams`, `user_teams` + FK во всех team-aware таблицах** | ✅ Phase 10.1 | [migrations/0008_multi_tenancy.up.sql](../migrations/0008_multi_tenancy.up.sql); сидинг 'default'-team (`ch_database='nexus_default'`), admin → owner |
| **TeamProvisioner: `CREATE DATABASE nexus_<slug>` атомарно с PG-tx** | ✅ Phase 10.C.1 | [adapter/out/clickhouse/team_provisioner.go](../internal/web/adapter/out/clickhouse/team_provisioner.go), [usecase/team.go](../internal/web/usecase/team.go); при упавшем CH `repo.Delete` откатывает PG-row; имя БД жёстко валидируется regex'ом |
| **Нормализация `nodes.clickhouse_table` → `<team.ch_database>.<table>`** | ✅ Phase 10.C.2 | [usecase/node.go](../internal/web/usecase/node.go) `normalizeCHTable` — write-time префикс при Create/Update; unprefixed `<x>` → `nexus_default.<x>`. Backfill-миграция (0009) удалена как ненужная (стенд greenfield) |
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
| **`nexus_requests_total` + `nexus_request_duration_seconds`** | ✅ Phase 6.1 | Gin middleware [platform/metrics/gin.go](../internal/platform/metrics/gin.go) — Receiver/Web; gRPC [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go) и async [usecase/async.go](../internal/sender/usecase/async.go) — Sender |
| **`nexus_kafka_lag`** | ✅ Phase 6.1 | reporter в [sender/app.go](../internal/sender/app.go) `reportKafkaLag()` — раз в 15 сек снимает `Stats()` со всех consumer-инстансов |
| **`nexus_clickhouse_buffer_size` / `_errors_total` / `_dropped_total` / `_fallback_total`** | ✅ Phase 6.1 | [chlog/writer.go](../internal/sender/adapter/out/chlog/writer.go) обновляет в `append`/`flushTable`/`Write` |
| **HTTP-API метрик для панели (§21)** | ✅ Phase 21.1 | Web опрашивает Prometheus query API: [adapter/out/prometheus/client.go](../internal/web/adapter/out/prometheus/client.go) (глобальные KPI/очередь/throughput) + per-node агрегаты из ClickHouse [adapter/out/clickhouse/metrics_reader.go](../internal/web/adapter/out/clickhouse/metrics_reader.go) (точные p95/p99). Usecase [usecase/metrics.go](../internal/web/usecase/metrics.go), порт [port/metrics_provider.go](../internal/web/usecase/port/metrics_provider.go), handler [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go): `GET /api/metrics/overview`, `/api/metrics/nodes`, `/api/metrics/nodes/{id}`. Конфиг `prometheus.url` ([config.go](../internal/platform/config/config.go)); деградация при отсутствии Prometheus. |

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
| **Sentry tracing-middleware для Gin (§14.3)** | ✅ Phase 5 | [platform/sentry/middleware.go](../internal/platform/sentry/middleware.go) — span'ы с тегами service/node/root_method |
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
  - ✅ Phase 11.B: перенос узла между командами — `POST /api/nodes/:id/move {target_team_slug}` (admin-only). `NodeUsecase.Move`: PG-перенос (team_id + clickhouse_table rebase на БД целевой команды) в UoW-транзакции + audit `node.move`; конфликт пути → `ErrNodeAlreadyExists` (409); перенос в свою команду → 403; чужой узел → 404. CH-таблица логов следует за узлом через `TeamProvisioner.RenameTable` (RENAME TABLE old_db.tbl TO new_db.tbl, best-effort: при отсутствии исходной таблицы — `ErrSourceTableAbsent`, пропуск). UI: кнопка «Move» на Overview + диалог выбора команды. Integration-тест `TestMultiTenancy_NodeMove_E2E`.
  - ✅ Phase 11.C: Redis ACL — `RedisSection.Username` (yaml `username`), проброшен в `goredis.Options.Username`. `config.example.yml`: `username: ${REDIS_USER:}`; `.env.example`: `REDIS_USER=`. Пустое значение = default-юзер (обратная совместимость). `config_debug.yml` не трогается (для локального ACL-Redis добавить `username: <user>` вручную).
  - ✅ Phase 11.D: multi-tenancy зафиксирована как ТЗ §18 — новый раздел [sections/18-multi-tenancy.md](sections/18-multi-tenancy.md) (модель данных, CH-БД per team, scope, Receiver URL, перенос узла, UI), пункт в `16-out-of-scope.md` помечен реализованным со ссылкой на §18, строка в `sections/README.md`, синхронизирован сводный `nexus_spec.md`. В `CLAUDE.md` — правило: новая крупная фича → новый раздел в `specs/sections/`.
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
| Отключение логирования + обрезка по символам в Sender | ✅ Phase 22.2 | [send.go](../internal/sender/usecase/send.go) (`truncateRunes`, guard на `Write`); тесты [send_test.go](../internal/sender/usecase/send_test.go) + integration [clickhouse_test.go](../tests/integration/clickhouse_test.go) |
| UI формы: Toggle, карточки «Заголовки» / «Логирование» | ✅ Phase 22.3 | [NodeSettings.tsx](../web-ui/src/pages/NodeSettings.tsx) (две карточки, мастер-тумблер гасит `<fieldset disabled>`), компонент [Toggle](../web-ui/src/components/ui/pickers.tsx), i18n ru/en |
| Telegram-алерты через Prometheus + метрика `nexus_request_incomplete_total` | ✅ Phase 22.4 | [notification.go](../internal/web/usecase/notification.go) (`PromMetrics.NodeErrors` вместо `LogReader.CountErrors`), [metrics.go](../internal/platform/metrics/metrics.go), инкремент в [sender_service.go](../internal/sender/adapter/in/grpc/sender_service.go)/[async.go](../internal/sender/usecase/async.go), wiring [app.go](../internal/web/app.go) (требует Prometheus) |
| Карточки Overview под `ui_cards.html` (спарклайн, p95, фильтр) | ✅ Phase 22.5 | [Overview.tsx](../web-ui/src/pages/Overview.tsx) (полоса-акцент, chip+pill, 3 метрики, спарклайн, target, фильтр статусов, сортировка); backend [prometheus/client.go](../internal/web/adapter/out/prometheus/client.go) (`NodeSeries` range-запрос + p95 в `NodeThroughput`), [metrics.go](../internal/web/usecase/metrics.go), DTO [metrics_handler.go](../internal/web/adapter/in/http/metrics_handler.go) |

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

### §25 Swagger в шапке (Topbar) + два дока

ТЗ — [sections/25-topbar-swagger.md](sections/25-topbar-swagger.md).

| Пункт | Статус | Где |
|---|---|---|
| Receiver swagger-аннотации + генерация двух доков (`--exclude`) | ✅ Phase 25.C.1 | [cmd/receiver/main.go](../cmd/receiver/main.go), [receiver/.../handler.go](../internal/receiver/adapter/in/http/handler.go), [Makefile](../Makefile), `docs/receiver` |
| Web раздаёт `/swagger/web` + `/swagger/receiver` (InstanceName) | ✅ Phase 25.C.2 | [app.go](../internal/web/app.go) |
| FE-инфра Radix/cmdk + обёртки Popover/Tooltip/Command | ✅ Phase D.1 | [components/ui/](../web-ui/src/components/ui/) |
| Topbar Swagger popover (две доки, ↗, tooltip) | ✅ Phase 25.D.2 | [components/Topbar.tsx](../web-ui/src/components/Topbar.tsx), [AppShell.tsx](../web-ui/src/components/AppShell.tsx) |

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

### 4.11.2 Metrics API панели — гибрид Prometheus + ClickHouse, с деградацией (Phase 21.1)

Дашборды панели (§21) не считают агрегаты на лету в Go, а тянут готовые из двух
источников по принципу «каждому показателю — лучший источник»:

- **Prometheus** ([adapter/out/prometheus/client.go](../internal/web/adapter/out/prometheus/client.go))
  — глобальные KPI Overview (incoming = `sum(increase(nexus_requests_total{service="receiver"}[24h]))`,
  outgoing = то же для `service="sender"`, errors = `…,status=~"0|[45].."`), очередь Kafka
  (`sum(nexus_kafka_lag)`) и per-node throughput (`sum by (node)(…)`). Это **query API**
  сервера Prometheus (`prometheus.url`), а не scrape-эндпоинт `/metrics`. Метка `node`
  совпадает с `domain.Node.Path` — поэтому per-node merge на фронте идёт по path.
- **ClickHouse** ([adapter/out/clickhouse/metrics_reader.go](../internal/web/adapter/out/clickhouse/metrics_reader.go))
  — per-node KPI и ряд графика на странице узла: точные `quantile(0.95/0.99)(duration)`,
  count/delivered/errors и time-bucket-ряд по таблице логов узла. Точные перцентили из CH,
  а не из histogram-бакетов Prometheus.

Почему так: outgoing/delivered/перцентили достоверно есть только в CH-логах (их пишет
Sender), а очередь Kafka и кросс-сервисный throughput — только в Prometheus. Оба источника
**опциональны**: при пустом `prometheus.url` провайдер не создаётся (nil), `MetricsUsecase`
отдаёт нули с `prometheus_available=false`; узел без `clickhouse_table` → `chart_available=false`.
Это сделано намеренно, чтобы поллинг UI не спамил 500-ками, когда Prometheus не настроен
(см. [usecase/metrics.go](../internal/web/usecase/metrics.go)).

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

### 4.23 §24 — headers_catalog: usage_count on-read, без M2M

- **Отдельной таблицы привязки нет.** Источник — `nodes.forward_headers TEXT[]` (его читает Receiver).
  `usage_count` считается коррелированным подзапросом (`unnest(forward_headers)` + `lower()`), а не
  trigger'ом на массив (тот хрупок). Форма узла шлёт имена (`string[]`), не ID — Receiver нетронут.
- **POST идемпотентен по `lower(name)`**: unique-violation ловится в usecase и резолвится в
  существующую запись (200). Combobox создаёт без диалогов и без гонок.

### 4.24 §25 — два swagger одним Web-бинарём

- **Изоляция генерации через `--exclude`.** swag сканирует всё дерево от searchDir; без `--exclude`
  web-док подхватил бы `/v1`-маршруты Receiver, а receiver-док — web-handler'ы с cross-package типами
  (`usecase.TestResult` → ошибка). Web исключает `cmd/receiver,internal/receiver`, Receiver —
  `cmd/web,internal/web`.
- **Разные `InstanceName`.** Web-док — default `swagger`, Receiver — `receiver` (`swag --instanceName`).
  Web раздаёт оба через `ginswagger.WrapHandler(..., InstanceName(...))`; blank-импорты обоих
  `docs/*`-пакетов регистрируют их в `swag.Registry`. Receiver — отдельный процесс, swagger UI к нему
  не подключён (мокап §25 это и предписывает).

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

10. **LogReader.GetByID детерминирован через `ORDER BY date_request DESC LIMIT 1`** (Phase 9.1).
    ClickHouse не enforce'ит PRIMARY KEY uniqueness в MergeTree — два INSERT'а с одним ID
    создают две строки (например, file-fallback restore после восстановления CH, или
    повторный INSERT при batch-retry). UUID v4-коллизий нет, но дубликаты по бизнес-логике
    возможны. Поэтому `GetByID` возвращает самую свежую запись детерминированно.
    Integration-тест `TestClickHouse_GetByID_Deterministic`
    ([tests/integration/clickhouse_test.go](../tests/integration/clickhouse_test.go))
    проверяет контракт «два INSERT'а с одним ID → видим новейший».

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
