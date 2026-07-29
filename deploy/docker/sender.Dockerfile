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
      -o /out/sender ./cmd/sender


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S nexus && adduser -S -G nexus nexus

WORKDIR /app
COPY --from=builder /out/sender /usr/local/bin/sender
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

# Корпоративный CA «Vozovoz Issuing CA» — иначе узлы на внутренних доменах
# (*.vz78.vozovoz.ru) падают с `x509: certificate signed by unknown authority`.
#
# ВАЖНО: кладём именно ПРОМЕЖУТОЧНЫЙ (issuing) CA, а не корневой. У «Vozovoz
# Root CA» отсутствует расширение basicConstraints (нет CA:TRUE) — по RFC 5280
# он не может подписывать сертификаты, и Go его отвергает независимо от того,
# добавлен он в хранилище или нет («parent certificate cannot sign this kind of
# certificate»; openssl verify даёт `error 24: invalid CA certificate`).
# Issuing CA оформлен корректно (CA:TRUE critical + Certificate Sign), а Go
# принимает любой сертификат из пула как якорь доверия — цепочка замыкается на
# нём и до сломанного корня не доходит. Проверено Go-клиентом против geo2.
#
# Ставим ДО `USER nexus`: update-ca-certificates пишет в /etc/ssl/certs.
# Не использовать SSL_CERT_FILE — он ЗАМЕНЯЕТ системный бандл целиком, и узлы
# с публичными сертификатами перестанут проверяться.
#
# «Russian Trusted Root CA» (НУЦ Минцифры) — им подписан боевой эквайринг Альфа-
# Банка (`pay.alfabank.ru`), переехавший на этот УЦ 29.07.2026 ≈16:05 MSK; в
# alpine-бандле российского НУЦ нет, и узел `qr` встал с `unknown authority`.
# Здесь кладём именно КОРНЕВОЙ (в отличие от Vozovoz — там корень сломан): он
# оформлен корректно (CA:TRUE critical, pathlen:4 + Certificate Sign), живёт до
# 27.02.2032 и переживает перевыпуск промежуточного Sub CA, который приходит в
# рукопожатии. Подробности и сверка отпечатка — в deploy/certs/README.md.
COPY deploy/certs/vozovoz-issuing-ca.crt /usr/local/share/ca-certificates/
COPY deploy/certs/russian-trusted-root-ca.crt /usr/local/share/ca-certificates/
RUN update-ca-certificates

# Директория для CH-fallback (NDJSON .tmp + rename). Sender пишет сюда при
# недоступности ClickHouse — иначе `chlog.flushTable.fallback` фейлится с
# "no such file or directory". Права на нашего непривилегированного user'а.
RUN mkdir -p /app/logs/clickhouse-fallback && \
    chown -R nexus:nexus /app/logs

USER nexus

EXPOSE 9190 9091
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:9091/health || exit 1

ENTRYPOINT ["/usr/local/bin/sender", "--config", "/app/config/config.yml"]
