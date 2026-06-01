## 27. Тип узла RabbitMQAsync (забор из RabbitMQ)

Раздел вводит **третий тип узла** — `RabbitMQAsync`. В отличие от `request` / `requestAsync` (§3.2),
которые ждут, что **клиент сам постучится** в Nexus по HTTP, узел `RabbitMQAsync` **сам периодически
забирает** сообщения из заданной очереди RabbitMQ, кладёт их в Kafka `nexus.async`, после чего Sender
обрабатывает их тем же конвейером, что и обычный `requestAsync` (внешний HTTP-вызов, лог в ClickHouse,
DLQ при отказе).

> Источник макета UI — `specs/nexus_rabbitmq_ui.html`. Исторический черновик —
> `specs/nexus_rabbitmq_spec.md` (в устаревшем нейминге); этот раздел — каноничная версия.

### 27.1. Зачем нужен этот тип узла

`request` / `requestAsync` покрывают **push-сценарии**: внешняя система шлёт нам события по HTTP.
В реальности часто бывает обратное — события уже лежат в **корпоративной шине** (RabbitMQ), а нужно
переложить их в HTTP-вызов к партнёру. Сейчас для этого приходится поднимать отдельный прокси-сервис
RabbitMQ → HTTP, который читает из очереди и стучится в Nexus, — лишний хоп, лишний код, лишняя точка
отказа.

`RabbitMQAsync` убирает прокси: Nexus сам читает из RabbitMQ, публикует в свою Kafka, Sender отправляет
дальше. **Единая трассировка, единое логирование, единая ретрай-политика и DLQ.** Это не замена
ESB-инструментам со сложной маршрутизацией по routing key и трансформацией payload — здесь Nexus
выступает простым forwarder'ом.

### 27.2. Поведение узла

**Жизненный цикл.** При создании узла `RabbitMQAsync` Web Service вставляет запись в `nodes`, а
фоновый компонент **Puller** (живёт в Receiver-сервисе) поднимает один воркер на этот узел. Один
воркер на узел, без шардинга — для типовой нагрузки 1–500 сообщений/сек этого с запасом (шардинг и
leader election — v2, §27.14).

**Цикл забора.** Воркер крутится в цикле с интервалом `pull_interval_sec`:

1. Открывает (или переиспользует) AMQP-соединение и канал к настроенной очереди; выставляет
   `basic.qos(prefetch=pull_prefetch)`.
2. Забирает до `pull_batch_size` сообщений через `basic.get` с **manual ack**.
3. Для каждого сообщения формирует Kafka-envelope (§27.3) и публикует в топик `nexus.async` с ключом
   партиционирования `node.Path` — тем же, что и обычный `requestAsync`.
4. После подтверждения `acks=all` от Kafka — `basic.ack` в RabbitMQ.
5. При ошибке публикации в Kafka — `basic.nack` с `requeue=true`; сообщение остаётся в очереди и
   попадёт на следующую итерацию.
6. После обработки батча спит `pull_interval_sec` секунд и идёт на следующую итерацию.

**Гарантии — at-least-once.** Одно и то же сообщение **может быть отправлено дважды** при сбое между
Kafka-ack и RabbitMQ-ack (сценарий «published-but-not-acked»). Exactly-once потребовала бы внешней
дедупликации по `message_id` — это out of scope в v1 (§27.14). Если внешний узел не идемпотентен —
дедупликация на его стороне.

**Отказы.**

| Отказ | Поведение |
|---|---|
| RabbitMQ недоступен | Переподключение с экспоненциальным backoff (1с → 2с → 4с → … → 60с потолок). Узел **не** считается упавшим — это норма для распределённых брокеров. `nexus_rmq_connection_state{node}` показывает состояние. |
| Очередь не существует | `error` в логи, узел переходит в runtime-состояние `degraded` (§27.4), повтор раз в 60с. В UI — красный баннер «Очередь не найдена». |
| Kafka недоступна | `basic.nack(requeue=true)` всего батча, ждём восстановления Kafka (Receiver уже умеет это для `requestAsync`, §9.4). Потерь нет. |
| Сообщение > `max_message_bytes` (10 МБ, §5.3) | `basic.reject` без `requeue` (уходит в DLQ RabbitMQ или удаляется — зависит от настроек очереди). Запись в Sentry с `op="puller.rejectOversize"`. |

### 27.3. Маппинг RabbitMQ → Kafka envelope

Используется тот же `Envelope`, что для `requestAsync` (`internal/receiver/usecase/envelope.go`), с
добавлением служебного блока `rmq`. Для `RabbitMQAsync`:

- `method` — всегда `"POST"` (другие методы — v2, §27.14);
- `target_url` / `auth_header` — резолвятся как для `requestAsync` (статичный URL + статичная исходящая
  авторизация);
