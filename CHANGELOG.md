# Changelog

Все заметные изменения в проекте документируются в этом файле.

Формат основан на [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
проект следует [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> Версионирование ведётся по git-тегам `v*` (job `release` в [.gitlab-ci.yml](.gitlab-ci.yml)).
> Первый релиз — `1.0.0`; его записи сгруппированы по фазам разработки до тега.

---

## [Unreleased]

## [1.1.0] - 2026-06-17

Релиз преимущественно из аудита надёжности/безопасности (ветка `fix/audit-2026-06`,
блоки Phase AUD.1–AUD.8) плюс точечные правки Web/Sentry/loadtest.

### Added

- **Глобальный список пользователей** — эндпоинт `/api/users` выведен из-под team-scope:
  пользователи видны и управляются глобально, а не в рамках одной команды.

### Security — аудит 2026-06

- **AUD.4 — web security.** Анти-брутфорс логина, проверка `Origin` для CSRF на
  мутирующих запросах, набор security-заголовков в ответах Web Service.
- **AUD.5.** Корректные trusted proxies (реальный клиентский IP за обратным прокси),
  `LastSeenAt` обновляется в `Touch`, креды узлов шифруются и в Redis-кеше (а не только в PG).

### Changed — аудит надёжности 2026-06

- **AUD.1 — sender.** Прерываемое ожидание paused-узлов (реакция на shutdown без
  залипания) + дренаж буфера ClickHouse-лога при `Stop`.
- **AUD.2.** `nodecache` write-back под `recover`/дедупом; circuit breaker переведён на
  single-probe half-open (одна пробная попытка вместо потока).
- **AUD.3.** Shutdown-гигиена фоновых горутин через `safego.Go`/`Await`; покрытие `goleak`.
- **AUD.6 — ui.** Устойчивый SSE live-tail (переподключение) + сброс query-кеша на
  logout/401.
- **AUD.7 — ui.** Клиентская валидация формы узла, NaN-guard, точечная инвалидация
  кеша, сброс формы после сохранения.
- **AUD.8.** UI-консистентность + мелочи backend: `.done`-маркер в file-fallback,
  метрика срабатывания fail-open лимитов.
- **chore.** `go fix` — модернизация идиом (range over int, loop var, `omitempty` на
  `time.Time`); добавлен `docker-compose.override.yml` для локальной разработки.

### Fixed

- **teams.** Участники команды отображаются логином/email вместо сырого UUID.
- **sentry.** Служебные трейсы `/metrics`, `/health`, `/ready` больше не отправляются в Sentry.
- **loadtest.** Keep-alive пул соединений + дочитывание тела ответа (корректная переиспользуемость соединений).
- **chlog.** Устранён data race на `WaitGroup` в file-fallback store (`Add` внутри горутины).

## [1.0.3] - 2026-06-09

### Changed — деплой без сборки образов в CI

- **Сборка Docker-образов в CI и публикация в GitLab Container Registry убраны.**
  Release-job (GoReleaser) требовал DinD/buildx/docker.io-auth на раннере и нестабильно
  работал. Деплой теперь — сборкой из исходников на сервере: `docker compose up -d --build`
  (версия по-прежнему вшивается из git, DEPLOYMENT.md §9.1/§9.5).
- `.gitlab-ci.yml`: удалён job `release` и стадия `release`; `GIT_DEPTH: 0` убран из
  `variables` (в CI больше не нужен). Тег `v*` прогоняет обычные test/lint/build.
- DEPLOYMENT.md §9 переписан: build-on-server — основной путь; registry-путь (§9.2),
  `VERSION`/`REGISTRY_BASE` в `.env` и раздел про GoReleaser — убраны.
- Удалены `.goreleaser.yaml`, `deploy/docker/release.Dockerfile` и make-цели
  `release-check`/`release-snapshot` (артефакты бывшего CI-релиза).
- `.gitlab-ci.yml`: job `loadtest` теперь запускается и на тег `v*` — так же, как на
  `master` (smoke-прогон, `allow_failure`).

## [1.0.1] - 2026-06-09

### Fixed
- CI release-job ([.gitlab-ci.yml](.gitlab-ci.yml)): образ `goreleaser/goreleaser:v2`
  (несуществующий тег, `manifest unknown`) → `:latest`; убран конфликтующий сервис
  `docker:24-dind` (раннер монтирует `docker.sock` — был `device or resource busy`).
  Историческое: позднее сборка образов в CI убрана целиком (см. [Unreleased]) — деплой
  перешёл на сборку из исходников на сервере.

## [1.0.0] - 2026-06-09

### Версионирование — единый источник истины git

#### Fixed
- Версия приложения теперь берётся **только** из git (ldflags), а не из `.env`/config.
  Устранены три бага, из-за которых git-версия не доезжала: (1) GoReleaser инъектил в
  несуществующий символ `bus/internal/platform/build.Version` (модуль — `nexus`), линкер
  молча игнорировал `-X` → релиз по тегу не получал версию; (2) `bootstrap.go` не
  перекрывал config-версию значением из ldflags; (3) `/api/version` показывал значение из
  `.env`/дефолт, а не из сборки.

#### Changed
- `.goreleaser.yaml`: путь символа `bus/` → `nexus/` во всех `-X`; дата `{{ .Date }}` → `{{ .CommitDate }}`.
- `bootstrap.go`: ldflags-версия (`buildOpt.Version`) перекрывает config (а она всегда непуста —
  минимум `0.0.0-dev` из versioninfo.json); ключ `build.version` убран из `config.example.yml`
  (в `config_debug.yml`, который под git skip-worktree, значение теперь игнорируется).
- `Makefile` и `deploy/docker/*.Dockerfile`: версия вшивается из `git describe --tags --always --dirty`
  (в Docker — внутри builder-стейджа, `.git` в контексте сборки); добавлен `.dockerignore`
  (намеренно сохраняет `.git`).
- `cmd/*/versioninfo.json`: `ProductVersion` → `0.0.0-dev` (fallback-маркер, вручную не бампается).
- `.gitlab-ci.yml`: `GIT_DEPTH: 0` вынесен в глобальные `variables` (теги для `git describe`/GoReleaser).
- `VERSION` в `.env` теперь только селектор тега образа на registry-пути, не версия приложения;
  DEPLOYMENT.md §9.0/§9.4 переписаны.

### Phase 7.9 — pre-commit hooks (lefthook)

#### Added
- `lefthook.yml` — pre-commit (gofmt/goimports/govet/golangci-lint `--new-from-rev=HEAD~ --fast`),
  pre-push (`go test -short` + swag drift-check), commit-msg (формат `Phase N.M:` или conventional).
- Make-цели `install-hooks`/`uninstall-hooks`/`hooks-run` с авто-установкой lefthook через `go install`.
- CONTRIBUTING.md — раздел «Git hooks» с описанием ивентов.

### Phase 7.8 — CONTRIBUTING + актуализация документации

#### Added
- `CONTRIBUTING.md` — процесс работы (блоки/коммиты/IMPLEMENTATION.md), стиль кода, тестирование, CI/release.
- README badges (Go version).
- Раздел «Docker images (release builds)» в README со ссылками на GitLab Container Registry.

#### Changed
- README статус-таблица обновлена строками Phase 6 и Phase 7.
- IMPLEMENTATION.md: устаревшие ⛔ пометки на «полный лейаут §7» и «app_settings + UI Sentry» заменены на ✅ Phase 6.3/6.4.
- Раздел «Куда копать дальше» переписан под §16 ТЗ (OpenTelemetry/webhook signatures/KMS/multi-tenancy).

### Phase 7.7 — security scanning

#### Added
- Stage `security` в `.gitlab-ci.yml` — параллельные jobs:
  - **govulncheck** (call-graph CVE-анализ, гейтит pipeline),
  - **gosec** (OWASP/CWE → SARIF в артефакты, `allow_failure: true`),
  - **trivy-fs** (vuln + secrets + Dockerfile/YAML misconfig → SARIF, `allow_failure: true`).
- Триггеры: push в master/dev, weekly schedule, manual.
- Make-цели `vuln-check`/`gosec`/`security-scan` с авто-установкой через `go install`.

### Phase 7.6 — GoReleaser + multi-arch docker

#### Added
- `.goreleaser.yaml` — 5 builds (receiver/sender/web/rotate-key/loadtest) × Linux/Windows/macOS × amd64+arm64.
- Archives (tar.gz + zip для Windows) с README/LICENSE/config.example/migrations, SHA-256 checksums, changelog с группами Features/Bug fixes/Phase milestones.
- 6 docker images (receiver/sender/web × amd64+arm64) через `deploy/docker/release.Dockerfile` + 3 multi-arch manifests `:{Version}` и `:latest` в GitLab Container Registry.
- Job `release` в `.gitlab-ci.yml` — триггер на тег `v*`; QEMU + Buildx + login в `$CI_REGISTRY`.
- Make-цели `release-check` (синтаксис) / `release-snapshot` (артефакты в `dist/`).
- `bus/internal/platform/build.{Version,Commit,BuildDate}` переменные пакета, переопределяются через `-ldflags`; fallback на versioninfo.json при обычной сборке.
- `bootstrap.Init` логирует `commit`/`build_date`, если заполнены.

### Phase 7.5 — Grafana dashboard + Prometheus alert rules

#### Added
- `deploy/grafana/nexus.json` — 10 панелей (RPS, error rate, request duration p50/p95/p99, Kafka lag, CH buffer/errors/dropped/fallback, L2 cache hit ratio, Go runtime).
- `deploy/prometheus.alerts.yml` — 9 alert rules (up==0, 5xx>5%, p95>200ms, kafka_lag>10k, CH errors/buffer/dropped, L2 stale-fallback).
- `deploy/grafana/README.md` — инструкция импорта.

### Phase 7.4 — GitLab CI pipeline

#### Added
- `.gitlab-ci.yml` — stages test/lint/build: `go-test` (race -short), `go-build`, `go-lint` (golangci-lint v2.12), `swagger-drift`, `ui-build` (Node 20 + vite build), `integration` (MR-label `run-integration` или master/dev/tag).
- `.golangci.yml` — bodyclose/rowserrcheck/errcheck/govet/revive/staticcheck.
- `renovate.json` — weekly gomod + npm, monthly docker; группировка minor/patch.

### Phase 7.3 — расширенный integration suite

#### Added
- Redis testcontainers (SessionRepo CRUD + DeleteByUser + TTL expire, NodeCache Set/GetByPath/Invalidate).
- ClickHouse testcontainers (chlog.Writer batch insert + LogReaderCH `GetByID`/`Search` с фильтрами status/IP/Done/full-text + SQL-injection guard в имени таблицы).
- Generic `testcontainers.GenericContainer` (без отдельных модулей) с retry-ping для CH native-handshake.

### Phase 7.2 — L2 in-memory LRU кеш узлов в Receiver

#### Added
- `nodecache.L2Reader` — decorator над `port.NodeReader`, generic `LRU[V]` (~150 строк через `container/list`+map+mutex).
- Stale-fallback при downstream-ошибках (§9.4 ТЗ — Redis и PG одновременно лежат).
- Конфиг `receiver.l2_cache.{enabled,size,ttl_ms,stale_ttl_ms}` с zero-overhead disable.
- Метрики `nexus_l2_cache_{hits,misses,evictions}_total` + `_size`.

### Phase 7.1 — Swagger 100% endpoints + UI

#### Added
- Swagger-аннотации на all handlers (auth, nodes, users, tokens, audit, dry-run, replay, logs, settings/app, settings/clickhouse/orphans).
- `ginswagger.WrapHandler` в `internal/web/app.go` + blank-import `_ "nexus/docs/web"` → UI на `/swagger/index.html`.
- Deps `github.com/swaggo/gin-swagger` + `github.com/swaggo/files`.

### Phase 6 — Web admin, observability, hot-reload, audit improvements

См. подробности в [IMPLEMENTATION.md](specs/IMPLEMENTATION.md) (раздел «Сделанное в Phase 6»).
Резюме: 9 блоков (6.1-6.9).

#### Added
- Prometheus метрики (`nexus_requests_total`, latency, kafka_lag, CH-метрики).
- Async e2e integration через Kafka.
- `app_settings` таблица + REST API + overlay поверх env.
- Hot-reload Sentry через Redis pub/sub.
- Полное hot-reload ClickHouse (Manager + WriterManager).
- Test connection для Sentry/ClickHouse.
- Settings → Users полный CRUD страница (§7.9/§7.10).
- Live-tail UI: подсветка новых записей (1s), авто-прокрутка, баннер «N new», pagination size, фильтры status/done.
- CSV-экспорт audit log (`/api/audit/export.csv`).
- ClickHouse orphan-tables — сканер + DROP с подтверждением.
- Live-tail расширенные фильтры (period/IP/Host/full-text через `port.LogQuery`).
- Audit log diff-двухколоночный для `node.update` (компонент `AuditDetailsCell`).

### Phase 5 — paused→async, dry-run, replay, SSE live-tail, i18n, SPA

#### Added
- paused-узел в sync: 202 + `queued:true` + `node_status:paused` через сигнальный `ErrNodePaused`.
- `POST /api/nodes/dry-run` (§7.5.1) — пошаговый отчёт без реального запроса.
- `POST /api/logs/{id}/replay` (§7.4.1) — replay через HTTPDispatcher (реальный Receiver pipeline, не bypass) + маркер `__replay_of` + rate-limit 10/мин.
- SSE live-tail `/api/nodes/{id}/logs/stream` (§7.4) с heartbeat, `RequireSessionOnly` (отклоняет API-токены).
- `rotate-encryption-key` утилита (идемпотентная, v1-формат `v1:nonce:ct:tag`).
- CH partition-drop housekeeping (§4.3) + миграция `clickhouse_retention_days`.
- Swagger generation + drift-check (`make swagger-drift-check`).
- `port.UnitOfWork` (атомарные Create/Update/Delete с audit-записью в одной транзакции).
- Sentry tracing-middleware для Gin (§14.3).
- i18n / Accept-Language (en/ru) на backend + SPA.
- SPA: React 18 + Vite + TS + Tailwind + TanStack Query + react-i18next, 6 страниц.
- Integration testcontainers (Postgres + миграции).

### Phase 5.1 — CH file-fallback, расширенный i18n, integration sync

#### Added
- NDJSON file-fallback при недоступности CH (`adapter/out/chlog/fallback.go`) с атомарной записью `.tmp` + rename и Windows-safe restore.
- Расширенный i18n на handlers.
- Расширение SPA (NodeSettings, ApiTokens, Language, Theme, AuditLog).

### Phase 5.2 — unit-тесты Replay/Logs/Sentry, локализация SPA, docs

#### Added
- Unit-тесты для Replay/Logs usecase, Sentry middleware.
- Локализация SPA-сообщений.
- `specs/IMPLEMENTATION.md` — карта проделанных работ.
- CLAUDE.md §0 «Project orientation» + явный процесс «коммит на блок + обновление IMPLEMENTATION.md».

### Phase 4 — loadtest, unit-тесты, housekeeping

#### Added
- `cmd/loadtest` бинарь (§10.2 ТЗ) — 500 rps × 10 мин с pass/fail-критериями.
- Unit-тесты для критических usecase'ов (domain/crypto/i18n/sentry/receiver-usecase).
- Housekeeping cron — audit retention.

### Phase 3 — Web auth, API tokens, SPA каркас

#### Added
- Web auth: users CRUD, sessions в Redis, login/logout/me, RBAC, `--set-admin-password` CLI.
- API-токены: SHA-256 hash, scopes, rate-limit, audit.
- GET `/api/audit` с фильтрами.
- SPA каркас через embed.FS (index.html-заглушка).

### Phase 2 — async через Kafka, circuit breaker, rate-limit, audit

#### Added
- Kafka admin + producer + consumer на `segmentio/kafka-go` (auto-create topics, retention 30 дней, acks=all, idempotence).
- Receiver `/v1/requestAsync/*` — producer, envelope, paused→202.
- Sender consumer для `nexus.async` — async-обработка + DLQ + paused-pacing (offset commit только после успешной доставки).
- Circuit breaker per-node в Redis (§9.5).
- Rate-limit per-node + per-token через Redis.
- `user_audit` таблица + AuditRepo + AuditUsecase + запись для CRUD узлов.

### Phase 1 — sync end-to-end, auth, AES-256-GCM, gRPC, ClickHouse

#### Added
- Sync end-to-end: `/v1/request/*` → Receiver → gRPC к Sender → внешний URL → лог в ClickHouse.
- Все режимы incoming auth (none/basic/token) и outgoing auth (none/basic/token/token_from_request/basic_from_request).
- URL-режимы `static`/`from_request` с allowlist + wildcard `*.partner.com`.
- AES-256-GCM шифрование auth_credentials в БД (v1-формат `v1:nonce:ct:tag`).
- proto/sender/v1/sender.proto + `make proto` + сгенерированный gRPC код.
- HTTP-клиент с keep-alive + retry/backoff.
- ClickHouse batch writer (§4 ТЗ).
- Постгрес-миграции 0001-0002 (uuid extension, methods, nodes, node_headers, users).
- Domain (Node, User, Session, LogRecord) + ports.

### Phase 0 — фундамент

#### Added
- Скелет трёх сервисов (Receiver/Sender/Web), docker-compose, healthcheck, kardianos/service runner.
- Базовая инфраструктура `internal/platform/{config,logging,sentry,pg,redis,clickhouse,kafka,crypto,...}`.

---

[Unreleased]: https://gitlab.ci.vozovoz.ru/bus/nexus/-/compare/v1.1.0...HEAD
[1.1.0]: https://gitlab.ci.vozovoz.ru/bus/nexus/-/compare/v1.0.3...v1.1.0
[1.0.3]: https://gitlab.ci.vozovoz.ru/bus/nexus/-/compare/v1.0.1...v1.0.3
[1.0.1]: https://gitlab.ci.vozovoz.ru/bus/nexus/-/compare/v1.0.0...v1.0.1
