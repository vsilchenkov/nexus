# CLAUDE.md — Go development conventions

> Communicate with the user in **Russian**. Code, identifiers, commit messages may be in English unless asked otherwise.

This file is the contract for any Claude Code session working in this repository.
Read it before touching code. Apply every rule. When in doubt, ask — do not guess.

---

## 1. Core principles (non-negotiable)

- **No global variables.** No package-level mutable state. No `init()` side effects. No singletons reached via package vars.
- **Interface-driven design.** Components depend on interfaces, never on concrete types from another package.
- **Constructor injection.** Every dependency is passed through `New…(…)`. Components never construct their own collaborators.
- **`context.Context` always.** First parameter of every function that performs I/O, blocks, spawns goroutines, or calls a method that does.
- **Errors are values.** Return them, wrap them with `%w`, inspect them with `errors.Is` / `errors.As`. Never `panic` in library code.
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
- [ ] Tests added or updated for every changed behavior; mocks are interface-based.
- [ ] `go vet ./...`, `golangci-lint run`, `go test -race ./...` all pass.
- [ ] `go.mod` / `go.sum` tidy (`go mod tidy`).
- [ ] No leftover `TODO`, `FIXME`, debug prints, commented-out code.
- [ ] Public API documented with godoc comments starting with the identifier name.
- [ ] Diff reviewed: no unrelated changes, no introduced globals, no widened interfaces.

---

## 11. Available skills (`.claude/skills/`)

Skills are domain-specific playbooks loaded by Claude Code on demand. Trigger them by topic when planning or reviewing related work.

### Language fundamentals
- **golang-code-style** — formatting, line breaks, declarations, when comments help vs hurt.
- **golang-naming** — packages, types, interfaces, errors, receivers, getters, options.
- **golang-documentation** — godoc, README, CONTRIBUTING, CHANGELOG, examples, llms.txt.
- **golang-modernize** — upgrade old-style code to current idioms and stdlib features.
- **golang-lint** — `golangci-lint` config, linter selection, nolint suppressions.
- **golang-stay-updated** — Go news, communities, libraries to watch.

### Types & structure
- **golang-structs-interfaces** — composition, embedding, segregation, pointer vs value receivers.
- **golang-data-structures** — slices/maps internals, container/*, strings.Builder, generics.
- **golang-design-patterns** — functional options, constructors, lifecycle, graceful shutdown, resilience.
- **golang-project-layout** — `cmd/`, `internal/`, `pkg/`, monorepo, workspace layout.

### Correctness & safety
- **golang-error-handling** — wrapping, sentinels, custom types, `errors.Join`, slog, oops.
- **golang-safety** — nil panics, append aliasing, map races, float pitfalls, defensive copies.
- **golang-context** — propagation, cancellation, timeouts, `WithoutCancel`, request-scoped values.
- **golang-concurrency** — goroutines, channels, sync, errgroup, singleflight, worker pools.
- **golang-security** — injection, crypto, secrets, filesystem, network, cookies, memory.

### Testing & quality
- **golang-testing** — table-driven, parallel, fuzz, fixtures, goleak, coverage, integration.
- **golang-stretchr-testify** — `assert`, `require`, `mock`, `suite` in depth.
- **golang-benchmark** — writing benchmarks, pprof, benchstat, regression detection.
- **golang-performance** — optimization patterns once a bottleneck is identified.
- **golang-troubleshooting** — systematic root-cause debugging, Delve, race, GODEBUG, pprof.

### Production
- **golang-observability** — slog, Prometheus, OpenTelemetry, pprof, RUM, alerting, Grafana.
- **golang-continuous-integration** — GitHub Actions, SAST, coverage, Dependabot, GoReleaser.
- **golang-dependency-management** — go.mod, MVS, vuln scanning, conflicts, workspaces.
- **golang-popular-libraries** — production-ready library recommendations.

### Persistence & APIs
- **golang-database** — `database/sql`, `sqlx`, `pgx`, transactions, scanning, pools, migrations.
- **golang-grpc** — server/client, protobuf, interceptors, TLS, streaming, bufconn testing.
- **golang-graphql** — `gqlgen`, `graphql-go`, resolvers, subscriptions.
- **golang-swagger** — `swaggo/swag` annotations, framework integrations, code generation.

### CLI
- **golang-cli** — command structure, flags, config layering, exit codes, signals, completion.
- **golang-spf13-cobra** — command trees, hooks, validators, completion, doc generation.
- **golang-spf13-viper** — layered config precedence, env binding, hot reload, test isolation.

### Dependency injection
- **golang-dependency-injection** — why DI, manual injection, library comparison.
- **golang-google-wire** — compile-time DI, `wire.Build`, `wire.Bind`, provider sets.
- **golang-uber-dig** — reflection-based container, In/Out, named values, groups.
- **golang-uber-fx** — `fx.New`, `fx.Module`, `fx.Lifecycle`, annotated providers.
- **golang-samber-do** — service container, scopes, lifecycle, health checks.

### `samber/*` ecosystem
- **golang-samber-lo** — 500+ functional helpers (Map, Filter, Reduce, GroupBy, …).
- **golang-samber-mo** — monadic types (Option, Result, Either, Future, IO, Task, State).
- **golang-samber-ro** — reactive streams, observables, subjects, operators.
- **golang-samber-hot** — in-memory cache (LRU, LFU, TinyLFU, ARC, SIEVE, …).
- **golang-samber-oops** — structured errors with stack traces and attributes.
- **golang-samber-slog** — slog handlers, sampling, formatters, HTTP middleware, backends.

---

## 12. When to ask vs when to act

- **Act:** style fixes, refactors that preserve behavior, tests, doc comments, lint fixes.
- **Ask first:** introducing a new dependency, changing a public interface, touching `main` wiring, anything that would add a global, anything that bypasses an existing interface, anything that weakens a test.
