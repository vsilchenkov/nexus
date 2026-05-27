## 6. Метрики и наблюдаемость

Каждый сервис экспонирует `/metrics` для Prometheus. Метрики (минимум):

- `nexus_requests_total{service, method, node, status}` — счётчик
- `nexus_request_duration_seconds{service, method, node}` — гистограмма
- `nexus_kafka_lag{topic}` — гейдж
- `nexus_clickhouse_buffer_size` — гейдж
- `nexus_clickhouse_errors_total` — счётчик

Prometheus поднимается в docker compose.

