# 46. Персистентный статус «Down» узла через Redis (переживает рестарт)

Раздел делает индикатор «Down/OK» узла (§41) **персистентным**: исход последнего исходящего вызова
хранится в Redis, а не только в in-memory Prometheus-гаудже. После рестарта/деплоя Sender'а или Web
статус остаётся корректным, а не сбрасывается в «OK». Развивает §41 (пересмотр статуса «Down») и §44.A
(per-node throughput дашборда).

## 46.1 Проблема (подтверждена на бою)

§41 определяет «Down» по Prometheus-гауджу `nexus_node_last_request_error` (`SetNodeLastRequestError`
в [metrics.go](../../internal/platform/metrics/metrics.go), ставится Sender'ом на каждом исходящем
вызове — [sender_service.go](../../internal/sender/adapter/in/grpc/sender_service.go) sync и
[async.go](../../internal/sender/usecase/async.go)); Web читает его через Prometheus
([prometheus/client.go](../../internal/web/adapter/out/prometheus/client.go) `NodeLastErrors` →
[metrics.go](../../internal/web/usecase/metrics.go) `applyLastErrors`).

Гаудж — **in-memory в процессе Sender**. При рестарте (деплой новой версии, перезапуск пода) серия
протухает, и Web-запрос возвращает пусто → код трактует как `last_error=false` → **все узлы
показывают «OK» независимо от исхода последнего реального запроса**, пока не придёт новый трафик.

**Боевой кейс (2026-06-26):** узел `statusnpd` (`5b00cdb5-…`). Последний лог `09:02:17` —
`status=0, done=false` («connection reset by peer» к ФНС). В ~11:00 выкатили v1.9.1 → процесс
рестартанул → `metrics/nodes` отдаёт `last_error=false` → дашборд красит узел «OK», хотя последний
запрос упал. `prometheus_available=true` (дело не в недоступности Prometheus — именно потеря гауджа).

## 46.2 Решение

Sender при каждом исходящем вызове **дополнительно** пишет исход в Redis (общий сервис, переживает
рестарт любого из процессов). Web читает «Down» из Redis. Prometheus-гаудж остаётся (для
Prometheus-алертов/дашбордов), но источник истины для UI-бейджа — Redis.

- **Ключ:** `nexus:node:last_error:<nodePath>` (по пути узла — тем же лейблом, что у гауджа §41 и
  ключом матчинга `le[row.Node]` в Web). Значение — исход: флаг ошибки + время + код статуса (Redis
  hash `{err: "0|1", at: <unixms>, status: <int>}` либо строка `"1|<unixms>|<status>"`).
- **Определение ошибки** — как в §41/§44: `status == 0 || status >= 400` (не доставлено). Sender уже
  вычисляет `isErr` в точках вызова `SetNodeLastRequestError`.
- **TTL:** долгий или без TTL — значение представляет «исход последнего вызова», живёт до перезаписи
  следующим вызовом. Чтобы не копить ключи удалённых узлов — TTL ~30 суток ИЛИ удаление ключа при
  удалении узла (housekeeping, ниже приоритет).
- **Бонус — мульти-реплики.** Сейчас при нескольких Sender'ах каждая реплика держит своё значение
  гауджа, и Web берёт `max by(node)` (§41-caveat). С общим Redis-ключом все реплики пишут один ключ
  (last-writer-wins = «последний исход» в реальном времени), и Web читает одно консистентное значение.

## 46.3 Слои (Clean Architecture, два сервиса делят Redis)

Интерфейсы — на стороне consumer'а, обе реализации в `adapter/out/redis` (Sender уже имеет
`*goredis.Client`, nil при отсутствии Redis — [sender/app.go](../../internal/sender/app.go); Web —
тоже).

