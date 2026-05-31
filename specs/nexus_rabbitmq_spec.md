# Nexus — ТЗ-фрагмент: тип узла «RabbitMQAsync»

Этот файл — дополнение к основному ТЗ (`data_bus_spec.md`). Описывает третий тип узла: **RabbitMQAsync** — узел, который сам периодически забирает сообщения из RabbitMQ и перекладывает их в Kafka. Дальше Sender обрабатывает их так же, как сообщения от `requestAsync`: достаёт из Kafka, отправляет на внешний URL.

Где встраивается в основное ТЗ: §3.2 (семантика методов), §3.3 (конфигурация узла), §5.1 (PostgreSQL — поля), §7.5 (форма создания узла), §9 (отказоустойчивость), §15 (критерии приёмки).

---

## 1. Зачем нужен этот тип узла

`request` и `requestAsync` ждут, что **клиент сам постучится** в Nexus по HTTP. Это покрывает push-сценарии (внешняя система шлёт нам события).

В реальности часто бывает обратное: события уже лежат в **корпоративной шине** (RabbitMQ), а нужно переложить их в HTTP-вызов к партнёру. Сейчас для этого приходится поднимать прокси-сервис, который читает из RabbitMQ и стучится в Nexus по HTTP — лишний хоп, лишний код, лишняя точка отказа.

**RabbitMQAsync** убирает прокси: Nexus сам читает из RabbitMQ, кладёт в свою Kafka, Sender отправляет дальше. Единая трассировка, единое логирование, единая ретрай-политика.

Это **не** замена RabbitMQ → HTTP-моста для тех, кому нужна сложная маршрутизация по routing key с трансформацией payload — для таких случаев есть отдельные ESB-инструменты. Nexus здесь — простой forwarder.

---

## 2. Поведение узла

### 2.1 Жизненный цикл

При создании узла типа `RabbitMQAsync` Web Service вставляет запись в `nodes`, отдельный фоновый компонент **Puller** (живёт в Receiver-сервисе) поднимает воркер на этот узел. Один воркер на узел, без шардинга — для типовой нагрузки 1–500 сообщений/сек это с большим запасом.

### 2.2 Цикл забора

Воркер крутится в цикле с интервалом `pull_interval_sec`:

1. Открывает (или переиспользует) AMQP-канал к настроенной очереди.
2. Забирает до `pull_batch_size` сообщений через `basic.get` (или короткоживущий consumer) с **manual ack**.
3. Для каждого сообщения: формирует Kafka-envelope (см. §2.4) и публикует в топик `databus.async` с тем же `node_path`-ключом, что и обычный `requestAsync`.
4. После успешного `acks=all`-подтверждения от Kafka — `basic.ack` в RabbitMQ.
5. При ошибке публикации в Kafka — `basic.nack` с `requeue=true`. Сообщение остаётся в очереди, попадёт на следующую итерацию.
6. После обработки батча — спит `pull_interval_sec` секунд, идёт на следующую итерацию.

**Гарантии.** At-least-once: одно и то же сообщение **может быть отправлено дважды** при сбое между Kafka-ack и RabbitMQ-ack (это сценарий «published-but-not-acked»). Для exactly-once нужна была бы внешняя дедупликация по `message_id`, и это **out of scope в v1** (см. §6).

### 2.3 Отказы

- **RabbitMQ недоступен.** Воркер переподключается с экспоненциальным backoff (1с → 2с → 4с → ... → 60с потолок). Узел не считается «упавшим» — это нормальное поведение для распределённых брокеров. Метрика `databus_rmq_connection_state{node}` показывает текущее состояние (`connecting` / `connected` / `failed`).
- **Очередь не существует.** Воркер пишет `error` в логи, ставит узел в состояние `degraded` (новое, см. §2.5), пытается заново раз в 60 секунд. В UI узел подсвечивается красным с подсказкой «Очередь не найдена».
- **Kafka недоступна.** То же поведение, что у обычного `requestAsync` — Receiver уже умеет с этим работать (см. §9.4). Воркер делает `nack` всех сообщений батча с `requeue=true`, ждёт восстановления Kafka.
- **Сообщение слишком большое.** Если размер превышает `kafka.topic.max_message_bytes` (10 МБ, §5.3) — `basic.reject` без `requeue` (сообщение уходит в DLQ RabbitMQ или удаляется, в зависимости от настроек очереди). Запись в Sentry с `op = "puller.rejectOversize"`.

