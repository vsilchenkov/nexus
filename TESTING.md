# TESTING.md — процедура запуска тестов DataBus

> **Статус: Phase 0.** В Phase 0 каркас сервисов поднимается, но без бизнес-логики. Полноценные unit/integration/loadtest появляются в Phase 1+.

## Unit-тесты

```bash
make test            # go test -race -short ./...
make test-coverage   # покрытие в ./coverage.html
```

Требований к покрытию пока нет. С Phase 1 — минимум **70%** для пакетов `/internal/receiver`, `/internal/sender`, `/internal/web` (§10.1 ТЗ).

## Integration-тесты (Phase 1+)

Будут использовать [testcontainers-go](https://golang.testcontainers.org/) для подъёма Postgres, Redis, ClickHouse и Kafka:

```bash
make test-integration
```

Требует запущенного Docker daemon.

## Сценарный тест производительности (Phase 4)

Согласно §10.2 ТЗ — отдельный исполняемый `/cmd/loadtest`:

```bash
make loadtest TARGET_RPS=500 DURATION=10m NODES=50
```

Создаёт N рандомных узлов, гонит RPS смешанного трафика (sync + async + динамический URL + динамический auth) в течение DURATION, в конце проверяет в финальном отчёте:

- Достигнутый RPS ≥ 95% от target.
- p95 latency ≤ 200 мс (без учёта mock-задержки).
- Error rate < 0.1%.
- Для async: число записей в ClickHouse-логе = числу отправленных.

При нарушении любого условия exit code = 1.

## CI/CD (Phase 4)

- На каждый PR — lint + unit-тесты + сборка + swagger drift check + короткий loadtest (1 минута на 100 rps).
- Nightly / перед релизом — полный 10-минутный loadtest на 500 rps.

## Отладка тестов

| Симптом                          | Что проверить                                    |
|----------------------------------|--------------------------------------------------|
| `port already in use`            | освободить или поменять адрес в config_debug.yml |
| `testcontainers timeout`         | Docker daemon запущен? `docker ps` отвечает?     |
| `migration "dirty"`              | `psql ... SELECT * FROM schema_migrations`; вручную поправить, затем `force`-ом сбросить |
| Sentry не получает события       | `SENTRY_USE=true` в `.env`, DSN корректен        |
