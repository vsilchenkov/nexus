# CLAUDE.md — Go development conventions

> Communicate with the user in **Russian**. Code, identifiers, commit messages may be in English unless asked otherwise.

This file is the contract for any Claude Code session working in this repository.
Read it before touching code. Apply every rule. When in doubt, ask — do not guess.

---

## 0. Project orientation — обязательно прочитать первым делом

Этот репозиторий — **Nexus**, шина данных из трёх Go-сервисов (Receiver + Sender + Web) + React SPA.
Полное ТЗ — [specs/nexus_spec.md](specs/nexus_spec.md), нарезано по разделам в [specs/sections/](specs/sections/)
(`NN-<slug>.md` + индекс в `sections/README.md`). **Новые крупные фичи/ТЗ добавляются туда новым
разделом** — см. правило в «Процесс работы» ниже.

**Прежде чем что-либо менять — открой [specs/IMPLEMENTATION.md](specs/IMPLEMENTATION.md).** Это карта
проделанных работ: статус каждого пункта ТЗ (✅/◐/⛔), ссылки на ключевые файлы кода, архитектурные
решения и неочевидности (зачем `ErrNodePaused`, почему replay через HTTP а не bypass, как устроен
file-fallback на Windows, и т.п.). Без этого документа ты потратишь время на исследование того, что
уже задокументировано.

### Архитектура в одном абзаце

Sync: `POST /v1/request/{path}` → Receiver (auth + URL resolve + masking) → gRPC к Sender → внешний
HTTP → ответ обратно. Async: `POST /v1/requestAsync/{path}` → Receiver → Kafka `nexus.async` →
Sender-consumer → внешний HTTP → лог в ClickHouse (или в NDJSON file-fallback при недоступности CH).
Конфиг узлов в PostgreSQL (с шифрованием кредов AES-256-GCM), горячий кеш и сессии в Redis.
Web Service отдаёт REST API под `/api/*` и SPA (`embed.FS`) на всё остальное. Подробности — в
[sections/02-architecture.md](specs/sections/02-architecture.md).

### Carved-in принципы для этого репозитория

- **Clean Architecture строго: `handler → usecase → port → adapter`.** Usecase зависит **только** от
  интерфейсов из `usecase/port/`, никогда от конкретных типов из `adapter/out/`. См.
  [sections/17-patterns.md](specs/sections/17-patterns.md).
- **Шифрование кредов живёт только в `adapter/out/postgres`.** `domain.Node` всегда содержит plaintext.
  Если касаешься Node в любом другом слое — креды уже расшифрованы. Не дублируй логику шифрования.
- **Receiver через `DBTX` интерфейс, не `*pgxpool.Pool`.** Это требование UnitOfWork — репозитории
  должны работать с пулом ИЛИ с pgx.Tx одинаково. См.
  [internal/web/adapter/out/postgres/db.go](internal/web/adapter/out/postgres/db.go).
- **Замаскированные значения (`***`) — не реверсивны.** В логах, Sentry, dry-run отчётах. Если
  добавляешь новое чувствительное поле — добавь его имя в `sensitiveKeys` в
  [platform/sentry/sentry.go](internal/platform/sentry/sentry.go).
- **Логгер только через DI.** Никаких `logging.GetLogger()` в продакшн-коде. В unit-тестах
  используй `logging.NewNoop()`.
- **`team_id` в v1 всегда `'default'`.** Колонки уже есть как закладка под multi-tenancy v2, но
  любая фильтрация по `team_id` сейчас бессмысленна.

### Самые частые сценарии