- `client_ip` — заполняется как `"rabbitmq://<host>:<port>/<vhost>"` (семантически — источник сообщения,
  см. §27.10);
- `headers` — `Content-Type` из AMQP-свойства `content_type` (иначе `application/octet-stream`); из
  AMQP-`headers` пробрасываются **только** перечисленные в `forward_headers` узла; автоматически
  добавляются `X-Nexus-Source: rabbitmq` и `X-Nexus-Routing-Key: <routing_key>`;
- `rmq` — новый блок:

```json
{
  "id": "<uuid>",
  "node_path": "billing-events",
  "method": "POST",
  "target_url": "https://api.partner.com/billing/webhook",
  "client_ip": "rabbitmq://rmq.internal:5672//",
  "headers": { "Content-Type": "application/json", "X-Nexus-Source": "rabbitmq" },
  "body": "<bytes>",
  "received_at": "2026-05-31T14:32:18Z",
  "rmq": {
    "exchange": "orders-exchange",
    "routing_key": "order.created",
    "delivery_tag": 42,
    "message_id": "msg-abc-123",
    "timestamp": "2026-05-31T14:32:18Z"
  }
}
```

### 27.4. Состояние `degraded` (runtime, не персистентное)

`degraded` — **runtime-состояние воркера**, а не значение `node.status`. Enum `NodeStatus`
(`enabled`/`disabled`/`paused`, §3.6) и его CHECK-constraint **не расширяются**: смешивать
конфигурационный статус (что выставил пользователь) с health (что наблюдает воркер) нельзя — иначе
`degraded` затирал бы `paused`, а воркер писал бы в конфиг.

- `degraded` поднимается воркером, когда RabbitMQ недоступен дольше **5 минут** или очередь не найдена.
- Отдаётся в API отдельным полем (health-снимок воркера, см. §27.8), не меняя `status`.
- Метрика `nexus_node_degraded{node, reason}` = 1.
- В UI узел подсвечивается красным с причиной и числом неудачных попыток.
- Снимается **автоматически** после восстановления связи (узел продолжает работать в своём `status`).
- Применимо только к pull-узлам (`RabbitMQAsync`).

### 27.5. Удаление узла и graceful shutdown

При удалении узла или `SIGTERM` воркер останавливается корректно: дожидается завершения текущего батча
(таймаут **10 секунд**), `basic.nack(requeue=true)` всех необработанных сообщений, закрывает AMQP-канал
и соединение. Только после этого запись удаляется из `nodes`. Это страхует от потери сообщений в момент
остановки.

### 27.6. Конфигурация узла

В дополнение к полям §3.3, для `root_method = RabbitMQAsync`:

| Поле | Тип | Дефолт | Лимиты | Назначение |
|---|---|---|---|---|
| `rmq_host` | varchar(253) | — | 1–253, hostname/IP | Хост RabbitMQ (или DNS кластера) |
| `rmq_port` | int | 5672 | 1–65535 | Порт (5672 AMQP / 5671 AMQPS) |
| `rmq_vhost` | varchar(255) | `/` | — | Virtual host |
| `rmq_user` | varchar(255) | — | — | Логин AMQP |
| `rmq_password` | text (шифр) | — | — | Пароль; AES-256-GCM как `auth_credentials` (§5.5) |
| `rmq_queue` | varchar(255) | — | 1–255, `^[a-zA-Z0-9._-]+$` | Имя очереди |
| `rmq_use_tls` | bool | false | — | AMQPS вместо AMQP |
| `pull_interval_sec` | int | 5 | 1–3600 | Интервал между batch-fetch'ами |
| `pull_batch_size` | int | 100 | 1–1000 | Максимум сообщений за итерацию |
| `pull_prefetch` | int | = `pull_batch_size` | 1–1000 | AMQP `basic.qos` prefetch count |

**Игнорируемые поля** для этого типа: `incoming_auth_type` / `incoming_auth_credentials` (нет HTTP-входа
— некого авторизовывать), `url_mode = from_request` и связанные (нет входящего запроса — нечего
разбирать). Если пользователь сохраняет узел с этими настройками — backend молча сбрасывает их в
`none` / `static`, а в audit-логе пишет `details.cleared_incompatible_fields: [...]`.

**Исходящая авторизация** работает как для `requestAsync`: `none` / `basic` / `token` со статичными
кредами. Динамические варианты (`*_from_request`) недоступны — выпадают из селекта.

### 27.7. PostgreSQL — изменения

Расширение списка методов (таблица `methods`, FK из `nodes.root_method`, §5.1):

```sql
ALTER TABLE methods DROP CONSTRAINT methods_name_check;
ALTER TABLE methods ADD CONSTRAINT methods_name_check
    CHECK (name IN ('request', 'requestAsync', 'RabbitMQAsync'));
INSERT INTO methods (name) VALUES ('RabbitMQAsync') ON CONFLICT DO NOTHING;
```

