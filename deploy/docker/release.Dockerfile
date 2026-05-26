# syntax=docker/dockerfile:1.7
#
# Release-Dockerfile для GoReleaser (Phase 7.6).
# Принимает уже собранный GoReleaser'ом бинарь как пре-собранный артефакт —
# в отличие от receiver/sender/web.Dockerfile, которые сами собирают через go build.
#
# Аргумент BINARY указывает имя бинаря (receiver / sender / web). GoReleaser
# подставляет соответствующий артефакт в build context.

ARG BINARY
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S databus && adduser -S -G databus databus

ARG BINARY
WORKDIR /app
COPY ${BINARY} /usr/local/bin/${BINARY}
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

# ENTRYPOINT не может использовать ARG напрямую — оборачиваем через env.
# Имя бинаря фиксируется в файле /entrypoint при сборке.
RUN echo "#!/bin/sh" > /entrypoint && \
    echo "exec /usr/local/bin/${BINARY} --config /app/config/config.yml \"\$@\"" >> /entrypoint && \
    chmod +x /entrypoint

USER databus

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:8080/health || wget -qO- http://localhost:8000/health || exit 1

ENTRYPOINT ["/entrypoint"]