| Задача                       | Куда смотреть                                                            |
|------------------------------|--------------------------------------------------------------------------|
| Добавить новый endpoint Web  | [internal/web/usecase/](internal/web/usecase/) + [adapter/in/http/](internal/web/adapter/in/http/) + routes.go |
| Добавить таблицу/колонку     | новая миграция в [migrations/](migrations/), затем `Validate()` и DB-маппер |
| Поправить routing Receiver   | [internal/receiver/usecase/route.go](internal/receiver/usecase/route.go) (sync) или `route_async.go` |
| Изменить логику Sender       | [internal/sender/usecase/](internal/sender/usecase/) (`send.go`, `async.go`, `ch_housekeeping.go`) |
| Новый i18n-ключ              | [internal/platform/i18n/i18n.go](internal/platform/i18n/i18n.go) (backend) **и** [web-ui/src/locales/](web-ui/src/locales/) (frontend) |
| Добавить SPA-страницу        | [web-ui/src/pages/](web-ui/src/pages/), маршрут в `App.tsx`, кнопку в `Topbar.tsx` |
| Прогнать integration-тест    | `make test-integration` (нужен Docker daemon)                            |
| Прогнать живой тестовый стенд (ручной сквозной прогон) | [docs/STAND_TESTING.md](docs/STAND_TESTING.md) — два варианта запуска (Docker / нативный), `scripts/stand/seed_and_test.{sh,ps1}`, чек-лист проверки по фичам |
| Сгенерировать Swagger        | `make swagger` после правки аннотаций над handler'ом                     |

> **Инфраструктура разработки.** На рабочей машине разработчика (Windows) установлен **Docker
> Desktop** — integration-тесты (`make test-integration`, testcontainers поднимает PostgreSQL/
> ClickHouse/Redis) и docker-compose можно запускать локально. То есть «нужен Docker» по тексту
> ниже здесь выполнимо: после изменения миграций / репозиториев / схемы **прогоняй
> `make test-integration`**, а не ограничивайся unit-тестами. Если CI integration-job всё же
> падает — сперва отличай инфра-проблему раннера от ошибки в коде (см. [memory] feedback_ci_infra).

### Процесс работы — коммит на каждый завершённый блок + актуальный IMPLEMENTATION.md

**Декомпозиция.** Большую задачу всегда разбивай на завершённые блоки (A.1, A.2, B.1, ...). Каждый
блок — это самостоятельная фича/правка, которая компилируется и проходит тесты. Сразу зафиксируй
этот список в TodoWrite, иначе посередине легко потерять прогресс.

**После каждого завершённого блока — обязательная троица:**

1. **Прогнать сборку и тесты.**
   - `go build ./...` (на Windows при OOM-линкере — `go build -ldflags="-s -w" ./cmd/<name>` по одному).
   - **Перед тестами — `go fix` + `gofmt`.** Прогони `go fix ./cmd/... ./internal/... ./tests/...`
     (не `./...` из корня — он спотыкается о стороннюю `.go`-заглушку в `web-ui/node_modules/`),
     затем `gofmt -w` на затронутые файлы (или `gofmt -l .` для проверки). Это держит код в
     актуальных идиомах Go и не даёт упасть CI job `lint` (`golangci-lint` проверяет `gofmt`).
   - `make test` — unit-тесты зелёные.
   - **`golangci-lint run --timeout=5m` — обязательно перед коммитом, не только `go vet`.**
     `go vet`/`gofmt` НЕ ловят `nilerr` (проверил `err != nil` → вернул `nil`),
     `staticcheck` (QF1001 De Morgan и пр.) и остальные линтеры из `.golangci.yml`. Их ловит
     только golangci-lint, и CI job `lint` на них падает (образ `golangci/golangci-lint:v2.12-alpine`,
     rolling — версия может обгонять локальную, гоняй именно его). Пропуск этого шага = красный CI
     и перепушивание постфактум.
   - Если менял Swagger-аннотации — `make swagger`.
   - Если менял integration-сценарий — `make test-integration` (нужен Docker).
   - Если менял `web-ui/` (TS/TSX, package.json, eslint.config.js) — `cd web-ui && npm run lint && npm run build`.
     В CI ([.gitlab-ci.yml](.gitlab-ci.yml) job `ui-build`) lint запускается с `--max-warnings=0` —
     любой warning валит pipeline. Если меняешь зависимости — коммить и `package-lock.json`,
     иначе `npm ci` в CI развалится.
   - **После правки `web-ui/` обязательно пересобери встроенный SPA и закоммить бандл в том же
     коммите.** Web Service отдаёт фронт из `internal/web/static/` через `embed.FS` — правка только
     исходников `web-ui/src/` НЕ доходит до пользователя, пока бандл не пересобран. Прогони
     `make build-ui` (`npm run build` → копирует `web-ui/dist/*` в `internal/web/static/`), затем
     `go build ./cmd/web`, и **закоммить изменённый `internal/web/static/`** (имена ассетов хешируются —
     старый `index-<hash>.js` заменяется новым). Грабли: Phase E (UI RabbitMQAsync) поправил исходники,
     но не пересобрал бандл — в проде показывались две карточки типа узла вместо трёх (см. коммит
     `fix(ui): пересборка встроенного SPA`). Пропуск этого шага = «фича в `dev`, но её нет в интерфейсе».