Новые колонки в `nodes` — все nullable (для `request` / `requestAsync` хранят NULL), плюс constraint
целостности RabbitMQ-узла:

```sql
ALTER TABLE nodes
    ADD COLUMN rmq_host          VARCHAR(253),
    ADD COLUMN rmq_port          INTEGER,
    ADD COLUMN rmq_vhost         VARCHAR(255),
    ADD COLUMN rmq_user          VARCHAR(255),
    ADD COLUMN rmq_password      TEXT,
    ADD COLUMN rmq_queue         VARCHAR(255),
    ADD COLUMN rmq_use_tls       BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN pull_interval_sec INTEGER,
    ADD COLUMN pull_batch_size   INTEGER,
    ADD COLUMN pull_prefetch     INTEGER;

ALTER TABLE nodes ADD CONSTRAINT chk_rmq_fields CHECK (
    root_method <> 'RabbitMQAsync' OR (
        rmq_host  IS NOT NULL AND char_length(rmq_host)  > 0 AND
        rmq_queue IS NOT NULL AND char_length(rmq_queue) > 0 AND
        pull_interval_sec BETWEEN 1 AND 3600 AND
        pull_batch_size   BETWEEN 1 AND 1000
    )
);
```

Миграция — `0014_rmq_async_node.{up,down}.sql`. Это защищает от полу-настроенного RabbitMQ-узла на
уровне БД (дублирует доменную `Node.Validate()`).

### 27.8. Web API — изменения

**Тест подключения** — `POST /api/nodes/test-rmq` (доступ — **manager+**, как CRUD узлов, §26;
rate-limit **10 запросов/мин на пользователя**, иначе зажатая кнопка «Проверить» устроит DoS на
RabbitMQ):

```jsonc
// Body
{ "host": "rmq.internal", "port": 5672, "vhost": "/",
  "user": "nexus", "password": "...", "queue": "billing-events", "use_tls": false }
// Response (всегда 200)
{ "ok": true,
  "checks": {
    "connect": { "ok": true, "elapsed_ms": 42 },
    "auth":    { "ok": true, "elapsed_ms": 18 },
    "queue":   { "ok": true, "message_count": 1248, "consumer_count": 0 } } }
```

Эндпоинт: AMQP-connect → auth → `queue.declare` с `passive=true` (проверяет существование очереди, не
создавая её и не получая прав на запись; **не** `basic.get`/`consume` — они меняют состояние очереди) →
close. При любой ошибке возвращает **200 OK** с `ok:false` и детализацией по шагам — это диагностический,
а не функциональный эндпоинт; UI рендерит результат, а не интерпретирует HTTP-статус. В audit **не**
пишется (диагностика). Если в `password` пусто, но узел уже существует — usecase может взять текущий
расшифрованный пароль (для повторной проверки без ввода).

**Health-снимок воркера** для UI — `GET /api/nodes/:id` (для `RabbitMQAsync` в `NodeResponse`
добавляется блок `rmq_status: { degraded, reason, connection_state, queue_depth, consumer_count,
attempts, since }`). Источник — снимок Puller-manager'а; межсервисный канал Receiver→Web — общий стор
(Redis), без нового gRPC.

**Существующие эндпоинты** (`GET/POST/PUT/DELETE /api/nodes`) принимают и возвращают новые поля.
`rmq_password` в JSON-ответе всегда отсутствует; вместо него флаг `rmq_password_set: bool` (как
`auth_credentials_set`). В PUT клиент отправляет `rmq_password` только если хочет его поменять; пустое
значение — оставить старое.

### 27.9. Метрики Prometheus

Добавляются (префикс `nexus_`, см. §6):

```
nexus_rmq_messages_pulled_total{node, status="ok|nack|reject"}   # counter
nexus_rmq_pull_duration_seconds{node}                            # histogram
nexus_rmq_connection_state{node}                                 # gauge: 0=down,1=connecting,2=up
nexus_rmq_queue_depth{node}                                      # gauge, обновляется на каждом тике
nexus_rmq_consumer_count{node}                                   # gauge, число consumer'ов на очереди
nexus_node_degraded{node, reason}                                # gauge: 1 при degraded
```

`queue_depth` особенно полезен: если он растёт быстрее, чем Nexus вынимает, узел не справляется — нужно
увеличить `pull_batch_size` либо уменьшить `pull_interval_sec`.

### 27.10. Логирование (ClickHouse)

В логе узла (§4.3): поле `IP` = `rabbitmq://<host>:<port>/<vhost>` (формально не IP, но семантически —
источник сообщения; позволяет фильтровать по типу источника без новых колонок); `method` = `POST`
(реальный исходящий метод); `type` = `RabbitMQAsync` (как `root_method` узла) — это даёт в одном запросе
ClickHouse различать сообщения из RabbitMQ vs обычного `requestAsync`. Значения приходят из envelope —
Sender не требует изменений сверх передачи `Type = node.RootMethod` и `IP = env.ClientIP`.

