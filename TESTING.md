# TESTING.md — процедура запуска тестов Nexus

> **Ручное сквозное тестирование на стенде** (поднять весь стек, создать узлы
> всех типов, прогнать по 500 запросов, проверить UI/логи/метрики) — отдельная
> пошаговая инструкция со скриптами: [docs/STAND_TESTING.md](docs/STAND_TESTING.md).

## Unit-тесты

```bash
make test           # go test -race -short ./...
make test-coverage  # покрытие в ./coverage.html
```

Покрытые на текущий момент:

- `internal/platform/crypto` — AES-256-GCM round-trip, валидация ключа, детектирование подделки, формат.
- `internal/platform/i18n` — парсер Accept-Language (en/ru-RU/quality), Translate с fallback на en.
- `internal/platform/sentry` — middleware mapping HTTP-status → SpanStatus, no-op без `sentry.Init`, теги `service`/`node`/`root_method`.
- `internal/domain` — `Node.SetDefaults` / `Validate`, `AuthType.IsDynamic`.
- `internal/receiver/usecase` — `ResolveURL` (static / from_request / wildcard allowlist),
  `CheckIncomingAuth` (none/basic/token), `BuildOutgoingAuth` (static),
  `BuildDynamicOutgoingAuth` (token_from_request: query/header/body, basic_from_request),
  `Route` (paused → ErrNodePaused, disabled → ErrNodeNotFound), `RouteAsync` (paused → Queued=true).
- `internal/web/usecase` — `NodeUsecase` (через DryRunUsecase), `DryRunUsecase` (static-none happy path,
  from_request без url_base, mask Bearer ***), `ReplayUsecase` (happy path с маркером `__replay_of`,
  disabled, too-old, rate-limit, body-override), `LogsUsecase` (ListSince, отсутствие CH-таблицы,
  ErrNodeNotFound, Subscribe закрывается на ctx-cancel).
- `internal/sender/adapter/out/chlog` — `fallbackStore` (save → restore удаляет файл; failure keeps file;
  disabled при пустом dir; naming format `ch-…-….ndjson`).

Минимальная планка покрытия по §10.1 ТЗ — 70% для `/internal/{receiver,sender,web}`. На текущем этапе
для usecase-слоёв план приближается к 70%; полный отчёт — `make test-coverage`.

## Integration-тесты (testcontainers)

Требуют запущенного Docker daemon. Запуск:

```bash
make test-integration
# или прямо:
go test -tags=integration -count=1 -v ./tests/integration/...
```

`make test-integration` гоняет пакет **под-прогонами по группам зависимостей**
(§10.1): `test-int-pg`, `test-int-ch`, `test-int-catalog`, `test-int-receiver`,
`test-int-rmq`, `test-int-sender` — каждая со своим `-timeout` (`INTEGRATION_TIMEOUT`,
дефолт 20m). Это не даёт одному зависшему/упавшему тесту съесть бюджет всего
пакета (`go test -timeout` общий на пакет) и замаскировать остальные группы.
Группы выполняются последовательно; Kafka-группа (`test-int-sender`) идёт
последней. Отдельную группу можно запустить точечно: `make test-int-rmq`.

Покрытые сценарии:

- **`TestNodeRepoCreate_E2E`** — реальный Postgres через `tcpg.Run`, миграции из `/migrations`,
  Node CRUD через `UnitOfWorkPg` с проверкой того, что запись узла и `user_audit`-запись лежат в БД
  одной транзакцией.
- **`TestReceiver_Sync_E2E`** — Postgres + `httptest` mock внешнего узла + inline-stub Sender, который
  делает реальный HTTP-запрос вместо gRPC. Проверяет, что запрос дошёл до upstream с правильным path,
  body, и заголовком `Authorization: Bearer ...`.
- **`TestNodeRepoRabbitMQAsync_E2E`** (§27) — Postgres: round-trip узла RabbitMQAsync через
  `NodeUsecase`, шифрование `rmq_password`, сброс несовместимых полей, срабатывание `chk_rmq_fields`.