- **Sender (writer).** Узкий порт `NodeStatusWriter.SetLastError(ctx, nodePath string, errored bool,
  at time.Time, status int) error`. Реализация — новый Sender-Redis-адаптер. Вызывается там же, где
  `SetNodeLastRequestError` (sync `sender_service.go`, async `async.go`), **fire-and-forget**:
  не блокирует ответ, ошибку Redis логируем (не валим запрос). При `redis == nil` — пропуск (гаудж
  по-прежнему ставится, поведение = §41).
- **Web (reader).** Порт `NodeStatusReader.GetLastErrors(ctx, nodePaths []string) (map[string]bool,
  error)`. Реализация — Web-Redis-адаптер (`adapter/out/redis`, MGET по путям). `applyLastErrors`
  читает **сначала Redis**; при недоступности Redis или пустом ответе — **fallback на Prometheus**
  `NodeLastErrors` (текущее поведение, чтобы не регрессировать без Redis). Деградация мягкая: нет ни
  Redis, ни Prometheus → «Down»-оверлей не проставляется (как сейчас).

## 46.4 Поведение после деплоя

Redis-ключи переживают рестарт Sender/Web → `statusnpd` из кейса §46.1 **сразу** после деплоя
показывал бы «Down» (последний вызов 09:02 был ошибкой), а не ложный «OK». Бейдж снова станет «OK»
только когда реальный запрос завершится успешно.

## 46.5 Анализ остальных in-memory метрик (почему только last_request_error)

Проверен весь реестр [metrics.go](../../internal/platform/metrics/metrics.go). Критерий «нужна
персистентность в Redis» = метрика хранит **накопленное/последнее состояние, заданное событием и НЕ
переderives живым циклом** после рестарта. Подходит **только `nexus_node_last_request_error`**
(event-driven: ставится на запросе, между запросами не пересчитывается → после рестарта 0 до нового
трафика). Остальное Redis не требует:

- **Counters (`*_total`)** — `requests`, `request_incomplete`, `loop_detected`, `ratelimit_check_errors`,
  `clickhouse_errors/dropped/fallback`, `l2_cache_*`, `rmq_messages_pulled`, `dlq_reprocess`: Prometheus
  `increase()`/`rate()` корректно обрабатывают сброс счётчика при рестарте.
- **Histograms (`*_seconds`)** — `request_duration`, `kafka_produce_duration`, `rmq_pull_duration`,
  `dlq_reprocess_duration`: распределение; `rate`/`histogram_quantile` за окно работают после сброса.
- **Live-gauges (пересчитываются циклом)** — `kafka_lag`, `kafka_in_flight`, `clickhouse_buffer_size`,
  `l2_cache_size`, `rmq_connection_state`, `rmq_queue_depth`, `rmq_consumer_count`: сэмплируются живым
  воркером каждый тик из реального источника → свежее значение после рестарта корректно (персистить
  было бы неверно).
- **`nexus_node_degraded`** (borderline) — тоже re-derived, но непрерывным циклом puller'а (re-set
  каждый тик), а не событием → самовосстанавливается за тик; Redis не нужен.

## 46.x Scope / неочевидности

- **Переименование узла** осиротит ключ (ключ по пути). Допустимо: новый путь начнёт писать свой
  ключ с первого запроса; старый истечёт по TTL. Если позже понадобится — перейти на ключ по ID
  узла (стабилен), но тогда Sender должен знать ID (сейчас в точках вызова есть только path).
- **Prometheus-гаудж не удаляем** — он остаётся источником для Prometheus-алертинга; §46 меняет лишь
  источник UI-бейджа «Down».
- **Один SET на запрос** в Sender (fire-and-forget) и один MGET на загрузку дашборда в Web — дёшево;
  на горячий путь запроса латентность не добавляем (запись вне критической секции ответа).
- **Без Redis** (`redis == nil` в Sender / недоступен в Web) — поведение ровно как в §41 (гаудж,
  теряется при рестарте). §46 — улучшение, а не новая жёсткая зависимость.
