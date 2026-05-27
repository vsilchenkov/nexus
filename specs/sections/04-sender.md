## 4. Sender Service

### 4.1 gRPC API

Один сервис `SenderService` с методом:

```protobuf
syntax = "proto3";

package nexus.sender.v1;
option go_package = "github.com/<org>/nexus/proto/sender/v1;senderv1";

rpc Send(SendRequest) returns (SendResponse);

message SendRequest {
  string id            = 1;
  string node_path     = 2;
  string target_url    = 3;
  AuthConfig auth      = 4;
  map<string,string> headers = 5;
  bytes body           = 6;
  int32 timeout_ms     = 7;
}

message SendResponse {
  int32 status_code        = 1;
  bytes body               = 2;
  map<string,string> headers = 3;
  string error             = 4;
}
```

**Версионирование gRPC.** Имя пакета содержит `v1` — это часть полного имени сервиса (`nexus.sender.v1.SenderService`), что эквивалентно префиксу `/v1/` в HTTP. При несовместимых изменениях контракта создаётся новый пакет `nexus.sender.v2` с обновлёнными сообщениями; обе версии работают параллельно, Sender регистрирует оба сервиса. `.proto`-файлы версий лежат в отдельных подкаталогах: `/proto/sender/v1/sender.proto`, `/proto/sender/v2/sender.proto` — это позволяет генерировать Go-код для обеих версий независимо.

Совместимые изменения (новые опциональные поля с новыми номерами) делаются в рамках текущей версии без создания v2.

Универсальный ответ позволяет Receiver проксировать его любому клиенту.

### 4.2 Внутреннее устройство

- Каждый входящий gRPC-запрос обрабатывается в отдельной горутине.
- Воркер-пул для исходящих HTTP-вызовов, общение через каналы.
- Kafka-consumer работает параллельно: читает `nexus.async`, для каждого сообщения делает HTTP-вызов с настроенными retry. Offset коммитится **только после успешной доставки** (HTTP 2xx или исчерпания retry с фиксацией в DLQ-топик `nexus.async.dlq`). Сообщения «в полёте» не теряются при рестарте.
- При сбое внешнего узла — экспоненциальный backoff + jitter.

### 4.3 Логирование

Каждый вызов (sync и async) пишется в ClickHouse асинхронно через канал → batch-вставка раз в N секунд. Сбой ClickHouse **не должен** ронять сервис и не должен блокировать основной поток — при недоступности логи буферизуются в памяти с ограничением размера, переполнение пишется в файл-фоллбек.

Таблица на каждый узел, схема едина:

```sql
CREATE TABLE vika_logs.{node_table}
(
  ID String,
  type String,
  url String,
  method String,
  parameters String,
  request String,
  response String,
  status Int32,
  reason String,
  date_create Date,
  date_request DateTime,
  date_response DateTime,
  duration Int32,
  done Bool,
  checksum_request FixedString(32),
  checksum_response FixedString(32),
  Host String,
  IP String,
  attempts Int32,
  attempts_details String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(date_create)
ORDER BY (date_create, date_request, method)
SETTINGS index_granularity = 8192;
```

#### Поля таблицы логов

Каждая запись описывает одно сообщение, прошедшее через шину — от приёма в Receiver до получения ответа от внешнего узла (или фиксации ошибки).