- **`TestRMQPuller_E2E_NoLoss` / `…_KafkaDown_Requeue` / `…_DegradedOnMissingQueue`** (§27.12) —
  реальный RabbitMQ через `tcrabbit.Run`: (1) публикуем N сообщений → `PullerWorker` забирает все,
  складывает в fake-producer (Kafka-путь покрыт отдельно), очередь дренируется без потерь; envelope
  содержит блок `rmq` и `IP=rabbitmq://…`; (2) при «упавшей» Kafka сообщения возвращаются в очередь
  (`nack requeue`), потерь нет; (3) если очереди узла не существует (passive-declare 404), воркер
  после `SetDegradeAfter` помечает узел `degraded` (health-снимок с `connection_state=down` и причиной).
- **`TestSender_Async_E2E`** — Postgres + Kafka (KRaft) + mock upstream. Receiver-RouteAsync публикует
  Envelope в `nexus.async`, Sender ConsumerGroup читает, делает HTTP-вызов, пишет в capturing log
  writer. Покрывает §3.6 / §4.2 happy-path.
- **`TestSender_Async_DLQ_E2E`** — тот же стэк, но mock всегда отвечает 500, узел с `retry_count=2`.
  Отдельный kafka-reader на `nexus.async.dlq` дожидается публикации и проверяет headers
  (`id` / `node_path` / `orig_topic` / `reason=status=500 attempts=3` / `last_attempt_at`).
  Покрывает §3.6 / §5.3 (DLQ after retry exhaustion).
- **`TestClickHouse_WriteAndRead`** + **`TestClickHouse_GetByID_Deterministic`** — ClickHouse 24-alpine,
  `chlog.Writer` пишет батч; `LogReaderCH` через `Search` фильтрует по `status` / `IP` / `Q` / `Done`.
  Второй тест гарантирует, что при двух записях с одним ID `GetByID` возвращает свежую по `date_request`.
- **`TestReplay_E2E_ClickHouse`** — Postgres + ClickHouse. Узел создан через `NodeUsecase`, в CH
  записывается «оригинальный» лог, далее `ReplayUsecase` через capturing-dispatcher проверяет, что в
  запрос проброшен маркер `__replay_of=<orig_id>`, оригинальные query-параметры сохранены, тело
  совпадает с оригиналом, audit-запись `node.replay` появляется в PG. Покрывает §7.4.1.
- **`TestSessionRepo_E2E`** / **`TestNodeCache_E2E`** / **`TestSession_TTLExpires`** — Redis 7,
  CRUD сессий и горячий кеш узлов (§5.4 / §7.1 / §9.2).
- **`TestCircuitBreaker_*`** — Redis 7, поведение CB: closed → open по threshold, переход в half_open
  по cooldown, изоляция ключей, recovery через `RecordSuccess`. Покрывает §9.5.
- **`TestAuth_Login_E2E`** — Postgres + Redis. `AuthUsecase.Login` с `UserRepoPg`+`SessionRepoRedis`,
  проверка `ErrUnauthorized` / `ErrUserInactive`, `Check` продлевает TTL, `ChangePassword`
  инвалидирует ВСЕ активные сессии пользователя (forced re-login §7.1), audit пишет
  `user.login.success` / `user.login.failed` / `user.password.change` с правильным `reason`
  в Details.
- **`TestReceiver_IncomingAuth_E2E`** — Postgres + полный Receiver HTTP-стек (Gin, `httptest.Server`).
  Три узла с `incoming_auth_type` = none / basic / token; реальные HTTP-запросы с правильными и
  ошибочными `Authorization`-заголовками проверяют, что Receiver отдаёт 200 / 401 и что 401-запросы
  до upstream не доходят. Покрывает §3.3.

## Нагрузочный тест (`make loadtest`)

Бинарь `cmd/loadtest` — §10.2 ТЗ. Поднимает встроенный mock-сервер, логинится в Web, создаёт N узлов,
гонит `target_rps` в течение `duration` через Receiver, считает p50/p95/p99/error_rate, сохраняет JSON-отчёт.

```bash
# Простой smoke (1 минута на 50 rps)
make loadtest ADMIN_PASSWORD=admin TARGET_RPS=50 DURATION=1m NODES=5

# Полный нагрузочный тест перед релизом (10 минут на 500 rps)
make loadtest ADMIN_PASSWORD=admin TARGET_RPS=500 DURATION=10m NODES=50
```

Прямой запуск со всеми флагами:

```bash
go run ./cmd/loadtest \
  --web http://localhost:8000 --receiver http://localhost:8080 \
  --admin-password ... \
  --target-rps 500 --duration 10m --nodes 50 \
  --payload-min 100 --payload-max 5120 \
  --mock-latency 50ms --mock-latency-jitter 30ms --report report.json
```

