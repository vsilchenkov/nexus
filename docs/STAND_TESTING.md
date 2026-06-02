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
| Prometheus | http://localhost:9099 (контейнер `:9090`) |
| RabbitMQ management | http://localhost:15672 (guest/guest) |
| echosrv (получатель) | http://localhost:9999 |

Зависимости (порты, опубликованные на хост для нативного запуска): PostgreSQL
`:5432`, Redis `:6379`, ClickHouse `:19000` (native protocol), Kafka `:9092`,
Prometheus `:9099`. Адреса/креды для нативного запуска зашиты в
[config/config_debug.yml](../config/config_debug.yml); для Docker-стека — в `.env`
(docker-сетевые хосты `postgres`/`redis`/`clickhouse`/`kafka`).

---

## 1. Поднять стек

Есть два варианта запуска. Боевой трафик в обоих идёт через единый вход Web (`:8000`).

### Вариант A — всё в Docker (как в проде, креды из `.env`)

```bash
make docker-up                 # зависимости + receiver/sender/web (env_file: .env)
# для RabbitMQAsync — поднять RabbitMQ (профиль stand):
docker compose -f deploy/docker-compose.yml --profile stand up -d rabbitmq
make set-admin-password PASSWORD=secret   # bootstrap пароля admin (идемпотентно)
```

`ENCRYPTION_KEY` сервисы получают из `.env` (`env_file`). echosrv виден Receiver'у
как `http://host.docker.internal:9999`.

### Вариант B — зависимости в Docker, сервисы нативно (быстрая итерация)

```bash
make docker-up-dev             # только postgres/redis/clickhouse/kafka/prometheus

# ВАЖНО: нативный --debug-запуск читает ENCRYPTION_KEY из окружения (НЕ из .env).
# Ключ должен совпадать с тем, которым зашифрованы креды в БД (значение из .env).
# Плюс PROMETHEUS_URL — иначе Web считает Prometheus недоступным и метрики панели
# (Overview KPI/throughput) будут НУЛЕВЫМИ (prometheus_available=false). Prometheus
# из deps скрейпит нативные сервисы по host.docker.internal; на хосте он на :9099.
# PowerShell:
$env:ENCRYPTION_KEY = (Select-String -Path .env -Pattern '^ENCRYPTION_KEY=').Line.Split('=',2)[1]
$env:PROMETHEUS_URL = 'http://localhost:9099'
# bash:
export $(grep -E '^ENCRYPTION_KEY=' .env)
export PROMETHEUS_URL=http://localhost:9099

make set-admin-password PASSWORD=secret   # bootstrap пароля admin
# три сервиса — каждый в своём терминале (config_debug.yml → localhost; для метрик
# панели run-web должен видеть PROMETHEUS_URL в окружении):
make run-receiver
make run-sender
make run-web
```

> Метрики панели (Overview KPI/очередь/throughput) приходят из Prometheus. Если
> Prometheus не поднят/не скрейпит сервисы или `PROMETHEUS_URL` не задан — они
> деградируют в нули (это не баг), а per-node KPI/график на странице узла
> продолжают считаться из ClickHouse за выбранный период.

echosrv виден нативному Receiver'у как `http://localhost:9999`.

### echosrv (общий для обоих вариантов)

Сервис-получатель — в отдельном терминале на хосте:

```bash
go run ./cmd/echosrv -addr :9999
```

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

### 3.7 Доработки §28 «онлайн-метрики» (8 пунктов ТЗ)

ТЗ — [specs/sections/28-online-metrics.md](../specs/sections/28-online-metrics.md).

- **#1 Публичный адрес.** Settings → **Общие** (admin): задать
  `https://nexus.example.com` → на форме узла и вкладке «Конфигурация» полный
  адрес собирается от него (а не от origin браузера). Пусто → снова origin.
- **#2 Онлайн-метрики.** На Overview и странице узла KPI/графики обновляются без
  перезагрузки (поллинг 12с) — стрельните нагрузкой и смотрите рост на месте.
- **#3 Маскирование кред.** Поля incoming/outgoing/RMQ-пароль — звёздочки +
  глазик (раскрывает только вводимое). Под ролью **viewer** кнопки New/Edit
  скрыты, прямой переход на форму узла редиректит (креды не видны).
- **#4 Период метрик.** Селектор «Период»: `1h/3h/24h/7d/14d/30d` + «Произвольный»
  (календарь from/to) на Overview и вкладках узла Обзор/Метрики; по умолчанию 1h.
- **#5 Ошибки валидации.** Создать узел с плохими данными (пустой `path`,
  кривой `rmq_queue`, `logging` включён без таблицы) → понятное локализованное
  сообщение **у конкретного поля** (красная рамка), а не сырой `domain: ...`.
- **#6 Фильтр RabbitMQAsync.** На Overview в фильтре типа узла есть третий
  вариант `RabbitMQAsync`.
- **#7 CH-таблица без шаблона.** Узлы стенда создаются с `clickhouse_table` без
  `clickhouse_template_id` — таблица логов авто-создаётся из дефолтного шаблона;
  логи/метрики узла грузятся **без** `clickhouse search: code 60`.
- **#8 Резолв async по пути-со-слешем.** Создать узел `webhook/sendasynq` и
  дёрнуть показанный в UI адрес **без слога команды**
  (`/api/v1/requestAsync/webhook/sendasynq`) — проходит (а не `node not found`);
  то же для sync `webhook/send`.

---

## 4. Диагностика

| Симптом | Причина / что проверить |
|---------|--------------------------|
| В ответ приходит HTML index.html | Бьёте по старому `/v1/...` вместо `/api/v1/...`, либо не через единый вход |
| 405 на всех запросах | `incoming_method` узла не совпадает с методом запроса (#5) |
| target_url unreachable / 502 | echosrv недоступен Receiver'у — проверьте `ECHO_URL` (host.docker.internal vs localhost) |
| RabbitMQAsync без трафика | RabbitMQ не поднят (`--profile stand`) или очередь `nexus.stand` не создана |
| IP в логах `::1` | проверьте, что собран код с нормализацией IPv4 (Phase D) |
| Сервис падает с exit 1 «invalid ENCRYPTION_KEY» при `make run-*` | нативный запуск не видит `.env` — экспортируйте `ENCRYPTION_KEY` в окружение (см. Вариант B) |
| Логи/метрики узла: `clickhouse search: code 60 ... Unknown table` | таблица логов не создана — убедитесь, что есть дефолтный CH-шаблон (узел без `clickhouse_template_id` берёт его); фикс #7 §28 |
| `node not found` на адресе с путём-со-слешем | бейте по адресу как в UI; фикс #8 §28 резолвит legacy-путь в default-команде |

См. также общий [TESTING.md](../TESTING.md) (unit/integration/loadtest) и
[DEVELOPMENT.md](../DEVELOPMENT.md) (локальный запуск).
