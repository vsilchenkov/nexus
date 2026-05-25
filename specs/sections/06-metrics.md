## 6. Метрики и наблюдаемость

Каждый сервис экспонирует `/metrics` для Prometheus. Метрики (минимум):

- `databus_requests_total{service, method, node, status}` — счётчик
- `databus_request_duration_seconds{service, method, node}` — гистограмма
- `databus_kafka_lag{topic}` — гейдж
- `databus_clickhouse_buffer_size` — гейдж
- `databus_clickhouse_errors_total` — счётчик

Prometheus поднимается в docker compose.

