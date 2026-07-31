## 31. Мониторинг Kafka

Новый раздел админ-панели **«Мониторинг Kafka»** — alarm-dashboard, открыв который
администратор за ~30 секунд понимает состояние шины: throughput, отставание consumer (lag),
ошибки, топ-узлы по нагрузке, состояние топиков и брокеров. Это **не** замена Grafana (туда уходит
детальное расследование по labels), а быстрый обзор «всё ли в порядке сейчас и за последние N
часов». Раздел **read-only** и доступен **только администраторам**.

> **Отклонения от исходного черновика ТЗ (важно).**
> 1. **Расположение пункта меню.** Пункт «Kafka» размещён **не** в группе «Настройки», а в основной
>    навигации рядом с **Audit log** (блок «Аудит»). Маршрут — `/kafka`, не `/settings/kafka`.
>    Пункт виден только админам.
> 2. **Имена метрик.** Черновик ссылался на `databus_kafka_*`; в коде Nexus таких метрик нет. Async-
>    трафик Kafka уже размечен в существующих метриках (см. §6/§28) фильтром `method="requestAsync"`,
>    они и используются как источник (без новых метрик и без правок Receiver/Sender).
> 3. **Размер топика на диске.** Высокоуровневый `segmentio/kafka-go` не экспонирует
>    `DescribeLogDirs`, поэтому админ-запрос размера не даёт. ~~`size_bytes` приходит `0` (UI
>    показывает «—»)~~ — ограничение снято в **[§75](75-topic-size-jmx.md)**: размер берётся из
>    Prometheus (`kafka_log_log_size` JMX-агента брокера) и домешивается в ответ `/api/kafka/topics`.
>    Там, где JMX-агента нет (внешний чужой кластер), поведение прежнее — «—».

### 31.1. Доступ

Раздел виден только админам. Для `viewer`/`manager` пункт «Kafka» в навигации не отображается, а
прямой GET/POST к `/api/kafka/*` возвращает **403 Forbidden**. Решение: Kafka-метрики показывают
детали инфраструктуры (адреса брокеров, размеры топиков), которые не-админ знать не должен.

Все эндпоинты `/api/kafka/*` имеют отдельный rate-limit (по умолчанию 60 req/min на пользователя,
`web.kafka_monitor_rate_limit_per_min`) — защита от dashboard-флуда при автообновлении.

### 31.2. Источники данных

- **Prometheus** (query/query_range) — throughput, ошибки, latency, lag, top-узлы. Async-трафик
  выделяется фильтром `method="requestAsync"`:
  - produced ← `nexus_requests_total{service="receiver",method="requestAsync"}`
  - consumed ← `nexus_requests_total{service="sender",method="requestAsync"}`
  - errors ← `nexus_request_incomplete_total{method="requestAsync"}` (+ status-фильтр для produced)
  - lag ← `nexus_kafka_lag{topic,partition,group}`
  - in-flight ← `sum(nexus_kafka_in_flight)` — выделенный gauge (Sender: fetched, но не
    committed; §31 добавил его в consumer-цикл)
  - produce p95 ← `nexus_kafka_produce_duration_seconds_bucket{topic}` — выделенная гистограмма
    длительности публикации в Kafka (Receiver/Sender producer)
- **Kafka Admin** (`segmentio/kafka-go` `*kafka.Client`) — метаданные топиков (партиции, RF, ISR,
  offline), consumer-группы и lag (ListGroups/OffsetFetch/ListOffsets), число сообщений
  (Σ high-low watermark), ping брокеров (Metadata). Кешируется в Redis (TTL 30с).
- **Top-узлы** — из Prometheus (`NodeThroughput`, метка `node`), а не из ClickHouse: в CH логи лежат
  по одной таблице на узел (нет единой колонки `node_path` для `GROUP BY`).

Все источники **деградируют мягко**: нет Prometheus → KPI/графики нули (`prometheus_available=false`);
нет доступа к Kafka → блоки «Топики»/«Брокеры» помечены недоступными (`kafka_available=false`), экран
не падает.

### 31.3. Web API

Все — admin-only, под `/api/kafka/*`, rate-limit на пользователя.

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/api/kafka/overview?range\|from\|to` | сводка: summary (produced/consumed/failed/lag/in-flight), дельта к пред. периоду, broker_health, severity health-banner, error_rate |
| GET | `/api/kafka/timeseries?range&step&metrics` | ряды produced/consumed/errors (rate) и lag (gauge) через query_range; шаг авто по периоду или явный |
| GET | `/api/kafka/topics` | топики: партиции, RF, размер (best-effort), оценка сообщений, consumer-группы+lag, состояние реплик |
| GET | `/api/kafka/by-node?range\|from\|to` | top_producers (по числу async-сообщений) и top_failures (по ошибкам) |
| POST | `/api/kafka/test` | ping каждого брокера (отклик + предупреждения) |

Период: пресеты `1h/3h/24h/7d/14d/30d` или произвольный `from`/`to` (RFC3339/UnixMilli), лимит
произвольного периода — **90 дней** (иначе 400).

Severity health-banner вычисляется на каждом `/overview` по порогам (§31.5). Бэкенд отдаёт
машинный `reason`-код (`healthy`/`offline_partitions`/`brokers_down`/`error_rate_high`/`lag_high`/
`lag_growing`/`under_replicated`/`produce_latency_high`); конкретный текст с подстановкой чисел
локализуется на фронте (бэкенд i18n-независим).

### 31.4. UI экрана

Расположение — `/kafka` (блок «Аудит» в навигации). Структура сверху вниз:

1. **Шапка** — заголовок, селектор периода (`PeriodPicker`, общий компонент), индикатор «обновлено
   N сек назад», кнопки «Обновить» и «Проверить кластер».
2. **Health banner** — зелёный/жёлтый/красный, всегда показывает, что делать (текст по `reason`).
3. **4 KPI** — сообщений за период (+дельта), текущий lag (+спарклайн, цвет по порогу), ошибок за
   период (+распределение и доля), in-flight.
4. **Главный график throughput** (recharts) — produced/consumed сплошные, errors пунктир; селектор
   разрешения `Auto/10s/1m/5m`.
5. **Lag-график** (recharts) — линия lag + пороговая линия критичности (1000) с подсветкой зоны.
6. **Таблица топиков** — имя, партиции, RF, размер, оценка сообщений, consumer-группы (chips), состояние.
7. **Top producers / Top failures** side-by-side; пустой failures → зелёное «нет ошибок».
8. **Карточки брокеров** — адрес, online/offline, ping, предупреждения (из `/test`).

Автообновление — раз в 10с (инвалидация TanStack Query); кнопка «Обновить» — немедленно.

### 31.5. Пороги индикации

Настраиваются в `web.kafka_alerts_thresholds.*`, дефолты:

| Состояние | Жёлтый | Красный |
|---|---|---|
| Consumer lag (текущий) | > 100 | > 1000 ИЛИ рост > 50/сек |
| Error rate (produced+consumed) | > 0.1% | > 1% |
| Under-replicated partitions | ≥ 1 | — |
| Offline partitions | — | ≥ 1 (всегда критично) |
| Brokers offline | ≥ 1 | > N/2 |
| Produce latency p95 (прокси) | > 100ms | > 500ms |

### 31.6. Out of scope (v1)

Тревоги по email/Slack (это пассивный мониторинг; алертинг — §22/§16), управление топиками
(read-only экран), просмотр сообщений в Kafka (kcat/kafka-console-consumer), per-topic графики
throughput, графики ресурсов брокера (CPU/RAM/disk — зона кластерного мониторинга).
