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

Зависимости для нативного запуска берутся из **постоянного docker-стека `services`**
(compose-проект `services`, уже запущен на машине разработчика): PostgreSQL `:5432`,
Redis `:6379`, ClickHouse `:19000` (native) / `:18123` (HTTP), Kafka `:9092`.
Адреса/креды зашиты в [config/config_debug.yml](../config/config_debug.yml) и **уже
соответствуют `services`** (postgres `postgres`/`vOkjDn`/db `nexus`; redis ACL-пользователь
`sa`/`I2MV5s`; clickhouse `default` без пароля; kafka `localhost:9092`).

> **Не поднимай свои зависимости через `make docker-up-dev`** — это создаёт
> дублирующие контейнеры `nexus-*`, которые не могут занять опубликованные порты
> (их держит `services`) и только путают (две Redis/Postgres). Используй уже
> поднятый `services`. Prometheus в `services` **нет** — метрики панели (Overview
> KPI/throughput) деградируют в нули (это норма; per-node KPI/график узла всё равно
> считаются из ClickHouse). RabbitMQ в `services` тоже нет — для RabbitMQAsync подними
> его отдельно. Docker-стек Варианта A (`.env`) — отдельная история.

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

### Вариант B — зависимости из стека `services`, сервисы нативно (быстрая итерация)

Зависимости НЕ поднимаем — используем уже запущенный стек `services` (см. §0).
Проверить, что он жив: `docker ps --filter label=com.docker.compose.project=services`
(ожидаем postgres/redis/clickhouse/kafka — healthy). **`make docker-up-dev` не запускаем.**

```bash
# ВАЖНО: нативный --debug-запуск читает ENCRYPTION_KEY из окружения (НЕ из .env).
# Ключ должен совпадать с тем, которым зашифрованы креды в БД (значение из .env).
# PROMETHEUS_URL опционален: в стеке `services` Prometheus НЕТ — без него метрики
# панели деградируют в нули (норма). Если нужен — подними Prometheus отдельно и
# задай PROMETHEUS_URL на его адрес.
# PowerShell:
$env:ENCRYPTION_KEY = (Select-String -Path .env -Pattern '^ENCRYPTION_KEY=').Line.Split('=',2)[1]
# bash:
export $(grep -E '^ENCRYPTION_KEY=' .env)

make migrate-up                            # применить миграции (свежая/обновлённая схема)
make set-admin-password PASSWORD=secret123 # bootstrap пароля admin (≥8 символов)
# три сервиса — каждый в своём терминале (config_debug.yml → localhost = стек `services`):
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
| `web listen: ... :8000: bind: Only one usage of each socket address` (или `address already in use`) | На порту уже висит **старый** экземпляр сервиса с прошлого прогона — стенд не был выключен. Завершите его (см. §5) и запускайте заново |
| Контейнер `clickhouse` стека `services` в рестарт-лупе, в `docker logs` только `get_mempolicy: Operation not permitted` | Настоящая ошибка в `clickhouse-server.err.log` **внутри** контейнера (`docker cp clickhouse:/var/log/clickhouse-server/clickhouse-server.err.log .`). Известный случай: `Code: 36 … If 'engine' is specified for system table, TTL parameters should be specified directly inside 'engine'` — конфиг `logs_ttl.xml` задавал `<ttl>` для `query_log`/`opentelemetry_span_log`, у которых в `config.xml` образа уже есть `<engine>`. Лечится правкой `services/clickhouse/logs_ttl.xml` (2026-08-04 он вынесен из контейнера в репозиторий стека и монтируется через compose — правки больше не теряются при пересоздании) |

---

## 5. Завершение: ОБЯЗАТЕЛЬНО выключить стенд после тестов

> **Правило.** Любой прогон на стенде (ручной или автоматический, в т.ч. агентом Claude Code)
> **завершается выключением** поднятых сервисов. Иначе следующий запуск падает на
> `bind: Only one usage of each socket address ... :8000` (порт занят зависшим Web), а лишние
> процессы тихо едят ресурсы и искажают метрики.

**Нативные сервисы (Вариант B).** Если запускали `make run-{receiver,sender,web}` в терминалах —
закройте их `Ctrl+C`. Если процессы зависли/запускались в фоне (или это был временный экземпляр
для проверки) — снять по имени образа:

```powershell
# Windows (PowerShell): гасит web/sender/receiver и временные бинари проверки
taskkill /IM web.exe /F; taskkill /IM sender.exe /F; taskkill /IM receiver.exe /F
# проверить, что порты свободны (пусто = всё выключено):
netstat -ano | findstr ":8000 :8080 :9091 :9190"
```

```bash
# Linux/macOS:
pkill -f 'cmd/web' ; pkill -f 'cmd/sender' ; pkill -f 'cmd/receiver'
```

echosrv (`go run ./cmd/echosrv`) тоже закрыть (`Ctrl+C` / `taskkill /IM echosrv.exe /F`).

**Docker-стек (Вариант A).** `make docker-down` (остановит receiver/sender/web + зависимости).

**Зависимости (PostgreSQL/Redis/ClickHouse/Kafka/Prometheus/RabbitMQ).** Это инфраструктура, а не
сам «стенд» — по умолчанию **оставляем поднятой** (данные в volume'ах сохраняются). Остановить при
необходимости: `make docker-down` (профиль базового compose) либо `docker compose -f <ваш-файл> down`
для внешнего deps-стека.

> Для агента Claude Code: если в ходе задачи ты сам поднимал сервисы стенда (включая временные
> экземпляры на других портах), **в конце задачи погаси их** и убедись, что порты освобождены.

---

## Браузерная проверка UI (обязательна перед сдачей ТЗ с фронтом)

Backend/unit/integration-тесты, `golangci-lint` и `npm run lint --max-warnings=0` **не ловят
фронт-баги** (нестабильный react-query `queryKey`, пустой UI при рабочем API, неверная видимость
кнопок по роли/статусу). Поэтому любое изменение в `web-ui/` перед сдачей **прокликивается живьём
через Playwright/chromium MCP-агента**. Грабли §35: `queryKey: [..., periodWindow(period)]` (с
`until: Date.now()`) → вечный refetch → KPI «Неудачные доставки» = «—» при 42 строках в CH; бэкенд,
unit, integration и линтеры — все зелёные, поймал только браузер.

**Процедура.**
1. Собрать бандл и пересобрать web (иначе браузер увидит старый embed): `make build-ui` →
   `go build ./cmd/web` → поднять стенд (Вариант B выше).
2. Один раз поставить браузер MCP: `npx @playwright/mcp@latest install-browser chrome-for-testing`.
3. `browser_navigate http://localhost:8000` → логин (admin) → открыть изменённые страницы.
4. `browser_snapshot` — проверить, что данные/KPI/счётчики **реальны** (не «—»/пусто), сверить с CH
   (`docker exec clickhouse clickhouse-client -q "SELECT count() FROM <table> WHERE done=0 AND
   date_request > now() - INTERVAL 1 HOUR"`).
