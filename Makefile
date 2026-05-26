# DataBus — Makefile (см. §13.1 ТЗ).
# Совместим с Windows (GNU Make / mingw32-make) и Linux/macOS.

GO          ?= go
GOFLAGS     ?=
BIN_DIR     ?= bin
LDFLAGS     ?= -s -w
PKG          = ./...

ifeq ($(OS),Windows_NT)
    GOEXE := .exe
    RM    := del /Q
    MKDIR := mkdir
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
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/receiver$(GOEXE) ./cmd/receiver

build-sender:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/sender$(GOEXE) ./cmd/sender

build-web:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/web$(GOEXE) ./cmd/web

build-ui: ## Сборка SPA (web-ui) и копирование в internal/web/static
	cd web-ui && npm install && npm run build
	cp -r web-ui/dist/* internal/web/static/ 2>/dev/null || true

build-windows: ## Кросс-сборка под Windows (.exe)
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/receiver.exe ./cmd/receiver
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/sender.exe ./cmd/sender
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/web.exe ./cmd/web

build-linux: ## Кросс-сборка под Linux
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/receiver ./cmd/receiver
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/sender ./cmd/sender
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/web ./cmd/web

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

docker-up-dev: ## Поднять только зависимости (для локального make run-*)
	$(COMPOSE) $(COMPOSE_DEV) up -d postgres redis clickhouse kafka prometheus

docker-down: ## Остановить стек
	$(COMPOSE) $(COMPOSE_F) down

docker-logs: ## Логи сервисов (Ctrl+C для выхода)
	$(COMPOSE) $(COMPOSE_F) logs -f receiver sender web

# ----- placeholders для следующих фаз ---------------------------------------

.PHONY: swagger proto loadtest test-integration sqlc-gen rotate-encryption-key

SWAG ?= swag
swagger: ## Сгенерировать swagger в docs/web и docs/receiver (см. §11)
	$(SWAG) init -g cmd/web/main.go -o docs/web --parseDependency --parseDepth 2 --quiet
	@echo "swagger generated -> docs/web"

swagger-drift-check: swagger ## CI: фейлит сборку, если docs/ изменились (см. §11.2)
	@git diff --exit-code docs/ \
		|| (echo "swagger drift detected; run 'make swagger' and commit" && exit 1)

proto: ## Генерация Go-кода из .proto через protoc
	protoc \
		--proto_path=. \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/sender/v1/sender.proto

loadtest: ## Нагрузочный сценарий: make loadtest TARGET_RPS=500 DURATION=10m NODES=50 ADMIN_PASSWORD=...
	$(GO) run ./cmd/loadtest \
		--admin-password $(ADMIN_PASSWORD) \
		--target-rps $(or $(TARGET_RPS),500) \
		--duration $(or $(DURATION),10m) \
		--nodes $(or $(NODES),50)

test-integration: ## Integration-тесты через testcontainers (требует Docker)
	$(GO) test -tags=integration -count=1 -v ./tests/integration/...

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

# ----- release (Phase 7.6) --------------------------------------------------

.PHONY: release-check release-snapshot

release-check: ## Проверить .goreleaser.yaml на синтаксис
	goreleaser check

release-snapshot: ## Локальный snapshot-релиз (без публикации) — артефакты в dist/
	GITHUB_REPOSITORY=local/databus \
	GITHUB_REPOSITORY_LOWER=local/databus \
	goreleaser release --snapshot --clean --skip=publish

# ----- clean ----------------------------------------------------------------

.PHONY: clean

clean: ## Удалить bin/ и тестовые артефакты
	$(GO) clean -testcache
	-$(RM) -r $(BIN_DIR)
	-$(RM) -r dist
	-$(RM) coverage.out coverage.html
