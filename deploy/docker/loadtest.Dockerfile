# syntax=docker/dockerfile:1.7
# Контекст сборки: корень репозитория (см. docker-compose.yml).
#
# Контейнер для cmd/loadtest — сценарного теста производительности (§10.2 ТЗ).
# Запускается под compose-профилем `loadtest` (см. deploy/docker-compose.yml),
# чтобы при обычном `docker compose up` стек поднимался без него.

FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/loadtest ./cmd/loadtest


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S databus && adduser -S -G databus databus

WORKDIR /app
COPY --from=builder /out/loadtest /usr/local/bin/loadtest

USER databus

# Порт mock-сервера внешних узлов. В job передаём `--mock-bind 0.0.0.0:9999`
# и `--mock-public-url http://loadtest:9999`, чтобы Receiver/Sender могли
# достучаться по compose-сети.
EXPOSE 9999

ENTRYPOINT ["/usr/local/bin/loadtest"]