### Микс трафика (§10.2)

Узлы — **односценарные корзины**: каждый упражняет ровно одну фичу, что даёт чистую per-mode
статистику и понятные testcase'ы. Доли — независимые корзины (сумма ≤ 1, остаток — plain `sync`):

| Флаг                  | Дефолт | Режим узла  | Что упражняет                                                |
|-----------------------|--------|-------------|--------------------------------------------------------------|
| `--ratio-async`       | `0`    | `async`     | `root_method=requestAsync` → Kafka-путь, `/api/v1/requestAsync/...` |
| `--ratio-dynamic-url` | `0`    | `dyn-url`   | `url_mode=from_request`, target в `?url_base=` (allowlist пуст = allow-all) |
| `--ratio-auth-token`  | `0`    | `auth-token`| `auth_type=token_from_request`, заголовок `Authorization: Bearer …` |
| `--ratio-auth-basic`  | `0`    | `auth-basic`| `auth_type=basic_from_request`, заголовок `Authorization: Basic …`  |
| `--random-headers`    | `true` | (все)       | 1–3 случайных `X-Lt-*` заголовка на запрос                    |

Дефолт 0 для всех долей сохраняет старое поведение (`make loadtest` без переменных = чистый sync-smoke).
В отчёт (`report.json`) добавлен блок `modes` — sent/errors/error_rate/p50/p95/p99 на каждый режим.

### No-loss проверка async/rmq через ClickHouse (§10.2)

