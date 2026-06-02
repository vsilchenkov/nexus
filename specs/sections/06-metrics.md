## 6. Метрики и наблюдаемость

Каждый сервис экспонирует `/metrics` для Prometheus. Метрики (минимум):

- `nexus_requests_total{service, method, node, status}` — счётчик
- `nexus_request_duration_seconds{service, method, node}` — гистограмма
- `nexus_kafka_lag{topic}` — гейдж
- `nexus_clickhouse_buffer_size` — гейдж
- `nexus_clickhouse_errors_total` — счётчик

Prometheus поднимается в docker compose. Глубина хранения задаётся флагом
`--storage.tsdb.retention.time` (env `PROMETHEUS_RETENTION`, дефолт `90d`).

### 6.1. Источник дашбордов Web-панели — только Prometheus

Все показатели панели метрик (§21) — глобальные KPI Overview, очередь Kafka, per-node throughput, а
также **per-node KPI и график на вкладке «Метрики» узла** (total/delivered/errors, перцентили
p95/p99, временной ряд) — считаются **исключительно из Prometheus query API**
(`prometheus.url`), а не из ClickHouse. ClickHouse используется **только для логов** (поиск,
просмотр, replay).

- total/errors узла — `increase(nexus_requests_total)` / `increase(nexus_request_incomplete_total{service="sender"})`
  (errors = «незавершённые», non-2xx); delivered = total − errors.
- p95/p99 — `histogram_quantile()` по `nexus_request_duration_seconds_bucket`. Бакеты гистограммы
  расширены до 300с (под `timeout_ms` узла), перцентили — приблизительные (по бакетам).
- Метка `node` = `domain.Node.Path`. Счётчики Sender'а пишутся **независимо от `logging_enabled`
  узла**, поэтому метрики узла видны даже при выключенном логировании в ClickHouse.

Деградация: при пустом `prometheus.url` или ошибке запроса панель не падает — отдаёт нули с
`prometheus_available=false` / `chart_available=false`. Глубина доступной истории графиков
ограничена retention Prometheus (`PROMETHEUS_RETENTION`).

