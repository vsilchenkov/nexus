# TESTING.md — процедура запуска тестов DataBus

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

Покрытые сценарии:

- **`TestNodeRepoCreate_E2E`** — реальный Postgres через `tcpg.Run`, миграции из `/migrations`,
  Node CRUD через `UnitOfWorkPg` с проверкой того, что запись узла и `user_audit`-запись лежат в БД
  одной транзакцией.
- **`TestReceiver_Sync_E2E`** — Postgres + `httptest` mock внешнего узла + inline-stub Sender, который
  делает реальный HTTP-запрос вместо gRPC. Проверяет, что запрос дошёл до upstream с правильным path,
  body, и заголовком `Authorization: Bearer ...`.
- **`TestSender_Async_E2E`** — Postgres + Kafka (KRaft) + mock upstream. Receiver-RouteAsync публикует
  Envelope в `databus.async`, Sender ConsumerGroup читает, делает HTTP-вызов, пишет в capturing log
  writer. Покрывает §3.6 / §4.2 happy-path.
- **`TestSender_Async_DLQ_E2E`** — тот же стэк, но mock всегда отвечает 500, узел с `retry_count=2`.
  Отдельный kafka-reader на `databus.async.dlq` дожидается публикации и проверяет headers
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
  --mock-latency 50ms --report report.json
```

### Критерии pass/fail

Loadtest exit'ится с кодом 1, если:

- `achieved_rps < 95%` от `target_rps`;
- `p95 > 200 ms` (без учёта mock-задержки);
- `error_rate >= 0.1%`.

Это и есть критерий §10.2 для CI.

### Что не покрыто текущим loadtest

- Все варианты `url_mode` / `auth_type` (сейчас static + auth=none).
- Доля async / dynamic-url / token-auth-узлов из конфига (`--ratio-*` — следующая итерация).
- Проверка числа сообщений в ClickHouse-логе == числу отправленных
  (для async). Требует подключения к ClickHouse — TODO Phase 6.

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

| Триггер                                    | Что катится                                                   |
|--------------------------------------------|---------------------------------------------------------------|
| push в любую ветку                         | `go-test`, `go-lint`, `swagger-drift`, `go-build`, `ui-build` |
| push в master/dev/tag                      | + `integration` + security stage (govulncheck — gate)         |
| MR с label `run-integration`               | + `integration`                                               |
| MR с изменениями go.mod/go.sum/Dockerfile  | + security stage (govulncheck/gosec/trivy)                    |
| schedule (CI/CD → Schedules, weekly)       | security stage + `renovate`                                   |
| tag `v[0-9]…`                              | + `release` (GoReleaser → GitLab Container Registry)          |

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
| `LOADTEST_TARGET_RPS`     | нет   | `500`   | целевой RPS                                                                   |
| `LOADTEST_DURATION`       | нет   | `5m`    | длительность нагрузки (формат `time.Duration`)                                |
| `LOADTEST_NODES`          | нет   | `50`    | число узлов, которые loadtest создаст через Web API                           |

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
| Receiver 404 на /v1/request/...  | узел существует и `status=enabled`               |
| Sender DLQ заполняется           | внешний URL отвечает; CB не открыт; см. attempts_details |
| Loadtest fail: rps < target      | проверить max_idle_conns_per_host в http-клиенте Sender; ограничения Receiver (read/write_timeout) |
| `migration "dirty"`              | `psql ... SELECT * FROM schema_migrations`; вручную поправить, force-сбросить |
| testcontainers timeout           | проверить что Docker daemon запущен; на Windows — Docker Desktop, на Linux — `systemctl status docker` |
| Тесты после go build падают с OOM (Windows) | использовать `make build-linux` или сборку по одному `./cmd/<name>` с `-ldflags="-s -w"` |