### 27.11. UI

- Селектор типа узла — **три карточки** (`PickGroup`): `request` / `requestAsync` / `RabbitMQAsync`
  (последняя с бейджем «NEW»). Выбор определяет, какие секции формы раскрываются.
- При выборе `RabbitMQAsync`: секция «RabbitMQ — источник» (хост/порт/TLS, vhost, логин/пароль,
  очередь) + кнопка «Проверить подключение» (`POST /api/nodes/test-rmq`, рендер трёх статусов
  connect/auth/queue с временем и `message_count`); секция «Параметры забора» (`pull_interval_sec` с
  пресетами, `pull_batch_size`, `pull_prefetch`). Скрываются блоки «Входящая авторизация» и
  `url_mode = from_request`.
- Detail-страница: KPI-блоки для `RabbitMQAsync` — глубина очереди, скорость забора, p95 «от очереди до
  получателя», ошибки доставки. При `degraded` — красный баннер с причиной, числом попыток и кнопками
  «Попробовать сейчас» / «Перепроверить настройки».
- i18n: ключи в backend (`internal/platform/i18n/i18n.go`) и frontend (`web-ui/src/locales/{en,ru}.json`).

### 27.12. Сценарные тесты

**e2e integration** (testcontainers: RabbitMQ + Kafka + ClickHouse + mock-HTTP). Сценарий:
создать `RabbitMQAsync`-узел через Web API → опубликовать N сообщений в очередь → Puller забирает →
проверить: (а) доставку на mock-сервер; (б) запись в ClickHouse с `type=RabbitMQAsync` и
`IP=rabbitmq://…`; (в) **отсутствие потерь** (число CH-логов = числу опубликованных). Дополнительные
кейсы: Kafka недоступна → сообщения остаются в очереди (`basic.nack requeue`), потерь нет; очередь не
существует → узел в `degraded`, метрика `nexus_node_degraded=1`. Запуск — `make test-integration`.

**Расширение сценарного теста производительности** (§10.2, `cmd/loadtest`): флаг `--ratio-rmq` — доля
узлов типа `RabbitMQAsync`; генератор для них публикует часть нагрузки **напрямую в RabbitMQ-очереди**
(а не через HTTP). Критерий «нет потерь» проверяется так же: число записей в ClickHouse-логе ≥ числа
опубликованных в очереди сообщений. Процедура запуска — в `TESTING.md`.

### 27.13. Критерии приёмки (дополнение к §15)

- При создании узла с `root_method = RabbitMQAsync` в форме появляется секция RabbitMQ-подключения,
  скрываются `incoming_auth_*` и блок `url_mode`.
- Кнопка «Проверить подключение» делает реальный AMQP-handshake через `/api/nodes/test-rmq` и возвращает
  три статуса (connect / auth / queue) с временем и `message_count` для существующей очереди.
- Воркер забирает сообщения с заданной периодичностью, публикует в Kafka, `basic.ack` только после
  `acks=all`. Сбой Kafka между ack-Kafka и ack-RabbitMQ → дубль; сбой до ack-Kafka → requeue без потери.
- При недоступности RabbitMQ воркер переподключается с backoff; узел переходит в runtime-`degraded` через
  5 минут безуспешных попыток; после восстановления `degraded` снимается автоматически.
- Удаление узла дожидается текущего батча (≤10с), `basic.nack` необработанных, закрывает канал.
- `rmq_password` шифруется AES-256-GCM по схеме `auth_credentials`; в API — `rmq_password_set`, в
  логах/Sentry маскируется.
- Constraint `chk_rmq_fields` блокирует сохранение RabbitMQ-узла без host/queue/валидного интервала.
- Audit: `node.create` / `node.update` / `node.delete` с `details.root_method = RabbitMQAsync`; тест
  подключения в audit не пишется.

### 27.14. Out of scope в v1 / планы на v2

- **Exactly-once** через дедупликацию по `message_id` (в v1 — at-least-once).
- **HTTP-методы кроме POST** (PUT/PATCH с маппингом из routing key/headers — v2).
- **Динамический URL / авторизация** из тела или AMQP-заголовков (в v1 — статичные).
- **Несколько очередей на узел** (один узел = одна очередь; нужно больше — заведите несколько узлов).
- **Шардинг pull-воркера** между инстансами Receiver'а (нужен leader election — v2).
- **Push-режим** (`basic.consume` без поллинга) как альтернатива (в v1 — поллинг `basic.get`).
- **Exchange + bindings через UI** (в v1 узел подписывается на готовую очередь, созданную заранее).