2. **Дописать результаты в [specs/IMPLEMENTATION.md](specs/IMPLEMENTATION.md).**
   - В разделе «Карта реализации по разделам ТЗ» — поменять статус (✅/◐/⛔) и добавить ссылки на
     новые файлы.
   - Если решение неочевидное — добавить пункт в раздел «Архитектурные решения и неочевидности»
     (зачем сделано именно так, какие грабли).
   - Если появилась новая Make-цель — добавить в «Команды для типовых задач».
   - Если фича частично закрывает Phase 6+ из «Куда копать дальше» — обновить и этот список.
   - Цель документа: чтобы следующий агент **не повторял твоё расследование**. Если ты что-то понял
     не из кода — запиши.

   **Актуализировать `DEPLOYMENT.md` и `DEVELOPMENT.md`, если блок влияет на развёртывание или
   процесс разработки** — в том же коммите. Триггеры: новая миграция, новый конфиг/ENV-параметр,
   новый шаг генерации (proto/swagger), изменение зависимостей между сервисами (например «Telegram
   теперь требует Prometheus»), новая Make-цель, изменение порядка запуска. Это часть обязательной
   троицы: пропустишь — следующий деплой/онбординг наткнётся на сюрприз.

3. **Сделать коммит блока** с осмысленным сообщением:
   - Формат: `Phase N.M: <одна строка summary>` + развёрнутое тело со списком файлов и пунктов ТЗ
     (см. историю — `git log --oneline` для образца).
   - Один коммит = один логически завершённый блок. Не сваливай несколько фич в один большой коммит,
     не дроби одну фичу на 10 микро-коммитов.
   - Коммитить только когда блок реально готов (тесты прошли, IMPLEMENTATION.md обновлён) — не как
     «промежуточное сохранение».
   - Никогда не делай destructive операций (`push --force`, `reset --hard`, `drop migration`,
     `--no-verify`) без явной просьбы пользователя.

**Ветвление: новое крупное ТЗ → отдельная ветка → слияние в `dev` после подтверждения.**
Работу над новым ТЗ (крупная фича) веди в отдельной ветке, созданной от `dev`
(`git switch -c feature/<slug>`). Все блочные коммиты (`Phase N.M: ...`) — в эту ветку.
Самовольно в `dev`/`master` ничего не мержим и не пушим. Слияние ветки в `dev`
(`git switch dev && git merge --no-ff feature/<slug>`) — **только после явного подтверждения
пользователя**, что ТЗ завершено. `push` ветки/`dev` — тоже по явному запросу. Мелкие правки
(багфиксы, стиль, тесты) можно делать прямо в текущей ветке без отдельной — правило про новую
ветку касается именно нового ТЗ/крупной фичи.

**Новая крупная фича/ТЗ → новый раздел в `specs/sections/`.** Когда работа вводит существенную
новую возможность (а не правку существующей) — оформи её как ТЗ, а не только как код:

1. Создай новый раздел `specs/sections/NN-<slug>.md` (следующий свободный номер, формат как у
   соседних: `## NN. Заголовок` + подпункты `### NN.1`, ...). Опиши модель данных, API, UI, scope,
   неочевидности — чтобы будущий агент понял замысел без раскопок по коду.