### 2.4 Маппинг RabbitMQ → Kafka envelope

Kafka-envelope для RabbitMQAsync — тот же JSON-формат, что для `requestAsync`, плюс служебный блок `rmq`:

```json
{
  "id": "<uuid>",
  "node_path": "billing-events",
  "method": "POST",
  "body": "<base64 of message body>",
  "headers": {
    "Content-Type": "application/json",
    "X-Source": "rabbitmq:orders-exchange"
  },
  "rmq": {
    "exchange": "orders-exchange",
    "routing_key": "order.created",
    "delivery_tag": "...",
    "message_id": "msg-abc-123",
    "timestamp": "2026-05-30T14:32:18Z"
  }
}
```

Headers формируются так:
- `Content-Type` — берётся из AMQP-свойства `content_type`, если задано, иначе `application/octet-stream`.
- Если в AMQP-сообщении есть `headers` — пробрасываются в HTTP-headers исходящего запроса **только те, что перечислены в `forward_headers` узла** (см. §3.3 основного ТЗ).
- Служебные заголовки `X-Nexus-Source: rabbitmq`, `X-Nexus-Routing-Key: <routing_key>` добавляются автоматически.

HTTP-метод — всегда `POST`. Если внешний узел требует `PUT`/`PATCH` — это пока **out of scope** (см. §6).

### 2.5 Состояния узла

К существующим `enabled` / `disabled` / `paused` (см. §3.6) для RabbitMQAsync добавляется четвёртое:

- **`degraded`** — узел технически активен, но не может работать: RabbitMQ недоступен дольше 5 минут или очередь не найдена. Это автоматическое состояние, его не выставляет пользователь. В UI узел подсвечивается красным с подсказкой о причине. Метрика `databus_node_degraded{node, reason}` поднимается в 1. После восстановления связи воркер автоматически возвращает узел в `enabled`.

Это состояние применимо только к pull-узлам (RabbitMQAsync). Для `request` / `requestAsync` оно не выставляется.

### 2.6 Удаление узла

При удалении узла типа RabbitMQAsync воркер корректно останавливается: дожидается завершения текущего батча (с таймаутом 10 секунд), `basic.nack` всех необработанных, закрывает AMQP-канал. Только после этого запись удаляется из `nodes`. Это страхует от потери сообщений в момент удаления.

---

## 3. Конфигурация узла

В дополнение к полям §3.3 основного ТЗ, для `root_method = RabbitMQAsync`:

| Поле | Тип | Назначение |
|---|---|---|
| `rmq_host` | varchar(253) | Хост RabbitMQ (или DNS-имя кластера) |
| `rmq_port` | int | Порт, по умолчанию 5672 (AMQP) или 5671 (AMQPS) |
| `rmq_vhost` | varchar(255) | Virtual host, по умолчанию `/` |
| `rmq_user` | varchar(255) | Логин для AMQP-аутентификации |
| `rmq_password` | text | Пароль; шифруется AES-256-GCM как `auth_credentials` (см. §5.5 основного ТЗ) |
| `rmq_queue` | varchar(255) | Имя очереди, из которой забираются сообщения |
| `rmq_use_tls` | bool | Использовать AMQPS вместо AMQP |
| `pull_interval_sec` | int | Интервал между batch-fetch'ами, 1–3600 секунд (по умолчанию 5) |
| `pull_batch_size` | int | Максимум сообщений за одну итерацию, 1–1000 (по умолчанию 100) |
| `pull_prefetch` | int | AMQP basic.qos prefetch count, 1–1000 (по умолчанию равен `pull_batch_size`) |