5. Прокликать действия и снять `browser_snapshot` после каждого; `browser_console_messages
   onlyErrors=true` обязан быть **0**.
6. В конце — `browser_close`, погасить стенд.

**Чек-лист вкладки «Очередь» (узел requestAsync, §35).**
- [ ] KPI «Неудачные доставки» = число из CH (не «—»); список `done=no` за период не пуст, если в CH есть падения.
- [ ] KPI «Ожидают отправки» = 0 на активном узле (живая очередь пуста — норма).
- [ ] «Поставить на паузу» → статус-чип «Пауза», баннер про паузу, кнопки → «Возобновить узел» + «Отключить узел».
- [ ] На паузе видны «Очистить за период» / «Очистить все ожидающие»; на активном — скрыты (чистить нечего).
- [ ] «Возобновить узел» → статус «Активен», прежние кнопки. Накопленные сообщения уходят сами,
      **без перезапуска Sender'а**, в течение ближайших проходов sweeper'а (`sender.paused_sweep.
      interval_sec`, на dev-стенде 5 с; см. 4.38 IMPLEMENTATION.md).
- [ ] **Изоляция узлов (§3.6).** Пока один узел на паузе, ДРУГИЕ узлы должны работать без задержек —
      сообщения paused-узла переносятся в отдельный топик `nexus.async.paused` и не держат партицию.
      Симптом «второй узел молчит, пока первый на паузе» — это РЕГРЕССИЯ изоляции, а не норма.
      Проверять на двух узлах одновременно: один поставить на паузу, во второй слать трафик.
- [ ] «Ожидают отправки» на paused-узле показывает накопленный бэклог (он лежит в delay-топике, а не
      в основном), «Очистить все ожидающие» его отменяет, раскрытие строки показывает тело.
- [ ] Порядок бэклога после снятия паузы — приблизительный (осознанно, см. §3.6): строгий FIFO
      гарантируется только для активного узла.
- [ ] Кнопка «Обновить» в шапке (рядом с «Изменить») крутит иконку и перезагружает данные всех вкладок.
- [ ] Под viewer/manager вкладка открывается без 403 (failed-view виден; admin-секция/кнопки скрыты).
- [ ] Консоль без ошибок.

**Принципы верификации фиксов (чтобы «починил» не оказалось «не починил»).**

Грабли §44 (скролл логов): фикс DOM-потолка проверили скроллом с `pageSize=200` + искусственным
JS-циклом и увидели рост до ~1000 строк → объявили «работает». Но реальный симптом «не двигает вниз»
— это стопор курсора на **плотных секундах**, и он зависит от `pageSize`: при 200 CH отдаёт *разные*
строки секунды → дедуп всё же добавляет новые → прокрутка ползёт (баг замаскирован); при **дефолтном
50** — те же 50 строк → 0 новых → жёсткий стопор (то, что видел пользователь). Проверяли не тот
сценарий и не тот критерий.

- **Воспроизводи ТОЧНЫЙ симптом пользователя в ЕГО конфигурации.** Дефолтные настройки (не меняй
  `pageSize`/фильтры/период, которые могут замаскировать баг), тот самый узел/данные, тот самый жест
  (скролл вниз от верха). Если параметр влияет на баг — проверяй значение, которым пользуется
  пользователь, и худший случай.
- **Критерий прохождения = ОТРИЦАНИЕ бага, а не работа смежного механизма.** «Строки дошли до
  потолка» ≠ «скролл больше не стопорится». Формулируй критерий как «прокрутка прогрессирует мимо
  точки, где застревала» и **измеряй** это (рост строк по шагам).
- **Для data-зависимых багов сперва подтверди корень в данных** (на стенде: плотная секунда = 624
  строки/сек) и добавь **детерминированный тест инварианта**, который **падал бы на старом коде**
  (integration `TestClickHouse_KeysetPagination_DenseSecond_E2E`: >limit строк в одной секунде → keyset
  отдаёт все без повторов). Зелёный браузер без такого теста — не гарантия.
- Не давай конфигурации, которая «случайно работает», подменить падающую.

См. [memory] `feedback_browser_testing`, `feedback_reproduce_exact_symptom`.

См. также общий [TESTING.md](../TESTING.md) (unit/integration/loadtest) и
[DEVELOPMENT.md](../DEVELOPMENT.md) (локальный запуск).
