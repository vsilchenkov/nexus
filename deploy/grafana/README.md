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

**Узел — пара команда/путь (§101).** Путь уникален лишь внутри команды, поэтому все выражения
группируют `by (team, node)`, легенды — `{{team}}/{{node}}`. Стат-панели DOWN / DEGRADED / OK
считают пары: одноимённые узлы двух команд — два узла. Сводная таблица склеивает пять запросов
трансформацией **Merge** по обеим меткам: прежний `joinByField` склеивал по одному полю `node` и
сложил бы строки одноимённых узлов в одну.

Параметры:

- `DS_PROMETHEUS` — datasource Prometheus.
- `team` — мульти-селектор команд, `label_values(nexus_requests_total{service="sender"}, team)`.
  «All» подставляет регэксп `.*` (`allValue`), а не перечисление значений: перечисление спрятало
  бы ряды с `team=""`, собранные до обновления на §101.
- `node` — мульти-селектор узлов выбранных команд,
  `label_values(nexus_requests_total{service="sender",team=~"$team"}, node)`. Стат-панели и
  «Problem nodes (now)» фильтруются только по команде: они про все узлы, как и раньше.

## nexus-kafka.json

Kafka и async-конвейер: consumer lag по группам/топикам/партициям (с порогом 10k),
in-flight, produce p95 по топикам, конвейер produced→delivered→failed, ошибки async
по узлам, отменённые запросы, DLQ reprocess (исходы + длительность), loop-detection.

ClickHouse-панели сюда **намеренно не дублируются** — они живут в `nexus.json`
(Overview), т.к. это про Sender-логирование, а не про async-доставку.

Параметры:

- `DS_PROMETHEUS` — datasource Prometheus.
- `team` — мульти-селектор команд, `label_values(nexus_requests_total{method="requestAsync"}, team)`,
  «All» = `.*` — по той же причине, что в `nexus-nodes.json`.
- `node` — мульти-селектор async-узлов выбранных команд,
  `label_values(nexus_requests_total{method="requestAsync",team=~"$team"}, node)`. Панели по узлам
  группируют `by (team, node)`.

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
  деградация async + потеря логов. Lag — из брокера (§113.4), без очередей паузы
  и DLQ: для очереди паузы свой `NexusPausedBacklogStuck` (не пуста, а узлов на
  паузе нет 30 мин).
- `l2_cache_hits_total{kind="stale"} > 0` — Redis+PG лежат, Receiver
  обслуживает горячий кеш из памяти (§9.4 крайний случай).
- группа `nexus.nodes` — здоровье узла (DOWN / DEGRADED / error rate), pull-узлы RabbitMQ, DLQ.
  Агрегация `by (team, node…)`, в summary — `команда/путь` (§101): одноимённые узлы разных команд
  дают отдельные алерты и не маскируют друг друга (`max by (node)` прятал DEGRADED одной команды
  за DOWN другой).

Проверка — `make check-alerts`: `promtool check rules` и юнит-тесты
[../prometheus.alerts.test.yml](../prometheus.alerts.test.yml) в образе Prometheus той же версии,
что в docker-compose. В CI — job `alerts-check`. Меняете правило — поправьте и тест: он сверяет
метки и тексты алертов целиком.
