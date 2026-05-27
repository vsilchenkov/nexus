# Grafana dashboards для Nexus

Здесь лежат JSON-дашборды, импортируемые в Grafana через UI или provisioning.

## nexus.json

Обзорный дашборд: RPS / error rate / latency p50-p95-p99 (sync), Kafka lag (async),
ClickHouse buffer / errors / fallback, L2 cache hit ratio, Go runtime.

Параметры:

- `DS_PROMETHEUS` — datasource типа Prometheus, выбирается при импорте.
- `service` — мульти-селектор сервисов (receiver/sender/web), вытягивается через
  `label_values(nexus_requests_total, service)`.

### Импорт через UI

1. Открыть Grafana → Dashboards → New → Import.
2. Upload JSON file: выбрать `deploy/grafana/nexus.json`.
3. Выбрать datasource Prometheus (укажет на тот же endpoint, что и
   `deploy/prometheus.yml`).
4. Save.

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
