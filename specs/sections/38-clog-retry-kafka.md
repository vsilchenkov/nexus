## 38. Durable-retry проваленных ClickHouse-батчей через Kafka (замена NDJSON-fallback)

Логи запросов пишет Sender: после внешнего HTTP-вызова запись (`domain.LogRecord`) уходит в
буферный канал `chlog.Writer`, фоновый воркер копит батч и делает `INSERT` в ClickHouse (§4.3).
Если ClickHouse недоступен, `INSERT` падает — **отправка данных при этом не блокируется** (§9.4):
sync-ответ клиенту и async-доставка в получатель уже состоялись, падает только логирование.

До §38 проваленный батч сохранялся в **локальный NDJSON-файл** (`logs/clickhouse-fallback/`) и
переотправлялся фоновым циклом рестора каждые 30с. §38 заменяет этот локальный fallback на
**durable-буфер в Kafka** (топик `nexus.logs.retry`): проваленный батч продьюсится в Kafka, а
отдельный consumer-group дренит его обратно в ClickHouse после восстановления. Это реплицируемо
(переживает потерю хоста Sender), централизованно (нет россыпи файлов по инстансам) и опирается на
уже имеющийся в шине Kafka. Цена — нагрузка на Kafka **только во время простоя CH** (одно сообщение
на проваленный батч, ~100 строк, а не на каждый запрос); в норме (CH жив) Kafka логированием не
нагружается — прямой `INSERT` как прежде.

### 38.1 Поток

```
request → Sender → внешний HTTP → chlog.Writer (буфер → батч)
                                       │
                                  INSERT в CH ──ok──> готово (быстрый путь, Kafka не трогаем)
                                       └─fail (CH лёг)─> produce(nexus.logs.retry, {table, []LogRecord})
                                                              │  ERROR-лог (→ Sentry) + метрика
                                                              ▼
                              clog-retry consumer ── InsertBatch напрямую в CH ──ok──> commit offset
                              (группа <group>-clog-retry)        └─fail (CH ещё лёг)─> retry-in-place
```

Sync- и async-пути используют один `chlog.Writer`, поэтому §38 покрывает оба. Цикла
produce↔consume нет: retry-consumer вставляет батч **напрямую** (`InsertBatch`, в обход буфера) и при
ошибке НЕ продьюсит обратно в топик, а лишь не коммитит offset.

### 38.2 Продьюсер проваленных батчей

- Интерфейс `chlog.BatchRetrier { Retry(ctx, table, batch) error }` — определён на стороне consumer'а
  (`chlog.Writer`); реализуется `adapter/out/chlogretry.Retrier` поверх `platform/kafka.Producer`.
- При сбое `INSERT` в `flushTable`: ERROR-лог `clickhouse batch insert failed` (уходит в Sentry —
  ошибки CH видны), метрика `nexus_clickhouse_errors_total{op="insert"}`, затем `retrier.Retry(...)`.
  Успех → `INFO batch queued to kafka retry topic` + `nexus_clickhouse_fallback_total{op="queued"}`.
  Провал produce (и CH, и Kafka недоступны) → ERROR `clickhouse batch retry-produce failed` +
  `nexus_clickhouse_errors_total{op="retry_produce"}`; батч теряется (NDJSON-страховки больше нет —
  сознательный выбор: durability буфера = durability Kafka).
- **Формат сообщения** — `internal/sender/clogwire` (нейтральный leaf-пакет, общий для продьюсера и
  consumer'а): `Envelope{Table string, Logs []*domain.LogRecord}`, JSON. Одно сообщение = один
  под-батч одной таблицы; ключ = имя таблицы (порядок в пределах таблицы сохраняется в партишне).
- **Резка по размеру** (`clogwire.Split`): батч с логируемыми телами может превысить
  `max.message.bytes`; Split режет его на под-батчи под лимит (берётся `kafka.topic.max_message_bytes`).
  Ни одна запись не теряется; запись крупнее лимита уходит отдельным под-батчем.

### 38.3 Retry-consumer (дренаж обратно в CH)

- Топик `nexus.logs.retry` (config `kafka.retry_topic`, дефолт `nexus.logs.retry`; пусто → retry
  выключен, батчи теряются при сбое CH). Провижинится при старте Sender (`MustEnsureKafkaTopics`)
  с теми же параметрами, что async-топик.
- Отдельная **consumer-group** `<consumer_group>-clog-retry`, чтобы не конкурировать за партиции с
  основным async-consumer'ом.
- `adapter/in/kafka.ChLogRetryConsumer` + обработчик `ChLogRetryHandler` (декодирует конверт →
  `ChLogInserter.InsertBatch` — прямой синхронный INSERT в обход буфера, реализуется
  `chlog.WriterManager.InsertBatch`).
- **Семантика retry-in-place** (важно): kafka-go `FetchMessage` без commit'а двигает внутренний
  курсор и сам по себе **не** передоставляет сообщение на том же reader'е (только при
  rebalance/restart). Поэтому при сбое INSERT (CH ещё лежит) consumer **не пропускает** сообщение, а
  переобрабатывает **тот же** батч с экспоненциальным бэкоффом (1с→…→15с) до успеха или отмены ctx —
  и только потом коммитит offset и берёт следующий. Гарантия: батч доедет в CH сразу после
  восстановления, без потери и без необходимости рестарта Sender.
- Битое сообщение → commit (drop, не зацикливаемся на яде). Успех → `nexus_clickhouse_fallback_total{op="restored"}`.
- Lag топика `nexus.logs.retry` публикуется в `nexus_kafka_lag` (= объём проваленных батчей,
  ждущих восстановления CH) — для мониторинга.

### 38.4 Что осталось без изменений

- Поведение при недоступности CH для **отправки** — не изменилось: sync/async не блокируются (§9.4).
- Ошибки CH по-прежнему идут в **Sentry** (ERROR-уровень → slog→Sentry мост): `clickhouse batch
  insert failed` на каждом провале flush'а.
- Метрики панели при недоступности CH деградируют как раньше; read-path логов деградирует мягко
  (см. отдельную доработку: `domain.ErrLogsBackendUnavailable`, 200 + `logs_available=false`).
- При переполнении буферного канала запись по-прежнему отбрасывается
  (`nexus_clickhouse_dropped_total{reason="buffer_full"}`).

### 38.5 Конфиг и развёртывание

- Новый параметр `kafka.retry_topic` (дефолт `nexus.logs.retry`). Удалён `clickhouse.fallback_dir`.
- Топик создаётся автоматически при старте Sender; retention — общий `kafka.topic.retention_ms`
  (рассчитывайте на максимально ожидаемый простой CH × объём логов). Подробности — `DEPLOYMENT.md`.
- Scope: durable-буфер именно для **проваленных** батчей. Полный перевод логов на Kafka (сообщение
  на каждый запрос) НЕ делается — это постоянная нагрузка на Kafka ради редкого простоя CH, и плохо
  ложится на per-node-таблицы Nexus (Kafka-engine+MV не роутит по динамическому имени таблицы).
