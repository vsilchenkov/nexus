# Тестирование на стенде с полным погружением

Эта инструкция описывает, как поднять весь стенд Nexus и прогнать сквозной
ручной тест всех механизмов: sync/async/RabbitMQAsync узлы, все виды
авторизации, проброс заголовков и адреса, HTTP-методы, логи ClickHouse,
счётчики, метрики, графики и журнал аудита. Цель — выловить регрессии, которые
не ловят unit/integration-тесты.

Скрипты-оркестраторы лежат рядом — в [scripts/stand/](../scripts/stand/):

| Файл | Назначение |
|------|------------|
| [`seed_and_test.ps1`](../scripts/stand/seed_and_test.ps1) | PowerShell: логин, создание узлов всех типов, по N запросов на каждый, сводка (Windows, основной) |
| [`seed_and_test.sh`](../scripts/stand/seed_and_test.sh) | bash-версия того же (Linux/CI) |

Тестовый **сервис-получатель** — [`cmd/echosrv`](../cmd/echosrv/main.go): отвечает
эхом (метод, путь, заголовки, тело) и поддерживает режимы авторизации и
форс-статусы по path-префиксу.

> Узлы после прогона **не удаляются** — это сделано намеренно, чтобы можно было
> вручную проверить их в UI, логах и метриках.

---

## 0. Что где слушает (единый вход)

Боевой трафик идёт через **единый вход Web** (`:8000`): Web реверс-проксирует
`/api/v1/request|requestAsync|callback` в Receiver. Прямой доступ к Receiver
(`:8080/api/v1/...`) тоже работает, но штатный путь — через Web.

| Компонент | Адрес |
|-----------|-------|
| Web (UI + API + единый вход) | http://localhost:8000 |
| Receiver (напрямую) | http://localhost:8080 |
| Sender admin (health/metrics) | http://localhost:9091 |
| Prometheus | http://localhost:9091 → проброшен как `:9091` (см. compose) |
| RabbitMQ management | http://localhost:15672 (guest/guest) |
| echosrv (получатель) | http://localhost:9999 |

---

## 1. Поднять стек

Параметры берутся из `.env` (тестовые креды уже там).

```bash
# зависимости + 3 сервиса
make docker-up

# для теста RabbitMQAsync — поднять RabbitMQ (профиль stand)
docker compose -f deploy/docker-compose.yml --profile stand up -d rabbitmq

# задать пароль admin (если ещё не задан)
make set-admin-password PASSWORD=secret
```

Запустить сервис-получатель (в отдельном терминале, на хосте):

```bash
go run ./cmd/echosrv -addr :9999
```

Проверка, что echosrv виден из контейнера Receiver (важно для target_url):
в docker-сети хост доступен как `host.docker.internal`. Если Receiver запущен
локально (`make run-receiver`), используйте `http://localhost:9999`.

---

## 2. Создать узлы и прогнать нагрузку (по 500 запросов)

PowerShell (Windows, основной путь разработки):

```powershell
# ECHO_URL — адрес echosrv, видимый Receiver'у:
#   docker-стек  → http://host.docker.internal:9999
#   локальные main → http://localhost:9999
./scripts/stand/seed_and_test.ps1 `
  -AdminPassword 'secret' `
  -EchoUrl 'http://host.docker.internal:9999' `
  -Count 500

# чтобы дополнительно создать узел RabbitMQAsync:
$env:RMQ_HOST = 'host.docker.internal'; $env:RMQ_USER='guest'; $env:RMQ_PASSWORD='guest'
./scripts/stand/seed_and_test.ps1 -AdminPassword 'secret' -EchoUrl 'http://host.docker.internal:9999'
```

bash (Linux/CI):

```bash
ADMIN_PASSWORD=secret ECHO_URL=http://host.docker.internal:9999 COUNT=500 \
  bash scripts/stand/seed_and_test.sh