**Поля, которые игнорируются** для этого типа: `incoming_auth_type`, `incoming_auth_credentials` (нет HTTP-входа — некого авторизовывать), `url_mode = from_request` и связанные (нет входящего HTTP-запроса с query-параметрами — нечего разбирать). Если пользователь сохраняет узел с этими настройками — backend их сбрасывает в `none` / `static` молча, в audit-логе пишет `details.cleared_incompatible_fields: [...]`.

**Исходящая авторизация** работает как для `requestAsync`: `none` / `basic` / `token` со статичными кредами. Динамические варианты (`*_from_request`) недоступны — выпадают из селекта.

**Лимиты** (см. таблицу лимитов в §3.3 основного ТЗ):

| Поле | Ограничение |
|---|---|
| `rmq_host` | 1–253 символа, валидный hostname или IP |
| `rmq_queue` | 1–255 символов, regex `^[a-zA-Z0-9._-]+$` (стандарт RabbitMQ) |
| `pull_interval_sec` | 1–3600 |
| `pull_batch_size` | 1–1000 |

---

## 4. PostgreSQL — изменения

В таблицу `nodes` добавляются колонки из §3 выше. Все nullable; для типов `request` / `requestAsync` хранят NULL. Constraint:

```sql
ALTER TABLE nodes ADD CONSTRAINT chk_rmq_fields
  CHECK (
    (root_method != 'RabbitMQAsync') OR (
      rmq_host IS NOT NULL AND
      rmq_queue IS NOT NULL AND
      pull_interval_sec BETWEEN 1 AND 3600 AND
      pull_batch_size BETWEEN 1 AND 1000
    )
  );
```

Это защищает от полу-настроенного RabbitMQ-узла на уровне БД.

Перечисление `root_method` расширяется:
```sql
ALTER TYPE root_method_enum ADD VALUE 'RabbitMQAsync';
```

(Если `root_method` хранится как varchar без enum — миграция не нужна, добавляется только в код.)

---

## 5. Web API — изменения

### 5.1 Тест подключения

Новый эндпоинт:

```
POST /api/nodes/test-rmq
Body: {
  "host": "rmq.internal", "port": 5672, "vhost": "/",
  "user": "nexus", "password": "...", "queue": "billing-events",
  "use_tls": false
}
Response (200):
{
  "ok": true,
  "checks": {
    "connect": { "ok": true, "elapsed_ms": 42 },
    "auth":    { "ok": true, "elapsed_ms": 18 },
    "queue":   { "ok": true, "message_count": 1248, "consumer_count": 0 }
  }
}
```

Эндпоинт пытается:
1. Установить AMQP-соединение и канал.
2. Аутентифицироваться.
3. Сделать `queue.declare` с `passive=true` — это проверяет, что очередь существует, не создавая её и не получая прав на запись.
4. Закрыть соединение.

При любой ошибке возвращает `200 OK` с `ok: false` и детализацией в `checks`. **Не 4xx/5xx** — это диагностический эндпоинт, а не функциональный. UI рендерит результат, а не пытается интерпретировать HTTP-статус.

Доступен только администраторам. Rate limit: 10 запросов/мин на пользователя — иначе пользователь зажмёт кнопку «Проверить» и устроит DoS на RabbitMQ.

### 5.2 Существующие эндпоинты

`GET /api/nodes`, `POST /api/nodes`, `PUT /api/nodes/:id`, `DELETE /api/nodes/:id` принимают и возвращают новые поля. В JSON-сериализации значение поля `rmq_password` всегда `null` (как `auth_credentials`); реальное значение задаётся отдельным флагом — в PUT-запросе клиент отправляет `rmq_password` только если хочет его поменять. Если не отправил — backend оставляет старое значение.

---

## 6. Out of scope в v1 / планы на v2