При заданном `--ch-addr` loadtest после прогона ждёт `--ch-flush-grace` (батч-флаш sender'а в CH) и
сверяет число строк `type IN ('requestAsync','RabbitMQAsync')` в `--ch-table` с числом отправленных
async + rmq запросов. Потеря (`ch_rows < expected`) → exit 1. Дубликаты at-least-once
(`ch_rows > expected`) нарушением не считаются. Пустой `--ch-addr` пропускает проверку.

```bash
go run ./cmd/loadtest --admin-password ... \
  --target-rps 200 --duration 2m --nodes 20 \
  --ratio-async 0.5 --ch-addr localhost:9000 --ch-flush-grace 10s
```

### Критерии pass/fail

Loadtest exit'ится с кодом 1, если:

- `achieved_rps < 95%` от `target_rps`;
- `p95 > 200 ms` (без учёта mock-задержки);
- `error_rate >= 0.1%`.

Это и есть критерий §10.2 для CI.

### RabbitMQAsync-нагрузка (§27.12)

Флаг `--ratio-rmq` (0..1) + `--rmq-url` поднимают долю узлов как `RabbitMQAsync`: loadtest объявляет
их очереди и публикует payload'ы прямо в RabbitMQ (а не HTTP в Receiver). Пример:

```bash
go run ./cmd/loadtest \
  --admin-password=admin --target-rps=200 --duration=2m --nodes=20 \
  --ratio-rmq=0.3 --rmq-url=amqp://guest:guest@localhost:5672/
```

В отчёт печатается `rmq_published=N`. Критерий «нет потерь» = `N` ≤ числу строк в ClickHouse-логе
узлов (`type=RabbitMQAsync`); сверка вручную/скриптом (loadtest не ходит в ClickHouse).

### Что не покрыто текущим loadtest

- Kafka consumer-lag в отчёте (§10.2 — опциональная диагностика, не acceptance-критерий;
  «нет потерь» закрыто сверкой по числу строк CH). TODO.
- Внутренний размер CH-буфера sender'а — извне ненаблюдаем; заменён фактической no-loss
  сверкой числа строк.

### Прогон на стенде: очередь, размер, retention (2026-07)

Стендовый прогон 100% async (`--ratio-async 1.0`) на стеке `services` (Kafka 3.x, 1 партиция,
`instances=1`) — замеры Kafka через `kafka-consumer-groups`/`kafka-log-dirs`/`DumpLogSegments`:

- **Приём (Receiver→Kafka, acks=all):** держит 200 rps, latency p50 6.6 / p95 8.0 мс.
- **Обработка (Sender consumer):** **~43 сообщения/с** при 1 партиции + `instances=1` (полный путь
  с внешним вызовом). Узкое место: одну партицию в группе читает максимум один consumer — параллелизм
  ограничен **числом партиций**, а не `instances`. Для масштаба увеличивать партиции топика.
- **Очередь:** при 200 rps > 43/с lag растёт ~159/с (0→14 137 за 90 с) и дренируется после спада
  нагрузки; при 30 rps < 43/с lag стабильно 0–1 (устойчивый режим). Потерь на уровне шины нет
  (at-least-once + DLQ): sent = обработанные + DLQ, offset-дельта = sent.
- **Размер на диске:** ~312 B/сообщение (lz4, сжимаемое тестовое тело — фактически overhead
  envelope; реальные тела крупнее). ClickHouse-лог (архив): ~69 B/строка сжато (без тел запросов).
- **End-to-end обработка (устойчивый режим, из CH):** p50 21 / p95 29 мс.

**Настройка retention топика (`config.yml` → `kafka.topic`, применяется ко всем `nexus.*`):**

| Параметр | Значение | Смысл |
|----------|----------|-------|
| `retention_ms` | `604800000` (**7 дней**) | `nexus.async` — транзитная очередь (норма lag≈0, сообщение живёт секунды); 7 дней — запас на простой/отставание Sender'а. Долговременное хранение запросов/ответов — в ClickHouse, не в Kafka. |
| `retention_bytes` | `53687091200` (**50 ГиБ / партицию**) | Предохранитель диска: Kafka чистит старые сегменты при превышении, даже если младше `retention_ms`. **Пер-партиция** — суммарно по топику = 50 ГиБ × `partitions`. Держать заметно выше пикового backlog, иначе при отставании удалятся непрочитанные (потеря); мониторить lag. |

> **Грабли:** `kafka.topic.*` из `config.yml` применяется **только при создании топика**
> (`AlterConfigs` в коде нет). Смена retention на живом топике — через `kafka-configs --alter`, см.
> [DEPLOYMENT.md §4.3](DEPLOYMENT.md).

## Swagger

```bash
make swagger              # генерирует docs/web/ из аннотаций cmd/web/main.go
make swagger-drift-check  # CI-проверка: фейлит если docs/ устарела
```

Аннотации висят на ключевых handlers: login, nodes (List/Get/Create), dry-run, replay, logs (List/Stream).
Расширение остальных endpoints — Phase 6.

## CI/CD

GitLab CI — [.gitlab-ci.yml](.gitlab-ci.yml), Phase 9.2. Стэйджи в одном
pipeline: `test → lint → build → security → integration → release`. Все
jobs на runner с тегом `srv-d-android-l-docker`.

### Что когда запускается автоматически

| Триггер                                    | Что катится                                                          |
|--------------------------------------------|----------------------------------------------------------------------|
| push в любую ветку                         | `go-test`, `go-lint`, `swagger-drift`, `go-build`, `ui-build`        |
| push в master                              | + `integration` + security stage + `loadtest` (smoke)                |
| push в dev                                 | + `integration` + security stage (govulncheck — gate)                |
| MR с label `run-integration`               | + `integration`                                                      |
| MR с изменениями go.mod/go.sum/Dockerfile  | + security stage (govulncheck/gosec/trivy)                           |
| schedule (CI/CD → Schedules, weekly)       | security stage + `renovate`                                          |
| tag `v[0-9]…`                              | + `integration` + security stage + `loadtest` (smoke, как на master) |

`loadtest` на push в master — это **smoke-test инфраструктуры** (стек
поднимается, ноды создаются, end-to-end запросы проходят). Помечен
`allow_failure: true` — не блокирует merge при кратковременных просадках
RPS из-за загрузки runner-host'а. Полный capacity-bench (§10.2 ТЗ:
500 RPS / 5 мин) надо гонять отдельно через `RUN_PROFILE=loadtest-only`
с переопределением переменных.

### Селективный ручной запуск (UI → Run pipeline)

В GitLab UI: **Build → Pipelines → Run pipeline → выбрать ветку → ввести
переменную `RUN_PROFILE` со значением `integration-only` или `loadtest-only`
→ Run pipeline**. Остальные jobs стэйджей `test/lint/build/security/release`
скипаются.

| `RUN_PROFILE`      | Что выполнится                                | Доп. переменные                |
|--------------------|-----------------------------------------------|--------------------------------|
| (не задана)        | обычный pipeline по триггерам выше            | —                              |
| `integration-only` | только `integration` job (testcontainers)     | —                              |
| `loadtest-only`    | только `loadtest` job (compose-стек + нагр.)  | см. таблицу loadtest-vars ниже |