2. Добавь строку в таблицу-оглавление `specs/sections/README.md`.
3. Синхронизируй сводный `specs/nexus_spec.md` (источник истины) — допиши тот же раздел.
4. Если фича была в `16-out-of-scope.md` как «план на v2» — пометь её там как реализованную со
   ссылкой на новый раздел (см. как сделано с Multi-tenancy → §18).
`IMPLEMENTATION.md` остаётся картой *реализации* (статусы/файлы/грабли); `sections/` — это само *ТЗ*.

**В конце многоблочной сессии:** прогон полного `make test`, проверка `git log` («все коммиты на
месте, ничего не амеnд'нуто»), и краткий итоговый отчёт пользователю с перечислением коммитов и
ссылками на обновлённые разделы IMPLEMENTATION.md.

---

## 1. Core principles (non-negotiable)

- **No global variables.** No package-level mutable state. No `init()` side effects. No singletons reached via package vars.
- **Interface-driven design.** Components depend on interfaces, never on concrete types from another package.
- **Constructor injection.** Every dependency is passed through `New…(…)`. Components never construct their own collaborators.
- **`context.Context` always.** First parameter of every function that performs I/O, blocks, spawns goroutines, or calls a method that does.
- **Errors are values.** Return them, wrap them with `%w`, inspect them with `errors.Is` / `errors.As`. Never `panic` in library code.
- **Panic recovery in every goroutine.** Каждая горутина в продакшн-коде первой строкой ставит
  `defer safego.Recover(<logger>, "<op>")` (пакет [internal/platform/safego](internal/platform/safego/safego.go),
  ТЗ §30.2): паника гасится, логируется как `error` (с капчуром в Sentry и stacktrace), процесс не
  падает. Для горутин с request-scoped контекстом (несущим Sentry-hub) — `safego.RecoverCtx(ctx, <logger>, "<op>")`.
  В пулах с `defer wg.Done()` recover ставится в коде **после** `wg.Done()` (LIFO — выполнится первым,
  гасит панику до отработки `wg.Done`). Не пиши свой `recover()` — используй `safego`. `recover()` ловит
  панику только в своей горутине, поэтому middleware-recovery родительской горутины дочернюю НЕ спасает.
- **Small, focused units.** One file, one purpose. One function ≤ 40–50 lines. One type, one responsibility.

If a change cannot satisfy these principles, stop and discuss the design before writing code.

---

## 2. SOLID — strict enforcement

- **S — Single Responsibility.** One function / type does exactly one thing. Functions ≤ 40–50 lines. If a function grows, split it; if a type grows, decompose it.
- **O — Open/Closed.** Extend behavior by adding new types that satisfy an existing interface, or by passing in new collaborators / functional options. Do not mutate existing logic to bolt on new cases.
- **L — Liskov Substitution.** All implementations of an interface (e.g. `ProcessedStore`) are interchangeable. Same contract, same error semantics, same context behavior. No "this one ignores ctx" exceptions.
- **I — Interface Segregation.** Many small interfaces (1–3 methods) over one fat interface. Define the interface on the **consumer side**, listing only what the consumer actually calls.
- **D — Dependency Inversion.** High-level code depends on abstractions defined in its own package. Concrete implementations are wired in `main` (or a `wire`/`fx` graph). A package never imports a concrete implementation of one of its own dependencies.

---

## 3. Interface-driven design

- **Accept interfaces, return structs.** Constructors return the concrete `*T`; consumers declare the interface they need.
- Interfaces live in the **consumer** package, not the implementer package. The implementer does not know who consumes it.
- Compile-time check on the implementer side:
  ```go
  var _ SomeInterface = (*SomeImpl)(nil)
  ```
- No empty interfaces (`any`) in public APIs except where generics or `encoding/json` legitimately require them.
- Prefer composing small interfaces (`io.Reader` + `io.Closer` → `io.ReadCloser`) over declaring one large one.

---

## 4. Zero global state

- No `var x = …` at package level for anything mutable (counters, caches, clients, configs, loggers, RNGs, clocks).
- Configuration is loaded once in `main`, passed down explicitly.
- Loggers, metrics, tracers — injected via constructors, not pulled from package globals.
- `init()` functions: avoid. Only acceptable for registering with a stdlib registry where no alternative exists (e.g. `database/sql` drivers in `cmd/` only).
- Singletons are wired by the DI container (`google/wire`, `uber-fx`, `samber/do`), not by `sync.Once` on a package var.
- Time and randomness are dependencies: inject a `Clock` interface and a `*rand.Rand`, never call `time.Now()` / `rand.Intn` directly in business logic.

---

## 5. Modern Go patterns (target Go 1.22+)

- **Generics** for type-safe collections and helpers. No `interface{}` containers.
- **`log/slog`** for structured logging. No `log`, `fmt.Println`, `zap`, `logrus`, `zerolog` in new code.
- **`errors.Join`**, **`errors.Is` / `errors.As`**, **`%w`** wrapping. No string matching on error messages.
- **`slices` / `maps` / `cmp`** standard packages over hand-rolled loops.
- **`sync.OnceValue` / `sync.OnceValues`** instead of `sync.Once` + package var.
- **`context.WithoutCancel`**, **`context.AfterFunc`** where applicable.
- **Range over integer** (`for i := range n`) and **range over function** (Go 1.23 iterators) where they clarify intent.
- **`errgroup.WithContext`** for structured concurrency. No bare `go f()` without an owner that waits and propagates errors.
- **Functional options** (`Option func(*config)`) for constructors with > 3 optional parameters.
- **`any`** instead of `interface{}` in new code.
- Use `min` / `max` / `clear` builtins.

Avoid: `panic` outside `main`, naked `recover`, `init()` side effects, `reflect` in hot paths, `unsafe` outside `internal/`.

---

## 6. `context.Context` rules

- **First parameter, always.** `func (s *Service) Do(ctx context.Context, …) error`.
- **Propagate, never replace.** Pass the incoming `ctx` down. Wrap with `WithTimeout` / `WithCancel` only when the callee owns a deadline.
- **Never store `ctx` in a struct.** Pass per call.
- **No `context.TODO()`** in production code. `Background()` only at process entry points (`main`, tests, background workers explicitly outliving requests).
- **Check `ctx.Err()`** at loop heads and before expensive work.
- **Background work outliving the request:** detach with `context.WithoutCancel(ctx)`, never with `context.Background()`, so trace/log values propagate.
- **`ctx.Value`** only for request-scoped values that cross API boundaries (trace ID, auth principal). Never for optional parameters.

---

## 7. Error handling

- Wrap with `%w` and add context: `fmt.Errorf("parse line %d: %w", n, err)`.
- Sentinel errors: `var ErrNotFound = errors.New("repo: not found")` — exported, immutable, package-prefixed message.
- Custom error types implement `Error()` and, where useful, `Unwrap()` and `Is(target error) bool`.
- Handle each error exactly once: log **or** return — never both.
- No `_ = err`. If the error is truly ignorable, write a one-line comment explaining why.
- For production-grade structured errors with stack traces, prefer `samber/oops` (see `golang-samber-oops` skill).

---

## 8. Testing

- **Mocks only through interfaces.** No monkey-patching, no overwriting package-level function vars, no `//go:linkname`. If a component is hard to mock, the design is wrong — fix the design.
- **Table-driven tests** are the default. Each row: `name`, inputs, want, wantErr.
- **`testify`** for assertions and mocks. `require` for preconditions, `assert` for checks.
- **`t.Parallel()`** by default. Capture loop variables explicitly.
- **`testify/suite`** when fixtures are shared across many tests.
- **`go.uber.org/goleak`** in `TestMain` for any package that spawns goroutines.
- **Race detector** (`go test -race`) in CI on every PR.
- **Fuzzing** (`func FuzzX(f *testing.F)`) for parsers and any code consuming external bytes.
- **Concrete example — parser test:**
  - Input: a single log line (string).
  - Expectation: an exact `ParsedEntry` struct.
  - One row in the table per shape of input (happy path, malformed timestamp, missing field, oversized, empty, …).
- **Integration tests** hit real dependencies (containers via `testcontainers-go`), not mocks of those dependencies. Mocks are for unit tests of the component under test, not for the dependency boundary.

---

## 9. Package layout

- `cmd/<binary>/main.go` — entry point, only wiring.
- `internal/<domain>/…` — domain code, not importable from outside the module.
- `pkg/…` — only for code intentionally exposed to other modules. Default to `internal/`.
- One package = one responsibility. No `utils`, `helpers`, `common`, `shared`, `misc`.
- Test files (`*_test.go`) live next to the code they test. Use `package x_test` for black-box tests where the public API is the contract.

See `golang-project-layout` skill for full guidance.

---

## 10. Workflow checklist (before reporting a task as done)

Run through this list every time. If any item fails, fix it before declaring success.

- [ ] No new package-level mutable variables.
- [ ] Every new dependency is an interface defined on the consumer side and injected through a constructor.
- [ ] Every new exported function with I/O / blocking / goroutines accepts `ctx context.Context` as the first parameter.
- [ ] No function exceeds ~50 lines; no type carries more than one responsibility.
- [ ] Errors are wrapped with `%w` and handled exactly once.
- [ ] No `panic` outside `main`. No `init()` with side effects.
- [ ] Every new goroutine starts with `defer safego.Recover(<logger>, "<op>")` (or `RecoverCtx` for request-scoped ctx; after `wg.Done()` in pools). See §1 and ТЗ §30.2.
- [ ] Tests added or updated for every changed behavior; mocks are interface-based.
- [ ] `go vet ./...`, `golangci-lint run`, `go test -race ./...` all pass.
- [ ] `go.mod` / `go.sum` tidy (`go mod tidy`).
- [ ] No leftover `TODO`, `FIXME`, debug prints, commented-out code.
- [ ] Public API documented with godoc comments starting with the identifier name.
- [ ] Diff reviewed: no unrelated changes, no introduced globals, no widened interfaces.

---

## 11. Skills

Skills live in `.claude/skills/`. Claude Code auto-loads their descriptions and triggers them by
topic — no manual invocation needed. To browse: `ls .claude/skills/`; each skill's contract lives in
its `SKILL.md`. Three families are installed:

- **42 Go skills** from `samber/cc-skills-golang` (`golang-*`) — language, libraries, testing, CI,
  performance. The backbone for any work in `cmd/`, `internal/`, `tests/`.
- **5 Nexus front-end skills** (`nexus-web-*`) — project-specific guides for the React SPA in
  `web-ui/`, written against the actual code (not generic React advice). Start with
  `nexus-web-overview`; then `nexus-web-data` (react-query + the `api` wrapper), `nexus-web-components`
  (UI-kit atoms, `cn` + Tailwind tokens, **controlled `useState` forms — not react-hook-form**),
  `nexus-web-i18n` (`en.json`/`ru.json` sync + backend key parity), `nexus-web-testing` (Vitest + RTL,
  currently greenfield — no tests exist yet). **When touching `web-ui/`, consult these first** — they
  encode the embed-and-rebuild contract and the `--max-warnings=0` CI gate.
- **4 general engineering skills** from `mattpocock/skills` — `prototype` (throwaway UI/logic spikes),
  `tdd` (red-green-refactor; applies to `web-ui/` where `golang-testing` doesn't), `setup-pre-commit`
  (Husky + lint-staged + Prettier for the JS/TS side), `migrate-to-shoehorn` (TS test assertions).

---

## 12. When to ask vs when to act

- **Act:** style fixes, refactors that preserve behavior, tests, doc comments, lint fixes.
- **Ask first:** introducing a new dependency, changing a public interface, touching `main` wiring, anything that would add a global, anything that bypasses an existing interface, anything that weakens a test.
