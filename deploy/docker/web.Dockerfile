# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Версия — единый источник истины git: вшивается из git на этапе сборки (.git
# попадает в контекст, git установлен выше). safe.directory — страховка от
# "detected dubious ownership" при несовпадении uid контекста сборки.
# `git update-index --refresh` освежает stat-кэш индекса: после `COPY . .` у файлов
# в слое новые mtime/inode, а `git describe --dirty` (он, в отличие от `git status`,
# refresh не делает) принял бы неизменённое дерево за грязное → ложный суффикс
# "-dirty" в версии ДАЖЕ при чистом `git status` на сервере. См. DEPLOYMENT.md §9.4.
RUN git config --global --add safe.directory /src && \
    git update-index -q --refresh >/dev/null 2>&1 || true; \
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)" && \
    VERSION="${VERSION#v}" && \
    GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" && \
    BUILD_DATE="$(git show -s --format=%cI HEAD 2>/dev/null || echo unknown)" && \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w \
        -X nexus/internal/platform/build.Version=${VERSION} \
        -X nexus/internal/platform/build.Commit=${GIT_COMMIT} \
        -X nexus/internal/platform/build.BuildDate=${BUILD_DATE}" \
      -o /out/web ./cmd/web


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S nexus && adduser -S -G nexus nexus

WORKDIR /app
COPY --from=builder /out/web /usr/local/bin/web
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

# Корпоративные CA — те же, что в образе Sender'а, и по тем же причинам
# (обоснование выбора именно ISSUING-, а не корневого CA Vozovoz — в
# deploy/docker/sender.Dockerfile и deploy/certs/README.md).
#
# Web ходит наружу сам: реестр инстансов (§73) опрашивает соседей ПО HTTPS с
# сервера, а не из браузера. Без этих сертификатов сосед за корпоративным
# сертификатом всегда показывается как «Нет ответа / tls error» — Go отвергает
# цепочку, до /api/version дело не доходит. Сеть при этом в порядке: TLS-ошибка
# возникает уже ПОСЛЕ установленного TCP-соединения.
#
# Ставим ДО `USER nexus`: update-ca-certificates пишет в /etc/ssl/certs.
COPY deploy/certs/vozovoz-issuing-ca.crt /usr/local/share/ca-certificates/
COPY deploy/certs/russian-trusted-root-ca.crt /usr/local/share/ca-certificates/
RUN update-ca-certificates

USER nexus

EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:8000/health || exit 1

ENTRYPOINT ["/usr/local/bin/web", "--config", "/app/config/config.yml"]
