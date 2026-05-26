# CONTRIBUTING — DataBus

> Этот документ для разработчиков, которые хотят вносить изменения в DataBus.
> Если вы только используете шину — читайте [README.md](README.md).
> Если вы агент (Claude Code и т.п.) — обязательно прочитайте [CLAUDE.md](CLAUDE.md)
> до начала работы.

---

## Быстрый старт для разработки

```bash
# 1. Поднять зависимости (Postgres, Redis, ClickHouse, Kafka, Prometheus)
make docker-up-dev

# 2. Применить миграции
make migrate-up

# 3. Bootstrap admin (первая миграция оставляет пароль NULL)
make set-admin-password PASSWORD=changeme

# 4. Поднять три сервиса (каждый в своём терминале)
make run-receiver
make run-sender
make run-web

# 5. UI на http://localhost:8000/, Swagger на http://localhost:8000/swagger/index.html
```

Полный список целей — `make help`.

---

## Документация — карта

Прежде чем менять что-то нетривиальное, прочитайте по порядку:

1. [CLAUDE.md](CLAUDE.md) — конвенции проекта (no globals, DI, context everywhere,
   error handling). Не опциональны.
2. [specs/IMPLEMENTATION.md](specs/IMPLEMENTATION.md) — карта реализации ТЗ:
   что сделано, где это лежит, какие архитектурные решения уже приняты и почему.
3. [specs/sections/17-patterns.md](specs/sections/17-patterns.md) — паттерны
   разработки (Clean Architecture, accept interfaces / return structs, mapper'ы).
4. [TESTING.md](TESTING.md) — как запускать unit/integration/loadtest.

---

## Процесс работы

DataBus разрабатывается итеративными **фазами**. Каждая фаза — набор связанных
блоков (например, Phase 7.6 = GoReleaser). Каждый блок:

1. **Декомпозируйте** задачу заранее. Не начинайте, не имея списка
   подзадач — посередине легко потерять прогресс.
2. **Делайте один коммит на один завершённый блок.** Не сваливайте
   несколько фич в один большой коммит, не дробите одну фичу на 10 микро-коммитов.
3. **Перед коммитом:**
   - `go build ./cmd/<name>` для каждого изменённого main'а (на Windows
     при OOM-линкере — по одному с `-ldflags="-s -w"`, см. [TESTING.md](TESTING.md)).
   - `make test` — unit-тесты зелёные.
   - `make lint` — golangci-lint без новых нарушений.
   - Если менял Swagger-аннотации — `make swagger`.
   - Если менял integration-сценарий — `make test-integration` (нужен Docker).
4. **Обновите [specs/IMPLEMENTATION.md](specs/IMPLEMENTATION.md):**
   - Статус (✅/◐/⛔) и ссылки на новые файлы в «Карте реализации».
   - Архитектурные неочевидности — в раздел «Архитектурные решения и неочевидности».
   - Новая Make-цель — в «Команды для типовых задач».
5. **Формат коммита:**
   ```
   Phase N.M: <одна строка summary>

   - bullet с описанием изменения
   - ссылки на файлы (через relative path)
   ```
6. **Никогда не делайте destructive операций без явной просьбы:**
   `push --force`, `reset --hard`, `drop migration`, `commit --no-verify`.

---

## Стиль кода

Сводка — в [CLAUDE.md §1-§9](CLAUDE.md). Кратко:

- **Никаких глобалов** — никаких `var x = ...` для логгеров, конфигов, клиентов.
  Всё через DI.
- **Interface-driven design** — интерфейсы определяются на стороне consumer'а
  (см. `internal/receiver/usecase/port/`, `internal/web/usecase/port/`).
- **`context.Context` первым параметром** во всех функциях с I/O.
- **Error wrapping через `%w`**, проверка через `errors.Is` / `errors.As`.
- **Функции ≤ 50 строк**, типы — одна ответственность.
- **`go vet ./...` + `golangci-lint run` + `go test -race ./...`** должны быть зелёными.

Разметка пакетов:
- `internal/<domain>/{domain,usecase,adapter}` — Clean Architecture
- `internal/platform/<x>` — cross-cutting (бессмысленен в отрыве от приложения)
- `pkg/...` — только если код реально экспонируется наружу модуля (по умолчанию нет)

---

## Тесты

См. [TESTING.md](TESTING.md). Кратко:

- **Unit-тесты** — рядом с кодом (`*_test.go`), `package x` для white-box или
  `package x_test` для black-box.
- **Mocks через интерфейсы** — никакого monkey-patching, никаких пакетных
  function vars, никакого `//go:linkname`.
- **Table-driven по умолчанию.** `testify/require` для preconditions, `assert`
  для checks. `t.Parallel()` где это безопасно.
- **`go.uber.org/goleak`** в TestMain для пакетов с горутинами.
- **Integration-тесты** — в [tests/integration/](tests/integration/) под
  build-tag `integration`. Поднимают реальные Postgres/Redis/Kafka/ClickHouse
  через `testcontainers-go`. Запуск: `make test-integration` (нужен Docker).
- **Race-detector** — `make test` его включает. На Windows требует CGO; если
  CGO нет — гоняйте `go test -short` без `-race` (race-проверка отрабатывает в Linux CI).

---

## CI / Release

GitHub Actions workflows ([.github/workflows/](.github/workflows/)):

- **ci.yml** — gate-проверки на каждый push: `go vet`, `go build ./...`,
  `go test -race -short`, golangci-lint, swagger drift, vite build, integration
  (опционально — через label `run-integration` для PR).
- **security.yml** — govulncheck (gate) + gosec/trivy/nancy (SARIF в Security tab).
  Гоняется на push, PR, и weekly cron.
- **release.yml** — триггер по тегу `v*`: GoReleaser собирает бинари
  (Linux/Windows/macOS × amd64+arm64) + multi-arch docker images в GHCR.
  Локальная проверка: `make release-check` (синтаксис) и `make release-snapshot`
  (артефакты в `dist/` без публикации).

Релизный workflow:
```bash
git tag v1.2.3
git push origin v1.2.3
# Дальше release.yml сам всё сделает
```

---

## Когда спросить, а когда делать самому

- **Делать сразу:** стиль, рефакторинги без изменения поведения, тесты, godoc,
  lint-фиксы.
- **Сначала спросить:** новая зависимость; изменение публичного интерфейса;
  правки `main`-wiring'а; добавление глобалов; обход существующего интерфейса;
  ослабление теста.

---

## Сообщить о баге

- Опишите шаги воспроизведения.
- Прикрепите релевантный кусок логов (с замаскированными секретами —
  логи и Sentry должны их уже маскировать, но проверьте дважды).
- Если падает запрос — приложите содержимое query-логов из ClickHouse
  (таблица `nodes.clickhouse_table` для проблемного узла) и `id` лог-записи.
