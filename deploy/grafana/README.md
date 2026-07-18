# Grafana dashboards для Nexus

Здесь лежат JSON-дашборды, импортируемые в Grafana через UI или provisioning.
Три дашборда с общим тегом `nexus`:

| Файл | uid | Назначение |
| ---- | --- | ---------- |
| `nexus.json` | `nexus-overview` | Обзор Nexus в целом (по сервисам). |
| `nexus-nodes.json` | `nexus-nodes` | Здоровье всех узлов, проблемные подсвечены. |
| `nexus-kafka.json` | `nexus-kafka` | Kafka + async-конвейер + DLQ. |

## nexus.json

Обзорный дашборд: RPS / error rate / latency p50-p95-p99 (sync, разрез по сервисам),
Kafka lag (async), ClickHouse buffer / errors / fallback, L2 cache hit ratio,
Go runtime (heap + goroutines по `job`).

Параметры:

- `DS_PROMETHEUS` — datasource типа Prometheus, выбирается при импорте.
- `service` — мульти-селектор сервисов (receiver/sender/web), вытягивается через
  `label_values(nexus_requests_total, service)`.

> Go-runtime панели фильтруются по `job=~"nexus-.*"`, а не по `service`: у метрик
> `go_*`/`process_*` нет const-label `service` (он навешивается только на `nexus_*`),
> label `job` приходит из scrape-конфига `deploy/prometheus.yml`.

## nexus-nodes.json

Per-node мониторинг. Сверху — сразу видно проблемные узлы: счётчики
DOWN / DEGRADED / OK и таблица «Problem nodes (now)». Ниже — сводная таблица всех
узлов за период (отправлено, ошибок, % ошибок, p95, статус — столбцы ошибок красятся),
per-node timeseries (RPS, ошибки top-15, p95), история статуса (state-timeline),
и секция RabbitMQAsync pull-узлов (состояние соединения, глубина очереди, pulled по
статусам, degraded-узлы).

Исходы доставки берутся со стороны **Sender** (`service="sender"`) — это фактический
ответ upstream'а; receiver-сторона `requestAsync` отражает лишь enqueue, не доставку.
Gauge `nexus_node_last_request_error`: `0=OK / 1=DEGRADED / 2=DOWN`.

Параметры:

- `DS_PROMETHEUS` — datasource Prometheus.
- `node` — мульти-селектор узлов, `label_values(nexus_requests_total{service="sender"}, node)`.

## nexus-kafka.json

Kafka и async-конвейер: consumer lag по группам/топикам/партициям (с порогом 10k),
in-flight, produce p95 по топикам, конвейер produced→delivered→failed, ошибки async
по узлам, отменённые запросы, DLQ reprocess (исходы + длительность), loop-detection.

ClickHouse-панели сюда **намеренно не дублируются** — они живут в `nexus.json`
(Overview), т.к. это про Sender-логирование, а не про async-доставку.

Параметры:

- `DS_PROMETHEUS` — datasource Prometheus.
- `node` — мульти-селектор async-узлов, `label_values(nexus_requests_total{method="requestAsync"}, node)`.

### Импорт через UI

1. Открыть Grafana → Dashboards → New → Import.
2. Upload JSON file: выбрать `deploy/grafana/nexus.json` (повторить для
   `nexus-nodes.json` и `nexus-kafka.json`).
3. Выбрать datasource Prometheus (укажет на тот же endpoint, что и
   `deploy/prometheus.yml`).
4. Save.

> uid у всех трёх стабильны (`nexus-overview`, `nexus-nodes`, `nexus-kafka`) —
> повторный импорт обновляет существующий дашборд по uid, а не плодит копии.

### Provisioning (опционально)

Чтобы Grafana подтягивала дашборды автоматически — смонтировать каталог в
`/etc/grafana/provisioning/dashboards/` и положить рядом
`dashboards.yml`:

```yaml
apiVersion: 1
providers:
  - name: nexus
    folder: Nexus
    type: file
    options:
      path: /etc/grafana/provisioning/dashboards
```

## Alert rules

Лежат отдельно в [../prometheus.alerts.yml](../prometheus.alerts.yml) и
подключаются через `rule_files:` в Prometheus config. Покрывают:

- `up{job=nexus-*} == 0` — недоступность сервиса.
- `5xx-rate > 5%`, `p95 > 200ms` — деградация sync-пути по §9.1.
- `nexus_kafka_lag > 10k`, `clickhouse_errors_total > 0`, `dropped > 0` —
  деградация async + потеря логов.
- `l2_cache_hits_total{kind="stale"} > 0` — Redis+PG лежат, Receiver
  обслуживает горячий кеш из памяти (§9.4 крайний случай).
