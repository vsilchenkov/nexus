# syntax=docker/dockerfile:1.7
# Контекст сборки: корень репозитория (см. docker-compose.yml).

FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Версия — единый источник истины git: вшивается из git на этапе сборки (.git
# попадает в контекст, git установлен выше). safe.directory — страховка от
# "detected dubious ownership" при несовпадении uid контекста сборки.
RUN git config --global --add safe.directory /src && \
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)" && \
    VERSION="${VERSION#v}" && \
    GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" && \
    BUILD_DATE="$(git show -s --format=%cI HEAD 2>/dev/null || echo unknown)" && \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w \
        -X nexus/internal/platform/build.Version=${VERSION} \
        -X nexus/internal/platform/build.Commit=${GIT_COMMIT} \
        -X nexus/internal/platform/build.BuildDate=${BUILD_DATE}" \
      -o /out/receiver ./cmd/receiver


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S nexus && adduser -S -G nexus nexus

WORKDIR /app
COPY --from=builder /out/receiver /usr/local/bin/receiver
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

USER nexus

EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/receiver", "--config", "/app/config/config.yml"]