| Поле | Тип | Что хранит |
|---|---|---|
| `ID` | String | Уникальный идентификатор запроса (UUID v4), генерируется Receiver при приёме. Этот же ID возвращается клиенту в ответе async-запросов (`{"result": true, "id": "<uuid>"}`) и используется для трассировки end-to-end через все сервисы и в Sentry. |
| `type` | String | Тип обработки: `request` (синхронный, через gRPC) или `requestAsync` (асинхронный, через Kafka). Совпадает с `root_method` узла из §3.3. |
| `url` | String | **Фактический** URL, на который ушёл запрос. В режиме `url_mode = static` совпадает с `target_url`; в `from_request` — содержит значение, переданное клиентом в `url_base`. Никогда не `target_url` из конфига, если он отличается от реального адреса доставки. |
| `method` | String | HTTP-метод исходящего запроса (`POST`, `GET`, `PUT`, …). Шина пробрасывает метод входящего запроса как есть. |
| `parameters` | String | Query-string фактически отправленного запроса в виде `key1=value1&key2=value2`. Служебные параметры шины (`url_base`, `token` и т.д.) **исключены** из этого значения. Значения чувствительных параметров маскируются как `***` (см. §3.5). |
| `request` | String | Тело запроса (raw bytes как строка). Пишется только при включённом флаге `Логировать тело запроса` в настройках узла (см. §7.5). Если выключено — пустая строка. Служебные поля (например, `auth_token` для body-источника) удалены из значения. |
| `response` | String | Тело ответа внешнего узла. Пишется только при включённом флаге `Логировать тело ответа`. Если выключено или ответа нет (timeout, сетевая ошибка) — пустая строка. |
| `status` | Int32 | HTTP-статус ответа внешнего узла (200, 404, 500, …). При сетевой ошибке или таймауте — `0` (сервер не ответил вообще). |
| `reason` | String | Текстовое описание результата: для `done = true` — обычно пустая строка или `OK`; для ошибок — причина (`timeout`, `connection refused`, `DNS lookup failed`, `5xx from upstream`, `circuit breaker open`, `retry exhausted`, …). |
| `date_create` | Date | Календарная дата создания записи. Используется как ключ партиционирования (`PARTITION BY toYYYYMM(date_create)`) — старые данные удаляются помесячно через `DROP PARTITION`. |
| `date_request` | DateTime | Момент, когда Receiver принял запрос от клиента (для sync) или Sender забрал сообщение из Kafka (для async). Точность — до секунды. |
| `date_response` | DateTime | Момент, когда был получен ответ от внешнего узла (или зафиксирована ошибка). Для запросов с таймаутом — момент истечения таймаута. |
| `duration` | Int32 | Время обработки в миллисекундах = `date_response - date_request`. Включает все retry, backoff'ы, ожидание в очереди воркеров. Это основная метрика для p95 latency, видимая в UI и Prometheus. |
| `done` | Bool | Финальный результат: `true` — сообщение доставлено внешнему узлу и получен HTTP 2xx; `false` — любая ошибка (4xx, 5xx, таймаут, retry исчерпан, ушло в DLQ). Используется для расчёта success rate. |
| `checksum_request` | FixedString(32) | MD5-хеш тела входящего запроса. Позволяет дедуплицировать одинаковые запросы (одинаковый payload) и группировать в UI. Считается до маскирования чувствительных полей, по оригинальному содержимому. |
| `checksum_response` | FixedString(32) | MD5-хеш тела ответа внешнего узла. Полезен для аудита: если два разных запроса получили одинаковый ответ, checksum совпадёт. |
| `Host` | String | Имя инстанса сервиса, обработавшего запрос (hostname контейнера). Нужно для трассировки в горизонтально масштабированной среде — какой именно Receiver или Sender этим занимался. |
| `IP` | String | IP-адрес клиента, инициировавшего запрос. Берётся из `X-Forwarded-For` (если шина за обратным прокси) или из `RemoteAddr` HTTP-соединения. Используется для аналитики, rate-limit и анти-фрод. |
| `attempts` | Int32 | Сколько попыток доставки выполнено суммарно. `1` — успех с первой попытки или единственная failed-попытка без retry. `≥ 2` — было N-1 ретраев. Удобно для алертов «узел работает, но с N-й попытки». |
| `attempts_details` | String | JSON-массив с детализацией каждой попытки. Пишется только когда `attempts > 1` или `done = false` (для успехов с первой попытки — пустая строка, чтобы не раздувать таблицу). Структура одного элемента: `{"n": <номер попытки>, "started_at": "<iso8601 ms>", "duration_ms": <int>, "status": <int>, "reason": "<string>", "backoff_before_ms": <int>, "auth_used": "<basic\|token\|none>", "circuit_breaker_state": "<closed\|open\|half_open>"}`. Чувствительные значения (значения токенов, пароли) в JSON не попадают — только тип авторизации. Поле используется при разборе инцидентов: «почему запрос N упал на 3-й попытке, какой был backoff, был ли активирован circuit-breaker». |

**Партиционирование и retention.** Партиции по месяцу (`PARTITION BY toYYYYMM(date_create)`). Глобального TTL на таблицу нет — старые данные удаляются командой `ALTER TABLE … DROP PARTITION 'YYYYMM'`, которую запускает отдельный housekeeping-cron в Sender по политике из конфигурации узла (по умолчанию хранение 90 дней). Это даёт контроль над архивацией: партицию можно перед удалением выгрузить в S3 или другую систему.

**Сортировка.** Первичный ключ `(date_create, date_request, method)` оптимизирован под типичный запрос «дай мне записи за последние N часов по узлу X», который доминирует в UI и метриках. Поиск по `ID` использует skip-index на дополнительном поле (создаётся миграцией).

