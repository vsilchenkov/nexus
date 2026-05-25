# TESTING.md — процедура запуска тестов DataBus

## Unit-тесты

```bash
make test           # go test -race -short ./...
make test-coverage  # покрытие в ./coverage.html
```

Покрытые на текущий момент:
- `internal/platform/crypto` — AES-256-GCM round-trip, валидация ключа, детектирование подделки, формат.
- `internal/domain` — Node.SetDefaults/Validate, AuthType.IsDynamic.
- `internal/receiver/usecase` — ResolveURL (static / from_request / wildcard allowlist),
  CheckIncomingAuth (none/basic/token), BuildOutgoingAuth (static),
  BuildDynamicOutgoingAuth (token_from_request: query/header/body, basic_from_request).

Не покрыты unit-тестами (требует mock'и/integration):
- usecase.NodeUsecase / UserUsecase / AuthUsecase / APITokenUsecase / SendUsecase / AsyncProcessor (моки port-интерфейсов — следующая итерация).
- adapter/out/* (postgres, redis, clickhouse, kafka) — integration через testcontainers.
- adapter/in/* (http handlers, gRPC) — httptest + bufconn.

Минимальная планка покрытия по §10.1 ТЗ — 70% для `/internal/{receiver,sender,web}`. На текущем этапе не достигнута; план — Phase 4 итерация 2.

## Integration-тесты (TODO)

По §10.1 ТЗ — через `testcontainers-go` (Postgres, Redis, ClickHouse, Kafka):
```bash
make test-integration   # cейчас — placeholder
```

Минимальный e2e-сценарий:
1. Поднять контейнеры.
2. Накатить миграции.
3. Создать узел через `POST /api/nodes`.
4. Отправить `POST /v1/request/{path}`.
5. Проверить запись в `vika_logs.{table}`.

## Нагрузочный тест (`make loadtest`)

Бинарь `cmd/loadtest` — §10.2 ТЗ. Поднимает встроенный mock-сервер, логинится в Web, создаёт N узлов, гонит target_rps в течение duration через Receiver, считает p50/p95/p99/error_rate, сохраняет JSON-отчёт.

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

- Все варианты `url_mode`/`auth_type` (сейчас static + auth=none).
- Доля async / dynamic-url / token-auth-узлов из конфига (`--ratio-*` — следующая итерация).
- Проверка числа сообщений в ClickHouse-логе == числу отправленных
  (для async). Требует подключения к ClickHouse — TODO Phase 4 итерация 2.

## CI/CD (TODO)

GitHub Actions workflow ещё не написан. План:
- На каждый PR: lint + unit-тесты + сборка + swagger drift (когда swag добавим) + loadtest smoke (1 min × 100 rps).
- Nightly / перед релизом: полный loadtest 10 min × 500 rps.

## Отладка

| Симптом                          | Что проверить                                    |
|----------------------------------|--------------------------------------------------|
| `port already in use`            | освободить или поменять адрес в config_debug.yml |
| Receiver 401 на запросе с узлом  | `incoming_auth_type=none` или верные креды       |
| Receiver 404 на /v1/request/...  | узел существует и `status=enabled`               |
| Sender DLQ заполняется           | внешний URL отвечает; CB не открыт; см. attempts_details |
| Loadtest fail: rps < target      | проверить max_idle_conns_per_host в http-клиенте Sender; ограничения Receiver (read/write_timeout) |
| `migration "dirty"`              | `psql ... SELECT * FROM schema_migrations`; вручную поправить, force-сбросить |
