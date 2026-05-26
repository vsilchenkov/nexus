# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/sender ./cmd/sender


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S databus && adduser -S -G databus databus

WORKDIR /app
COPY --from=builder /out/sender /usr/local/bin/sender
COPY config/config.example.yml /app/config/config.yml
COPY migrations /app/migrations

USER databus

EXPOSE 9090 9091
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -qO- http://localhost:9091/health || exit 1

ENTRYPOINT ["/usr/local/bin/sender", "--config", "/app/config/config.yml"]