```

Скрипт создаёт узлы:

| path | тип | метод (in/out) | auth | что проверяет |
|------|-----|----------------|------|----------------|
| stand/req-noauth-post | request | POST/POST | none | базовый sync, проброс тела/заголовков получателя (#6) |
| stand/req-get | request | GET/GET | none | исходящий GET (#5) |
| stand/req-put | request | PUT/PUT | none | исходящий PUT (#5) |
| stand/req-delete | request | DELETE/DELETE | none | исходящий DELETE (#5, только ps1) |
| stand/req-basic | request | POST/POST | basic | outgoing basic |
| stand/req-token | request | POST/POST | token | outgoing token |
| stand/req-fwd-headers | request | POST/POST | none | проброс заголовков X-Request-Id/X-Custom |
| stand/req-empty | request | POST/POST | none | пустое тело → пустой ответ (#6) |
| stand/req-token-from-req | request | POST/POST | token_from_request | проброс токена из заголовка (только ps1) |
| stand/async-noauth | requestAsync | POST/POST | none | async-ответ `{"result":true}` (#7) |
| stand/async-token | requestAsync | POST/POST | token | async + outgoing token (только ps1) |
| stand/rmq-async | RabbitMQAsync | —/POST | none | pull из RabbitMQ (если задан RMQ_HOST) |

Нагрузка для RabbitMQAsync льётся не HTTP-запросами, а публикацией в очередь
`nexus.stand`. Через RabbitMQ management HTTP API:

```bash
# опубликовать 500 сообщений в очередь nexus.stand (vhost "/")
for i in $(seq 1 500); do
  curl -fsS -u guest:guest -H 'content-type: application/json' \
    -d "{\"properties\":{},\"routing_key\":\"nexus.stand\",\"payload\":\"{\\\"n\\\":$i}\",\"payload_encoding\":\"string\"}" \
    http://localhost:15672/api/exchanges/%2f/amq.default/publish >/dev/null
done
```

> Очередь `nexus.stand` создаётся узлом-puller'ом при старте; если её ещё нет,
> создайте её в management UI (Queues → Add) до публикации.

---

## 3. Что проверять после прогона

### 3.1 Ответ Request (#6)
- Тело ответа = тело получателя (echosrv возвращает JSON с эхом метода/тела/
  заголовков), а **не** HTML. Узел `stand/req-empty` должен вернуть **пустое**
  тело при 200.
- Заголовки получателя пробрасываются: в ответе есть `X-Echo: 1`.

### 3.2 Async (#7) и RabbitMQAsync (#8)
- `stand/async-*` отвечают `{"result":true}` (тело JSON, не HTML). На ошибке
  (например, выключить узел и стрельнуть) — `{"result":false,"message":...}`.
- `stand/rmq-async`: сообщения из очереди доезжают до echosrv; в UI узла растёт
  счётчик, в ClickHouse появляются логи. Синхронного `result`-ответа у pull-узла
  нет — это ожидаемо (#8 неприменим).

### 3.3 Методы (#5)
- В логах ClickHouse каждого узла поле `method` совпадает с `outgoing_method`.
- Запрос «неправильным» входящим методом отклоняется с `405`. Проверка:
  ```bash
  curl -i -X DELETE http://localhost:8000/api/v1/request/default/stand/req-noauth-post
  # ожидаем HTTP/1.1 405 Method Not Allowed
  ```

### 3.4 Логи и журнал аудита (#4)
- Вкладка логов узла (ClickHouse): видно `method`, `request`/`response` (если
  включено логирование тела), `IP`. **IP должен быть IPv4** (`127.0.0.1`, а не
  `::1`).
- Журнал аудита (страница Audit): создание узлов, dry-run, логины — IP в формате
  IPv4.

### 3.5 UI (#2, #3, #9)
- Форма узла и read-only «Конфигурация»: показывается **полный адрес**
  (`http://localhost:8000/api/v1/request/<path>`) и кнопка **«Скопировать»** с
  иконкой — копирует адрес в буфер.
- Dry-run: длинный результат **скроллится внутри окна**, кнопки footer остаются
  на экране (#3).
- Сузьте окно браузера (≤ 640px): формы не «съезжают» — поля становятся в одну
  колонку, шапка переносится, таблицы прокручиваются по горизонтали (#9).

### 3.6 Счётчики, метрики, графики
- Главный экран: счётчики входящих/исходящих за 24ч, очередь Kafka, ошибки.
- Карточка/страница узла: throughput, p95, спарклайн.
- `GET http://localhost:8080/metrics` (Receiver) и `:9091/metrics` (Sender):
  `nexus_requests_total{method=...,node=...,status=...}` растёт по узлам.
- Панели Web: `GET $WEB/api/metrics/overview`, `/api/metrics/nodes`,
  `/api/metrics/nodes/{id}`.

---

## 4. Диагностика

| Симптом | Причина / что проверить |
|---------|--------------------------|
| В ответ приходит HTML index.html | Бьёте по старому `/v1/...` вместо `/api/v1/...`, либо не через единый вход |
| 405 на всех запросах | `incoming_method` узла не совпадает с методом запроса (#5) |
| target_url unreachable / 502 | echosrv недоступен Receiver'у — проверьте `ECHO_URL` (host.docker.internal vs localhost) |
| RabbitMQAsync без трафика | RabbitMQ не поднят (`--profile stand`) или очередь `nexus.stand` не создана |
| IP в логах `::1` | проверьте, что собран код с нормализацией IPv4 (Phase D) |

См. также общий [TESTING.md](../TESTING.md) (unit/integration/loadtest) и
[DEVELOPMENT.md](../DEVELOPMENT.md) (локальный запуск).
