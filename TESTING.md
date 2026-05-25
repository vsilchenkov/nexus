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

Минимальный сценарий §10.1 ТЗ (Postgres + Redis + ClickHouse + Kafka + создание узла → отправка `POST
/v1/request/{path}` → запись в `vika_logs.{table}`) — следующая итерация (добавление CH и Kafka контейнеров
требует heavier-setup, обычно гоняется в CI).

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

## CI/CD (TODO)

GitHub Actions workflow ещё не написан. План:

- На каждый PR: `make lint`, `make test`, `make swagger-drift-check`, сборка всех бинарей,
  loadtest smoke (1 min × 100 rps).
- Nightly / перед релизом: полный loadtest 10 min × 500 rps + `make test-integration`.

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