- **Exactly-once семантика** через дедупликацию по `message_id`. В v1 — at-least-once, дубли возможны на сбоях между Kafka-ack и RabbitMQ-ack. Если внешний узел не идемпотентен — нужна дедупликация на его стороне.
- **HTTP-методы кроме POST.** В v1 все исходящие запросы — POST. PUT/PATCH с маппингом из routing key или headers — в v2.
- **Динамический URL для RabbitMQAsync.** Возможность брать target URL из тела сообщения или заголовков AMQP. В v1 — только статичный `target_url`.
- **Динамическая авторизация.** Brearer/Basic из тела сообщения — не делаем в v1. Статичные креды на узел.
- **Множественные очереди на один узел.** Один узел = одна очередь. Если нужно слушать несколько — заведите несколько узлов с одинаковыми остальными настройками.
- **Шардинг pull-воркера.** Один воркер на узел, без распределения между инстансами Receiver'а. При горизонтальном масштабировании Receiver'а нужен mechanism leader election — это в v2.
- **Push-режим (basic.consume без поллинга).** AMQP позволяет получать сообщения через push, а не через периодический pull. Это эффективнее, но требует более сложного управления потоком. В v1 — простой поллинг, в v2 можно добавить вариант «непрерывный consumer» как альтернативу.
- **Exchange + bindings вместо прямой очереди.** В v1 узел подписывается на готовую очередь, которую кто-то заранее создал и привязал к нужному exchange. Создание очередей и bindings через UI — в v2.

---

## 7. Метрики Prometheus

Добавляются (см. §6 основного ТЗ):

```
databus_rmq_messages_pulled_total{node, status="ok|nack|reject"}
databus_rmq_pull_duration_seconds{node}            # histogram
databus_rmq_connection_state{node}                 # gauge: 0=down, 1=connecting, 2=up
databus_rmq_queue_depth{node}                      # gauge, обновляется на каждом тике
databus_rmq_consumer_count{node}                   # gauge, число consumer'ов на очереди (если 0 — мы единственные)
databus_node_degraded{node, reason}                # gauge: 1 при degraded
```

`queue_depth` особо полезен: если он растёт быстрее, чем Nexus вынимает, узел не справляется — нужно либо увеличить `pull_batch_size`, либо уменьшить `pull_interval_sec`.

---

## 8. Логирование

Для RabbitMQAsync поле `IP` в ClickHouse-логе (см. §4.3 основного ТЗ) заполняется как `rabbitmq://<host>:<port>/<vhost>` — формально это не IP, но семантически — источник сообщения. Это позволяет фильтровать логи по типу источника без дополнительных колонок.

Поле `method` (см. §4.3) хранит `POST` (как реальный исходящий метод), `type` хранит `RabbitMQAsync` (как `root_method` узла) — это позволяет в одном запросе ClickHouse различать сообщения, пришедшие из RabbitMQ vs из обычного `requestAsync`.

---

## 9. Critique acceptance criteria (дополнение к §15 основного ТЗ)

- При создании узла с `root_method = RabbitMQAsync` в форме отображается секция RabbitMQ-подключения, скрываются `incoming_auth_*` и блок `url_mode`.
- Кнопка «Проверить подключение» делает реальный AMQP-handshake через `/api/nodes/test-rmq` и возвращает три статуса (connect / auth / queue) с временем и `message_count` для существующей очереди.
- Воркер забирает сообщения с заданной периодичностью, публикует в Kafka, ack'ает в RabbitMQ только после Kafka `acks=all`. Сбой Kafka между ack-Kafka и ack-RabbitMQ ведёт к дублю; сбой Kafka до ack-Kafka ведёт к requeue без потери.
- При недоступности RabbitMQ воркер пере-подключается с backoff, узел переходит в `degraded` через 5 минут безуспешных попыток. После восстановления связи `degraded` снимается автоматически.
- Удаление узла дожидается обработки текущего батча (до 10 секунд), `basic.nack` необработанных, закрывает канал.
- `rmq_password` шифруется AES-256-GCM по той же схеме, что `auth_credentials`. В API возвращается `null`, в логах маскируется `***`.
- Constraint в БД блокирует сохранение RabbitMQ-узла без обязательных полей (host, queue, валидный интервал).
- Audit-записи: `node.create`, `node.update`, `node.delete` с полем `details.root_method = RabbitMQAsync`. Тест подключения в audit не пишется (диагностика).
