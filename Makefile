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

swagger: ## Phase 1+: генерация swagger
	@echo "TODO Phase 1+: swag init для receiver и web"

proto: ## Генерация Go-кода из .proto через protoc
	protoc \
		--proto_path=. \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/sender/v1/sender.proto

loadtest: ## Phase 4: нагрузочный сценарий
	@echo "TODO Phase 4: make loadtest TARGET_RPS=500 DURATION=10m NODES=50"

test-integration: ## Phase 1+: integration через testcontainers
	@echo "TODO Phase 1+: testcontainers-based интеграционные тесты"

sqlc-gen: ## Phase 1: генерация Go-кода из SQL через sqlc
	@echo "TODO Phase 1: sqlc generate"

rotate-encryption-key: ## Phase 4: ротация ENCRYPTION_KEY
	@echo "TODO Phase 4: см. §5.5 ТЗ"

# ----- clean ----------------------------------------------------------------

.PHONY: clean

clean: ## Удалить bin/ и тестовые артефакты
	$(GO) clean -testcache
	-$(RM) -r $(BIN_DIR)
	-$(RM) coverage.out coverage.html