**Переменные для `loadtest-only`** (Settings → CI/CD → Variables и/или
поле Variables в Run pipeline):

| Переменная                | Обяз. | Default | Назначение                                                                    |
|---------------------------|-------|---------|-------------------------------------------------------------------------------|
| `LOADTEST_ADMIN_PASSWORD` | да    | —       | пароль admin'а (masked + protected). Падает в `before_script:` если не задана |
| `LOADTEST_TARGET_RPS`     | нет   | `300`   | целевой RPS (CI smoke-mode — см. ниже)                                        |
| `LOADTEST_DURATION`       | нет   | `2m`    | длительность нагрузки (формат `time.Duration`)                                |
| `LOADTEST_NODES`          | нет   | `50`    | число узлов, которые loadtest создаст через Web API                           |

Дефолты RPS=300 / 2m — это **CI smoke-mode**, не полный capacity-тест. На
self-hosted runner'е (общий docker daemon + параллельные builds на одной
машине) реалистично 300-400 RPS. Полный bench §10.2 ТЗ (500 RPS / 5 мин)
надо гонять локально или через UI «Run pipeline → Variables» с переопределением.

Альтернативный путь для loadtest без `RUN_PROFILE`: в любом полном
pipeline'е (push в любую ветку) job `loadtest` создаётся как **manual**
кнопкой Play — это удобно когда хочешь сначала прогнать обычные jobs,
а потом ткнуть нагрузочный.

#### Loadtest job — что под капотом

1. Готовит изолированный compose-стек: `COMPOSE_PROJECT_NAME=loadtest-<pipe>-<job>`
   → уникальные docker-сети и volumes, параллельные запуски не конфликтуют.
   Базовый стек — [deploy/docker-compose.yml](deploy/docker-compose.yml)
   (postgres + redis + clickhouse + kafka + receiver + sender + web).
2. Bootstrap-задаёт пароль admin'у через `web --set-admin-password`
   (миграция 0002 создаёт admin'а с NULL hash, без этого шага login упадёт).
3. Запускает loadtest как сервис под compose-профилем `loadtest`
   ([deploy/docker/loadtest.Dockerfile](deploy/docker/loadtest.Dockerfile))
   с `depends_on: receiver/sender/web healthy`. Mock внешних узлов биндит
   на `0.0.0.0:9999`, Receiver/Sender ходят к нему как `http://loadtest:9999`
   (DNS внутри compose-сети).
4. После прогона: `down -v --remove-orphans` и `rm -f .env`. Артефакты —
   `loadtest-report/{report.json, compose-logs.txt}` (хранятся 1 месяц).

#### Pre-flight checklist для `loadtest-only`

- `LOADTEST_ADMIN_PASSWORD` задан в Settings → CI/CD → Variables (Type: Variable,
  Flags: Masked + Protected, Environment scope: `*`). Без него job упадёт в
  `before_script:` с понятным сообщением.
- Runner `srv-d-android-l-docker` имеет доступ к docker daemon (через
  смонтированный `docker.sock` либо DinD service + privileged).
- На runner'е нет других нагрузочных сценариев в это же время — хотя
  namespace docker уникальный, физические ресурсы (CPU/RAM) общие.

## Отладка

| Симптом                          | Что проверить                                    |
|----------------------------------|--------------------------------------------------|
| `port already in use`            | освободить или поменять адрес в config_debug.yml |
| Receiver 401 на запросе с узлом  | `incoming_auth_type=none` или верные креды       |
| Receiver 404 на /api/v1/request/...  | узел существует и `status=enabled`           |
| В ответ на запрос приходит HTML index.html | используешь `/api/v1/...` (не старый `/v1/...`); запрос идёт на Web `:8000` или Receiver `:8080` |
| Sender DLQ заполняется           | внешний URL отвечает; CB не открыт; см. attempts_details |
| Loadtest fail: rps < target      | проверить max_idle_conns_per_host в http-клиенте Sender; ограничения Receiver (read/write_timeout) |
| `migration "dirty"`              | `psql ... SELECT * FROM schema_migrations`; вручную поправить, force-сбросить |
| testcontainers timeout           | проверить что Docker daemon запущен; на Windows — Docker Desktop, на Linux — `systemctl status docker` |
| Тесты после go build падают с OOM (Windows) | использовать `make build-linux` или сборку по одному `./cmd/<name>` с `-ldflags="-s -w"` |
