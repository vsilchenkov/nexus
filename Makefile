# Nexus — Makefile (см. §13.1 ТЗ).
# Совместим с Windows (GNU Make / mingw32-make) и Linux/macOS.

GO          ?= go
GOFLAGS     ?=
BIN_DIR     ?= bin
LDFLAGS     ?= -s -w
PKG          = ./...

# ----- version (единый источник истины: git) --------------------------------
# Версия вшивается в бинарь из git на этапе сборки (ldflags -X), а не из .env.
# Вычисляется один раз (:=), иначе git дёргается на каждую подстановку.
#  - VERSION: `git describe` → "v1.2.3" / "v1.2.3-5-gabc1234" / "abc1234"
#    (короткий хеш в репо без тегов); суффикс "-dirty" при незакоммиченном дереве.
#  - BUILD_DATE: дата КОММИТА (%cI, ISO-8601) — переносимо Windows/Linux,
#    в отличие от непортируемого `date -u`.
GIT         ?= git
# patsubst срезает ведущий `v` тега (v0.1.0 → 0.1.0) — как делает GoReleaser
# ({{ .Version }}), иначе SPA-футер (он сам добавляет "v") покажет "vv0.1.0".
# Короткий хеш (без тега) не начинается на `v` (hex) — остаётся как есть.
VERSION     := $(patsubst v%,%,$(shell $(GIT) describe --tags --always --dirty 2>/dev/null || echo dev))
GIT_COMMIT  := $(shell $(GIT) rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  := $(shell $(GIT) show -s --format=%cI HEAD 2>/dev/null || echo unknown)
PKG_BUILD   := nexus/internal/platform/build
VERSION_LDFLAGS := -X $(PKG_BUILD).Version=$(VERSION) -X $(PKG_BUILD).Commit=$(GIT_COMMIT) -X $(PKG_BUILD).BuildDate=$(BUILD_DATE)
# LDFLAGS оставляем переопределяемым (base), полный набор с версией — LDFLAGS_FULL.
LDFLAGS_FULL = $(LDFLAGS) $(VERSION_LDFLAGS)

# Грязное дерево вшивается в версию бинаря суффиксом "-dirty" (git describe --dirty).
# Для build-целей предупреждаем (не блокируя): иначе в собранный артефакт уедет
# версия вида "1.1.0-dirty", и строка версии перестаёт соответствовать тегу.
# Чистая сборка тега — с detached-checkout: `git checkout vX.Y.Z` (git status пуст).
ifneq (,$(findstring -dirty,$(VERSION)))
ifneq (,$(filter build%,$(MAKECMDGOALS)))
  $(warning ВНИМАНИЕ: рабочее дерево грязное — бинарь получит версию "$(VERSION)". Закоммить/застешь правки или собери с чистого тега (git checkout vX.Y.Z).)
endif
endif

ifeq ($(OS),Windows_NT)
    GOEXE := .exe
    RM    := del /Q
    MKDIR := mkdir
    # Prepend MSYS2 mingw64 to PATH so cgo (cc1.exe) loads MSYS2 DLLs first
    # instead of older ones from C:\Program Files\Git\mingw64\bin. Required
    # for `go test -race` and any other CGO-enabled build on Windows.
    ifneq ($(wildcard C:/msys64/mingw64/bin/gcc.exe),)
        export PATH := C:\msys64\mingw64\bin;$(PATH)
    endif
else
    GOEXE :=
    RM    := rm -f
    MKDIR := mkdir -p
endif

COMPOSE     ?= docker compose
COMPOSE_F    = -f deploy/docker-compose.yml
COMPOSE_DEV  = $(COMPOSE_F) -f deploy/docker-compose.dev.yml

.PHONY: help
help: ## Список доступных целей
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-25s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----- build ----------------------------------------------------------------

.PHONY: build build-receiver build-sender build-web build-windows build-linux

build: build-receiver build-sender build-web ## Сборка всех бинарей под текущую ОС

build-receiver:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/receiver$(GOEXE) ./cmd/receiver

build-sender:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/sender$(GOEXE) ./cmd/sender

build-web:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/web$(GOEXE) ./cmd/web

build-ui: ## Сборка SPA (web-ui) и копирование в internal/web/static
	cd web-ui && npm install && npm run build
	cp -r web-ui/dist/* internal/web/static/ 2>/dev/null || true

build-windows: ## Кросс-сборка под Windows (.exe)
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/receiver.exe ./cmd/receiver
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/sender.exe ./cmd/sender
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/web.exe ./cmd/web

build-linux: ## Кросс-сборка под Linux
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/receiver ./cmd/receiver
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/sender ./cmd/sender
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS_FULL)" -o $(BIN_DIR)/web ./cmd/web

# ----- run ------------------------------------------------------------------

.PHONY: run-receiver run-sender run-web

run-receiver: ## Локальный запуск Receiver с config_debug.yml
	$(GO) run ./cmd/receiver --debug

run-sender: ## Локальный запуск Sender с config_debug.yml
	$(GO) run ./cmd/sender --debug

run-web: ## Локальный запуск Web с config_debug.yml
	$(GO) run ./cmd/web --debug

# ----- test -----------------------------------------------------------------

.PHONY: test test-coverage lint

test: ## Unit-тесты
	$(GO) test -race -short $(PKG)

test-coverage: ## Покрытие в ./coverage.html
	$(GO) test -race -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html

lint: ## golangci-lint run
	golangci-lint run $(PKG)

# ----- migrations -----------------------------------------------------------

.PHONY: migrate-up migrate-down migrate-status

migrate-up: ## Применить все миграции
	$(GO) run ./cmd/web --debug --migrate-up

migrate-down: ## Откатить N последних миграций: make migrate-down N=1
	$(GO) run ./cmd/web --debug --migrate-down $(N)

migrate-status: ## Текущая версия схемы
	$(GO) run ./cmd/web --debug --migrate-status

set-admin-password: ## Задать пароль admin: make set-admin-password PASSWORD=mypass
	$(GO) run ./cmd/web --debug --set-admin-password $(PASSWORD)

# ----- docker ---------------------------------------------------------------

.PHONY: docker-build docker-up docker-up-dev docker-down docker-logs

docker-build: ## Сборка всех Docker-образов
	$(COMPOSE) $(COMPOSE_F) build

docker-up: ## Поднять полный стек
	$(COMPOSE) $(COMPOSE_F) up -d

docker-up-dev: ## Поднять зависимости nexus-* (НЕ нужно, если уже есть стек `services` — см. docs/STAND_TESTING.md §1)
	@echo "ВНИМАНИЕ: если уже запущен docker-стек 'services' (postgres/redis/clickhouse/kafka), эту цель запускать НЕ НУЖНО — config_debug.yml указывает на него. Иначе создашь дублирующие nexus-* на занятых портах."
	$(COMPOSE) $(COMPOSE_DEV) up -d postgres redis clickhouse kafka prometheus

docker-down: ## Остановить стек
	$(COMPOSE) $(COMPOSE_F) down

docker-logs: ## Логи сервисов (Ctrl+C для выхода)
	$(COMPOSE) $(COMPOSE_F) logs -f receiver sender web

# ----- placeholders для следующих фаз ---------------------------------------

.PHONY: swagger proto loadtest test-integration sqlc-gen rotate-encryption-key
.PHONY: test-int-pg test-int-ch test-int-catalog test-int-receiver test-int-rmq test-int-sender

SWAG ?= swag
swagger: ## Сгенерировать swagger в docs/web и docs/receiver (см. §11, §25)
	$(SWAG) init -g cmd/web/main.go -o docs/web --exclude cmd/receiver,internal/receiver --parseDependency --parseDepth 2 --quiet
	$(SWAG) init -g cmd/receiver/main.go -o docs/receiver --instanceName receiver --exclude cmd/web,internal/web --parseDependency --parseDepth 2 --quiet
	@echo "swagger generated -> docs/web, docs/receiver"

swagger-drift-check: swagger ## CI: фейлит сборку, если docs/ изменились (см. §11.2)
	@git diff --exit-code docs/ \
		|| (echo "swagger drift detected; run 'make swagger' and commit" && exit 1)

proto: ## Генерация Go-кода из .proto через protoc
	protoc \
		--proto_path=. \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/sender/v1/sender.proto

loadtest: ## Нагрузочный сценарий §10.2: make loadtest ADMIN_PASSWORD=... [TARGET_RPS=500 DURATION=10m NODES=50 RATIO_ASYNC=.3 RATIO_DYNAMIC_URL=.2 RATIO_AUTH_TOKEN=.3 RATIO_AUTH_BASIC=.1 CH_ADDR=localhost:9000 RATIO_RMQ=.2 RMQ_URL=amqp://...]
	$(GO) run ./cmd/loadtest \
		--admin-password $(ADMIN_PASSWORD) \
		--target-rps $(or $(TARGET_RPS),500) \
		--duration $(or $(DURATION),10m) \
		--nodes $(or $(NODES),50) \
		--ratio-async $(or $(RATIO_ASYNC),0) \
		--ratio-dynamic-url $(or $(RATIO_DYNAMIC_URL),0) \
		--ratio-auth-token $(or $(RATIO_AUTH_TOKEN),0) \
		--ratio-auth-basic $(or $(RATIO_AUTH_BASIC),0) \
		$(if $(CH_ADDR),--ch-addr $(CH_ADDR),) \
		$(if $(RATIO_RMQ),--ratio-rmq $(RATIO_RMQ),) \
		$(if $(RMQ_URL),--rmq-url $(RMQ_URL),)

INTEGRATION_TIMEOUT ?= 20m

# Под-прогоны тяжёлого integration-пакета по группам зависимостей (§10.1, §27.12).
# Каждая группа — отдельный `go test` со своим -timeout: один зависший/упавший
# тест не съедает бюджет всего пакета и не маскирует остальные группы. GNU Make
# выполняет prerequisite'ы последовательно; Kafka-группа (`sender`, исторически
# самая медленная/тайминг-чувствительная) идёт последней, чтобы все остальные
# группы успели отработать и отчитаться даже при её падении.
# Регексы -run в ДВОЙНЫХ кавычках — переносимо между cmd.exe (Windows) и sh.
# Запуск отдельной группы: `make test-int-rmq` и т.п.
test-integration: test-int-pg test-int-ch test-int-catalog test-int-receiver test-int-rmq test-int-sender ## Integration-тесты под-прогонами (требует Docker; 20m на группу)

test-int-pg: ## integration: Postgres-узлы/миграции/multi-tenancy
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestNodeRepo|^TestNodeUC|^TestNodeCache|^TestMigrations|^TestMultiTenancy" ./tests/integration/...

test-int-ch: ## integration: ClickHouse/шаблоны/метрики/replay
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestClickHouse|^TestCHTemplateRepo|^TestCHProvisioner|^TestLogReader|^TestMetricsReader|^TestReplay" ./tests/integration/...

test-int-catalog: ## integration: каталоги/auth/сессии/нотификации/circuit-breaker
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestHostAllowlist|^TestHeaderCatalog|^TestAuth|^TestSession|^TestUserRoleManager|^TestAppSettingsRepo|^TestNotif|^TestCircuitBreaker" ./tests/integration/...

test-int-receiver: ## integration: Receiver sync/incoming-auth
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestReceiver_" ./tests/integration/...

test-int-rmq: ## integration: RabbitMQAsync Puller (§27)
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestRMQPuller" ./tests/integration/...

test-int-sender: ## integration: Sender async + DLQ (Kafka)
	$(GO) test -tags=integration -count=1 -v -timeout $(INTEGRATION_TIMEOUT) -run "^TestSender_Async" ./tests/integration/...

sqlc-gen: ## Phase 1: генерация Go-кода из SQL через sqlc
	@echo "TODO Phase 1: sqlc generate"

rotate-encryption-key: ## Ротация ENCRYPTION_KEY: make rotate-encryption-key OLD_KEY=... NEW_KEY=... [DRY_RUN=true]
	$(GO) run ./cmd/rotate-key \
		--old-key="$(OLD_KEY)" \
		--new-key="$(NEW_KEY)" \
		$(if $(filter true,$(DRY_RUN)),--dry-run,)

# ----- git hooks (Phase 7.9) ------------------------------------------------

.PHONY: install-hooks uninstall-hooks hooks-run

install-hooks: ## Установить pre-commit/pre-push hooks через lefthook
	@command -v lefthook >/dev/null 2>&1 || $(GO) install github.com/evilmartians/lefthook@latest
	lefthook install
	@echo "git hooks установлены. Конфиг — lefthook.yml. Отключение: make uninstall-hooks"

uninstall-hooks: ## Снять git hooks
	@command -v lefthook >/dev/null 2>&1 && lefthook uninstall || echo "lefthook не установлен"

hooks-run: ## Прогнать pre-commit hooks вручную (без коммита)
	lefthook run pre-commit

# ----- security (Phase 7.7) -------------------------------------------------

.PHONY: vuln-check gosec security-scan

vuln-check: ## govulncheck — Go CVE-сканер с call-graph анализом
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

gosec: ## gosec — статический анализ безопасности (OWASP/CWE)
	@command -v gosec >/dev/null 2>&1 || go install github.com/securego/gosec/v2/cmd/gosec@latest
	gosec -exclude-dir=web-ui -exclude-dir=docs ./...

security-scan: vuln-check gosec ## Локальный security-прогон (vuln + gosec)

# ----- clean ----------------------------------------------------------------

.PHONY: clean

clean: ## Удалить bin/ и тестовые артефакты
	$(GO) clean -testcache
	-$(RM) -r $(BIN_DIR)
	-$(RM) -r dist
	-$(RM) coverage.out coverage.html
