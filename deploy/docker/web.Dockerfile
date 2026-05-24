# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/web ./cmd/web


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S databus && adduser -S -G databus databus

WORKDIR /app
COPY --from=builder /out/web /usr/local/bin/web
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

USER databus

EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:8000/health || exit 1

ENTRYPOINT ["/usr/local/bin/web", "--config", "/app/config/config.yml"]
