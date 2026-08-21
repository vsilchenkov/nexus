# DEVELOPMENT.md — локальная разработка и отладка в VS Code (Windows)

Как запустить и отлаживать Nexus на Windows из VS Code: от первого запуска и миграций
до старта всех трёх сервисов под отладчиком одной кнопкой.

> Развёртывание в продакшене (Docker, Linux-сервер, внешние сервисы, обновление, откат) —
> в отдельном файле [DEPLOYMENT.md](./DEPLOYMENT.md).

---

## 1. Сценарий

Зависимости (PostgreSQL, Redis, ClickHouse, Kafka) поднимаются в Docker Desktop отдельным
self-contained compose-файлом с пробросом портов на хост. Сами сервисы `receiver`/`sender`/`web`
запускаются **локально под отладчиком dlv** и цепляются к `localhost:<port>`.

```
                Docker Desktop                         Хост (под отладчиком VS Code)
   ┌───────────────────────────────────┐        ┌──────────────────────────────────┐
   │ postgres:5432  redis:6379          │◄───────│  web      → :8000                 │
   │ clickhouse:8123/9000  kafka:9092   │◄───────│  receiver → :8080                 │
   └───────────────────────────────────┘        │  sender   → :9190 (gRPC) / :9091  │
        docker-compose.deps.yml                  └──────────────────────────────────┘
```

В каталоге [.vscode/](./.vscode/) уже лежат готовые конфиги: `launch.json` (запуск/отладка
сервисов), `tasks.json` (зависимости, миграции, тесты), `settings.json` (gopls/dlv).

---

## 2. Разовая подготовка

1. **Docker Desktop** — установить, включить WSL2-интеграцию, запустить.
2. **Go 1.26+** — установить, проверить `go version`.
3. **VS Code + расширение `golang.go`.** Через Command Palette (`Ctrl+Shift+P`) →
   **Go: Install/Update Tools** поставить `dlv`, `gopls`, `golangci-lint`.
   - Версию линтера держать в соответствии с CI (`golangci/golangci-lint:v2.12`):
     `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.
   - **На Windows запускать с `--concurrency=2`** — дефолтная параллельность
     приводит к `runtime: out of memory`:
     `golangci-lint run --timeout=10m --concurrency=2`
     (в CI ограничение не нужно — Linux-контейнер с достаточной памятью).
4. **`.env` в корне репозитория** на основе [.env.example](./.env.example). Минимально
   обязателен `ENCRYPTION_KEY` — 32 байта в base64. Сгенерировать в PowerShell:

   ```powershell
   [Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))
   ```

   Остальные `PG_*`/`REDIS_*`/`CH_*`/`KAFKA_BROKERS` для локальной отладки можно не трогать:
   конфиг `config/config_debug.yml` уже указывает на `localhost`, а значения из `.env`
   перекрывают дефолты через плейсхолдеры `${VAR:default}`.

---

## 3. Порядок первого запуска

### Шаг 1. Поднять зависимости

```powershell
docker compose -f deploy/docker-compose.deps.yml up -d
```

или Command Palette → **Tasks: Run Task → deps: up**.

Файл [deploy/docker-compose.deps.yml](./deploy/docker-compose.deps.yml) самодостаточный:
поднимает только зависимости с портами `5432/6379/8123/9000/9092/7071` на хосте. Kafka
анонсирует себя как `localhost:9092` (именно для процессов с хоста).

> **Первый запуск собирает образ брокера** (§75): `kafka` — это `apache/kafka` плюс
> `jmx_prometheus_javaagent`, который отдаёт размер топиков на `localhost:7071/metrics`
> (`kafka_log_log_size`) — без него колонка «Размер» на экране `/kafka` показывает «—».
> jar агента лежит в репозитории (`deploy/vendor/`), поэтому сборка идёт без сети; другая версия
> или зеркало —
> `docker compose -f deploy/docker-compose.deps.yml build --build-arg JMX_AGENT_SRC=<путь-или-url> kafka`.
> Правка [deploy/kafka-jmx.yml](./deploy/kafka-jmx.yml) с ошибкой не даст брокеру стартовать
> (JVM не запускается с невалидным javaagent) — смотрите тогда `deps: logs (kafka)`.

Проверка состояния — **deps: status**; логи Kafka — **deps: logs (kafka)**; полный сброс
данных — **deps: down + reset volumes**.

> **Kafka занимает ~1.2 ГБ памяти** — брокер стартует с `KAFKA_HEAP_OPTS=-Xmx1G -Xms1G`
> (значение по умолчанию в compose совпадает с дефолтом `kafka-server-start.sh`). На машине,
> где памяти в обрез, положите в `.env` строку `KAFKA_HEAP_OPTS=-Xmx512m -Xms256m` и пересоздайте
> контейнер (`docker compose -f deploy/docker-compose.deps.yml up -d --force-recreate kafka`) —
> брокер займёт ~515 МиБ. Замеры остальных компонентов — [DEPLOYMENT.md](./DEPLOYMENT.md) §5.4.

> Prometheus спрятан за профилем `metrics` и по умолчанию выключен (его scrape-таргеты
> рассчитаны на контейнерные имена). Для метрик: `docker compose -f deploy/docker-compose.deps.yml --profile metrics up -d`.

> **Дашборды панели (§21) локально.** KPI на Overview, per-node throughput и график на
> странице узла берутся из Prometheus + ClickHouse. По умолчанию `prometheus.url` в
> `config_debug.yml` пуст — панель работает, но эти блоки показывают «нет данных»
> (`prometheus_available=false`). Чтобы увидеть реальные числа локально:
> 1. подними профиль `metrics` (команда выше) — Prometheus слушает `localhost:9091`;
> 2. задай `PROMETHEUS_URL=http://localhost:9091` перед запуском `cmd/web` (env подставится
>    в `${PROMETHEUS_URL:}` плейсхолдер конфига).
>
> Важно: scrape-таргеты Prometheus в `deploy/prometheus.yml` указывают на контейнерные имена
> (`receiver:8080`, `sender:9091`, `web:8000`). При нативном запуске сервисов с хоста
> Prometheus их не доскрейпит — поэтому глобальные KPI/очередь будут пустыми даже с заданным
> `PROMETHEUS_URL`. Per-node KPI и график на странице узла при этом работают всегда (источник —
> ClickHouse). Полноценные дашборды проще смотреть в полном docker-compose, где все сервисы
> в одной сети с Prometheus.

### Шаг 2. Применить миграции (один раз / после новых миграций)

- **Tasks: Run Task → migrate up**, либо в терминале:
  ```powershell
  go run ./cmd/web --debug --migrate-up
  ```
- Проверить версию схемы: `go run ./cmd/web --debug --migrate-status`
- Откатить последнюю: `go run ./cmd/web --debug --migrate-down 1`
- Выйти из `dirty` после оборванной миграции (§74.4): сначала привести схему руками, затем
  `make migrate-force V=28` (или `go run ./cmd/web --debug --migrate-force 28`) — **SQL команда
  не выполняет**, только объявляет версию и снимает флаг.

Либо через Run and Debug — конфиг **Web: migrate-up** (миграция под отладчиком).

> При обычном `--debug`-запуске `web` и `receiver` и так применяют миграции автоматически.
> Отдельный шаг нужен, когда хотите накатить схему до старта сервисов или явно проверить статус.
>
> **Переключились на ветку со старым набором миграций?** Сервис стартует и пишет в лог запись
> уровня `error` «postgres schema is newer than this build» (§74.3): схема в БД новее вашего
> каталога `migrations/`. Это ожидаемо и мешать работе не будет, но если нужна чистая схема —
> откатите лишние миграции с ветки, где они есть (`--migrate-down N`), или пересоздайте БД.

### Шаг 3. Задать пароль администратора (один раз)

Миграция создаёт `admin` с пустым паролем. Задать:

- **Tasks: Run Task → set admin password (admin)** (ставит пароль `admin`), либо:
  ```powershell
  go run ./cmd/web --debug --set-admin-password admin
  ```
- Либо Run and Debug → **Web: set-admin-password**.

### Шаг 4. Запустить сервисы под отладчиком

Откройте панель **Run and Debug** (`Ctrl+Shift+D`) и запустите нужный конфиг (см. §4).
Для полного стека — **Nexus: all (web + receiver + sender)**.

---

## 4. Варианты запуска в VS Code

Конфиги из [.vscode/launch.json](./.vscode/launch.json):

| Конфиг                              | Что делает                                                       |
|-------------------------------------|------------------------------------------------------------------|
| `Web (debug)`                       | `go run ./cmd/web --debug` под dlv → `:8000`                     |
| `Receiver (debug)`                  | `go run ./cmd/receiver --debug` под dlv → `:8080`               |
| `Sender (debug)`                    | `go run ./cmd/sender --debug` под dlv → `:9190` gRPC + `:9091`  |
| `Nexus: all (web + receiver + sender)` | **compound** — все три сервиса одной кнопкой (`stopAll: true`) |
| `Web: migrate-up`                   | разовая миграция под отладчиком                                  |
| `Web: set-admin-password`           | задаёт пароль `admin`                                            |
| `Loadtest`                          | `cmd/loadtest` против локального стека (1m / 100 RPS / 10 узлов) |
| `Integration tests (current file)`  | `go test -tags=integration -v` для открытого файла              |
| `Attach to process (pid)`           | подключение отладчика к уже запущенному процессу                |

Все конфиги читают переменные из `${workspaceFolder}/.env` (`envFile`) и используют
`--debug` → [config/config_debug.yml](./config/config_debug.yml) (все хосты — `localhost`).
Точки останова, шаг с заходом, инспекция переменных — штатно через dlv-dap.

### Запуск всего сервиса целиком

`Nexus: all` — это **compound**-конфигурация: одним запуском поднимает `Web`, `Sender` и
`Receiver` (с `stopAll: true` — остановка любого гасит все три). Это и есть «весь сервис»
под отладчиком. Предусловие — выполнены шаги 1–3 из §3 (зависимости подняты, миграции
применены, пароль задан).

### Задачи (Tasks: Run Task) из [.vscode/tasks.json](./.vscode/tasks.json)

| Задача | Команда |
|--------|---------|
| `deps: up` / `deps: down` / `deps: status` | управление зависимостями в Docker |
| `deps: down + reset volumes` | снести зависимости вместе с данными |
| `deps: logs (kafka)` | стрим логов Kafka |
| `migrate up` | `go run ./cmd/web --debug --migrate-up` |
| `set admin password (admin)` | `go run ./cmd/web --debug --set-admin-password admin` |
| `test: unit` | `go test -race -short ./...` |
| `test: integration` | `go test -tags=integration -count=1 -v ./tests/integration/...` |
| `build: all` | сборка `bin/{receiver,sender,web}.exe` |

### Кодогенерация и §22-специфика

- **Правка gRPC-контракта** (`proto/sender/v1/sender.proto`) → перегенерировать Go-код:
  `make proto` (нужны `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` в `PATH`). Например, поля
  контроля логирования узла (`logging_enabled`/`max_body_size_enabled`/`max_body_size`, §22) едут в
  Sender по sync-пути именно через `SendRequest` — после правки proto не забыть `make proto` и
  смаппить новые поля в `route.go`/`sender_service.go`.
- **Правка handler-аннотаций / DTO** (`internal/web/adapter/in/http/*` или
  `internal/receiver/adapter/in/http/*`) → `make swagger`. Цель генерирует ДВА дока:
  `docs/web` (Web API) и `docs/receiver` (Receiver API, instance `receiver`, §25).
  CI-гейт `swagger-drift-check` валит сборку при расхождении `docs/`.
- **Сценарные тесты логирования (§22)** — `internal/sender/usecase/send_test.go` (обрезка по рунам,
  отключение логирования, спецсимволы) и `tests/integration/clickhouse_test.go`
  (`TestClickHouse_Logging_Scenarios`, требует Docker: большое тело/JSON/unicode + кейс «логирование
  выключено → count==0»). Сценарий большого тела (клиент получает полное, в логе усечено, checksum по
  полному) — `tests/integration/clickhouse_largebody_test.go`.
- **Динамическая подгрузка тел логов (§42)** — большое тело лога больше не тянется/рендерится целиком.
  Эндпоинты (Web, scope `logs:read`): `GET /api/nodes/:id/log/:logId` отдаёт **превью** тел (первые 64K
  рун) + `request_len`/`response_len`; `GET …/log/:logId/body?which=&offset=&limit=` — срез тела по
  рунам; `GET …/log/:logId/body/download?which=` — потоковое скачивание файлом. Срезы режутся в
  ClickHouse (`substringUTF8`/`lengthUTF8`), порт `LogReader.GetByIDPreview`/`GetBodyChunk`. После правки
  аннотаций — `make swagger`. Чистые UI-помощники тел — `web-ui/src/lib/logBody.ts` (+Vitest), пороги
  превью/pretty/warn там же. Тесты: `tests/integration/clickhouse_bodychunk_test.go`,
  `internal/web/adapter/in/http/logs_handler_test.go`.
- **Транспорт больших тел (§42.8)** — большое тело должно доезжать по самой шине, не только
  отображаться. gRPC Receiver↔Sender держал дефолтный лимит сообщения 4 МиБ → большой ответ апстрима
  падал `ResourceExhausted`. Лимит конфигурируется: `sender.grpc_max_message_bytes` (сервер,
  `MaxRecvMsgSize`/`MaxSendMsgSize` в `sender/app.go`) и `receiver.sender_grpc.max_message_bytes` (клиент,
  `MaxCallRecvMsgSize`/`MaxCallSendMsgSize` в `grpcsender/client.go`), оба дефолт 64 МиБ — **держи их
  равными** и `≥ receiver.max_body_bytes`. Async: Kafka producer `BatchBytes` теперь
  `max(producer.batch_size, topic.max_message_bytes)` (`producerBatchBytes` в `platform/kafka/producer.go`).
  Тесты: `internal/receiver/adapter/out/grpcsender/client_largemsg_test.go` (8 МиБ round-trip + негативный
  контроль), `internal/platform/kafka/producer_batchbytes_test.go`.
- **Новые поля формы узла** (§22): в `web-ui` — карточки «Заголовки»/«Логирование», компонент
  `Toggle`; после правки `web-ui/` обязательны `npm run lint` (`--max-warnings=0`) и `npm run build`,
  затем пересборка встроенного SPA (`make build-ui` или копирование `web-ui/dist/*` в
  `internal/web/static/`).
- **RabbitMQAsync (§27)** — Puller-воркеры живут в Receiver и сами поднимаются для узлов
  `root_method=RabbitMQAsync` (reconcile из PG раз в `receiver.puller.reconcile_sec`). Для локальной
  отладки нужен брокер RabbitMQ (Docker: `docker run -d --rm -p 5672:5672 -p 15672:15672
  rabbitmq:3.13-management-alpine`). e2e-тесты (`tests/integration/receiver_rmq_test.go`) сами
  поднимают RabbitMQ через testcontainers — отдельный брокер для `make test-integration` не нужен,
  только Docker daemon. Кнопка «Проверить подключение» в форме узла дёргает `POST /api/nodes/test-rmq`
  (manager+). Health воркера UI читает из Redis-hash `rmq:health`.
- **Мониторинг Kafka (§31)** — admin-only экран `/kafka` (раздел «Аудит»). Источники: Prometheus
  (async-трафик по `method="requestAsync"`) + Kafka Admin API (топики/брокеры; Web подключается к
  `kafka.brokers`). Новая npm-зависимость **`recharts`** (графики) — при правке зависимостей коммитьте
  `web-ui/package.json` + `package-lock.json`, иначе `npm ci` в CI развалится. После правки `web-ui/`
  — `npm run lint` (`--max-warnings=0`) + `npm run build` + `make build-ui` + коммит `internal/web/static/`.
  Для живых данных локально нужен Kafka (поднимается `deps: up`) и трафик через `requestAsync`-узлы
  (генератор — `cmd/loadtest`, флаг async). Без Prometheus/Kafka экран деградирует, не падает.

---

## 5. После запуска

- Web UI / API → `http://localhost:8000`
- Swagger UI → `http://localhost:8000/swagger/web/index.html` (Web API),
  `http://localhost:8000/swagger/receiver/index.html` (Receiver API)
- Receiver → `http://localhost:8080/api/v1/request/*`, `http://localhost:8080/api/v1/requestAsync/*`
  (боевой трафик в проде идёт через единый вход Web `:8000/api/v1/*`, который проксирует в Receiver)
- Sender admin → `http://localhost:9091/health` (сам gRPC SenderService — на `:9190`)

Логин в UI: `admin` + пароль, заданный на шаге 3.

### Тесты (§51 — что добавилось)

> **Добавили integration-тест — проверьте, что он попадает в под-прогон.** `make
> test-integration` гоняет тесты группами с фильтрами `-run` (ради таймаутов), а CI запускает
> пакет `tests/integration` целиком. Тест, не попавший ни в один фильтр, локально не
> выполняется вовсе — и падение всплывает уже на теге (так вышло при выпуске 1.28.0,
> IMPLEMENTATION §4.75). Первой целью `test-integration` идёт гейт `check-int-coverage`
> (`scripts/ci/check_integration_coverage.py`): он падает со списком непокрытых тестов.
> Не подходит ни одна группа — дополните `test-int-misc`.

- `make test-int-logs` — integration-группа консоли служебных логов (Redis+PG: шиппер, мерж,
  кросс-сервисный reload уровня, маскировка, неблокируемость); входит в общий `make test-integration`.
- Фронтовые unit-тесты: `cd web-ui && npm run test` (vitest + RTL, setup `src/test/setup.ts` с
  jest-dom). **Гоняются в CI** (job `ui-build`) — падение vitest валит pipeline, как и lint/build.
- `make test-int-rejected` (§94) — integration-группа журнала отказов на входе (PostgreSQL):
  слияние пачек нескольких реплик Receiver в одну группу, вытеснение клиентов и сэмплов
  сверх лимитов, снятие отметки «разобрано» новым отказом, чистка по сроку и полное
  выключение журнала, скоупы команд. Входит в общий `make test-integration`. Тесты гоняются
  на `postgres:12-alpine` — той же мажорной версии, что на бою.
- `make test-int-logs-scale` (§77.5) — **ручной** замер прокрутки логов на большом объёме: сидит
  таблицу одним `INSERT … SELECT FROM numbers` и прогоняет 20 страниц по 50 строк по keyset-курсору,
  печатая avg/max и эталон «без автоокна». Объём — `LOG_SCALE_ROWS` (по умолчанию 1 млн; боевой
  масштаб — `make test-int-logs-scale LOG_SCALE_ROWS=50000000`, ~30 с на прогон). В
  `make test-integration` и в CI **не входит**: результат здесь не pass/fail, а цифры. Сам тест без
  `LOG_SCALE_RUN=1` скипается, поэтому в группе `test-int-ch` он безопасен.
  Ориентиры (стенд разработчика): 50 млн строк — 7 мс средняя страница против 1153 мс без автоокна;
  1 млн — 7 мс против 86 мс. Смотреть надо на **форму зависимости**: с автоокном время страницы от
  объёма таблицы не зависит, без него растёт линейно.
- Правило §51.9 (CLAUDE.md §1): в новом коде закладывайте `logger.Debug` в неочевидных/опасных/
  тихих местах — уровень включается в runtime через консоль «Логи» без рестарта.

---

## 6. Запуск на уже поднятых внешних сервисах (Docker Desktop)

Если зависимости уже крутятся в Docker Desktop по **своей** конфигурации (отличной от
`docker-compose.deps.yml`) — с другими логинами, паролями, портами или именами БД, — то
`docker-compose.deps.yml` поднимать не нужно: достаточно привести конфиг Nexus в соответствие
с этими сервисами. Ниже — на примере типового стороннего `docker-compose.yml`:

| Сервис      | Адрес с хоста        | Логин      | Пароль   | Что важно учесть                                              |
|-------------|----------------------|------------|----------|---------------------------------------------------------------|
| PostgreSQL  | `localhost:5432`     | `postgres` | `vOkjDn` | БД по умолчанию `postgres`; Nexus жёстко требует БД `nexus` — её надо создать |
| Redis       | `localhost:6379`     | `sa`       | `I2MV5s` | default-юзер выключен через ACL → нужен `username`             |
| ClickHouse  | `localhost:`**`19000`** | `default` | —        | native-порт проброшен как **19000** (не 9000); HTTP — 18123   |
| Kafka       | `localhost:9092`     | —          | —        | брокер анонсирует себя как `kafka:9092` → нужен hosts-маппинг  |
| Prometheus  | `localhost:`**`9099`** | —          | —        | опционален; в Docker, скрейпит нативные сервисы через `host.docker.internal`; включает дашборды панели (§21) и Telegram-алерты (§22). Хост-порт **9099** (9091 занят Sender-admin'ом, gRPC Sender на 9190; 9090 зарезервирован под другое приложение) |

### Шаг 1. Подготовить сервисы

**а) Создать БД `nexus` в PostgreSQL** (имя БД в конфиге Nexus фиксированное, миграции его не
создают):

```powershell
docker exec -i postgres psql -U postgres -c "CREATE DATABASE nexus;"
```

**б) Создать БД `nexus_default` в ClickHouse.** Это база CH default-команды — именно сюда пишутся
логи (`nodes.clickhouse_table` хранит полное `nexus_<slug>.<table>`, поле нормализуется в Web при
создании/обновлении узла). Сидинг default-команды в миграции
0008 создаёт только строку в PostgreSQL — саму CH-базу для неё никто не заводит автоматически
(`TeamProvisioner` создаёт `nexus_<slug>` только при создании команды через UI `/api/teams`).
Плюс Sender при старте подключается к БД из `clickhouse.database`, поэтому она должна существовать:

```powershell
docker exec -i clickhouse clickhouse-client --query "CREATE DATABASE IF NOT EXISTS nexus_default"
```

Таблицы логов внутри `nexus_default` Nexus создаёт сам при создании узла с CH-шаблоном (§19) —
вручную их заводить не нужно, у CH-юзера `default` прав на `CREATE` достаточно. Для дополнительных
команд (`/api/teams`) их базы `nexus_<slug>` создаются провижионером автоматически.

**в) Замапить `kafka` на localhost.** Брокер анонсирует адрес `kafka:9092`
(`KAFKA_ADVERTISED_LISTENERS`), поэтому процесс на хосте после bootstrap'а получит `kafka:9092`
и не сможет его разрешить. Добавьте в `C:\Windows\System32\drivers\etc\hosts` (от администратора)
строку:

```text
127.0.0.1 kafka
```

> Альтернатива без правки hosts — сменить в вашем `docker-compose.yml`
> `KAFKA_ADVERTISED_LISTENERS` на `PLAINTEXT://localhost:9092` и пересоздать контейнер kafka.

### Шаг 2. Привести конфиг под свои сервисы

`--debug` использует [config/config_debug.yml](./config/config_debug.yml) с localhost-дефолтами
(PG `nexus`/`nexus`, Redis без пароля, CH порт 9000). Эти значения **зашиты в файл и НЕ
переопределяются через `.env`** — их нужно поправить. Выберите способ:

**Способ A (рекомендуемый для VS Code) — отредактировать `config/config_debug.yml`.** Тогда все
существующие launch-конфиги (`--debug`), `make run-*` и таски работают без изменений. Замените
три секции на свои значения (Kafka трогать не нужно — `brokers: localhost:9092` уже подходит
после hosts-маппинга):

```yaml
postgres:
  host: localhost
  port: 5432
  database: nexus
  user: postgres
  password: vOkjDn
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime_min: 30

redis:
  host: localhost
  port: 6379
  db: 0
  username: sa
  password: I2MV5s
  pool_size: 10
  min_idle_conns: 2
  dial_timeout_ms: 2000
  read_timeout_ms: 1000
  write_timeout_ms: 1000
  node_ttl_sec: 60
  session_ttl_sec: 3600
  ratelimit_window_sec: 60

clickhouse:
  host: localhost
  port: 19000
  database: nexus_default   # БД подключения по умолчанию (= CH-база default-команды)
  user: default
  password: ""
  batch_size: 100
  flush_interval_sec: 2
  buffer_max_size: 10000
  workers: 1
```

`config_debug.yml` отслеживается git'ом — чтобы случайно не закоммитить локальные креды и чтобы
правки не висели в `git status`:

```powershell
git update-index --skip-worktree config/config_debug.yml
# вернуть отслеживание при необходимости:
# git update-index --no-skip-worktree config/config_debug.yml
```

> **Локально стоит выключить дренаж остановки (§93.5).** По умолчанию сервис по SIGTERM сначала
> объявляет себя неготовым, ждёт `shutdown.drain_sec` (5 с) и только потом останавливается — на
> сервере это нужно балансировщику, а при отладке добавляет пять секунд к каждому перезапуску.
> В своём `config_debug.yml`:
>
> ```yaml
> shutdown:
>   drain_sec: -1     # отрицательное значение выключает паузу; 0 означает «дефолт»
>   timeout_sec: 15
> ```

**Способ B (изолированный) — отдельный файл.** Скопируйте `config/config_debug.yml` в
`config/config.local.yml`, внесите те же три правки и запускайте с `--config config/config.local.yml`
вместо `--debug`. Для VS Code продублируйте нужные конфиги в [.vscode/launch.json](./.vscode/launch.json),
заменив `"args": ["--debug"]` на `"args": ["--config", "config/config.local.yml"]`. Добавьте
`config/config.local.yml` в `.gitignore`.

### Шаг 2б. Prometheus (опционально — дашборды панели + Telegram-алерты)

Дашборды Overview (§21) и Telegram-уведомления (§22, теперь считают ошибки из Prometheus) работают
только при настроенном Prometheus. Если он есть в вашем `docker-compose.yml` сторонних сервисов
(сервис `prometheus`, хост-порт **9099**, скрейпит нативные сервисы через `host.docker.internal` —
см. пример в `prometheus.yml` рядом с compose), достаточно указать его URL Web-сервису.

`config_debug.yml` читает `prometheus.url: ${PROMETHEUS_URL:}`, поэтому правка конфига не нужна —
задайте переменную в `.env`:

```dotenv
PROMETHEUS_URL=http://localhost:9099
```

Без неё `url` остаётся пустым: панель деградирует (KPI/throughput скрыты, `prometheus_available=false`),
а планировщик Telegram-уведомлений не запускается (warning в лог). Остальная разработка (узлы, логи,
аудит) от этого не страдает. Проверка скрейпа: открыть `http://localhost:9099/targets` — таргеты
`nexus-receiver`/`nexus-sender`/`nexus-web` должны быть `UP` (нативные сервисы при этом запущены).

### Шаг 3. Первый запуск и миграция

`.env` с валидным `ENCRYPTION_KEY` всё равно обязателен (см. §2). Дальше — миграции и bootstrap
пароля (способ A — с `--debug`; способ B — подставьте `--config config/config.local.yml`):

```powershell
# применить миграции (создаст таблицы в БД nexus)
go run ./cmd/web --debug --migrate-up

# проверить версию схемы
go run ./cmd/web --debug --migrate-status

# задать пароль admin
go run ./cmd/web --debug --set-admin-password admin
```

То же доступно через Run and Debug (**Web: migrate-up**, **Web: set-admin-password**) и таски
(**migrate up**, **set admin password (admin)**).

### Шаг 4. Запуск сервисов

- **Способ A:** как обычно — Run and Debug → `Nexus: all (web + receiver + sender)` или
  `make run-receiver` / `run-sender` / `run-web`.
- **Способ B:** `go run ./cmd/web --config config/config.local.yml` (по сервису на терминал) или
  запуск добавленных в `launch.json` конфигов.

Проверка работоспособности — раздел §5.

---

## 7. Типовые грабли

- **`ENCRYPTION_KEY invalid`** — декодированный base64 не равен 32 байтам, перегенерируйте
  (см. §2).
- **`clickhouse connect failed` в Web** — некритично: replay/live-tail отключатся, остальное
  работает (см. [bootstrap.go](./internal/platform/bootstrap/bootstrap.go)).
- **Kafka не стартует за 10 сек** — норма для KRaft на Windows; дайте 30 сек. Логи:
  `docker compose -f deploy/docker-compose.deps.yml logs kafka`.
- **Порт `5432`/`6379`/`9092` занят** — выключите локальный сервис postgres/redis/kafka или
  поменяйте проброс портов в `docker-compose.deps.yml`.
- **`go test -race` падает на cgo** (`runtime/cgo: cgo.exe: exit status 2`) — на Windows нужен gcc
  из MSYS2 в PATH ПЕРЕД каталогом `mingw64\bin` из состава Git for Windows. Иначе `cc1.exe`
  из MSYS2 подхватывает DLL от Git for Windows (там нет `libmpfr`/`libmpc`/`libisl`, а `zlib1`/
  `libwinpthread` другой версии) и падает с `STATUS_ENTRYPOINT_NOT_FOUND` — молча, без вывода.
  `make`-цели чинят порядок сами (см. шапку [Makefile](./Makefile)); при запуске `go test` руками
  порядок PATH нужно поправить вручную либо в системных переменных.
- **Один пакет падает с `Access is denied … [build failed]`, остальные проходят** — это НЕ порча
  кода и не блокировка процессом. Kaspersky Endpoint Security помечает конкретный тестовый бинарь
  эвристикой `VHO:Trojan.Win64.Agent.gen` (вердикт облачный, точность «высокая») и блокирует файл
  сразу после линковки; go спотыкается на следующем шаге `go tool buildid -w`. Проверяется в
  журнале: `Get-WinEvent -LogName 'Kaspersky Endpoint Security' | Where-Object Id -eq 302` — там
  видно имя объекта и SHA256. Ложное срабатывание ловит именно PIE-бинарь (Go линкует тесты как
  PIE, когда включён cgo), поэтому все `make`-цели гоняют тесты с `-buildmode=exe`
  (`GO_TEST_FLAGS` в шапке Makefile) — обычный exe эвристику не задевает. Настоящее лечение —
  исключение в политике KES (каталог `%LOCALAPPDATA%\Temp\go-build*` либо доверенное приложение
  `go.exe`); флаг нужен потому, что прав на политику у разработчика обычно нет.
- **delve: «could not load source» для `embed.FS`** — не критично, на отладку не влияет
  (см. [.vscode/settings.json](./.vscode/settings.json)).
- **Web не стартует: «database already exists on first run of this instance» (§70).** Сработал гейт
  первого запуска: `instance.id` пуст, PostgreSQL свежая, а база `nexus_default` в ClickHouse уже
  есть — то есть в этот ClickHouse уже пишет другая нода. Варианты: задать свой `instance.id`
  (тогда базы будут `nexus_<id>_<slug>`) либо, если база действительно ваша (пересоздали
  PostgreSQL), запустить один раз с `--ch-adopt`.
- **Web не стартует: «instance.id mismatch» (§70).** Сохранённый в PostgreSQL идентификатор не
  совпадает с конфигом. Смена идентификатора не переименовывает уже созданные базы ClickHouse,
  поэтому старт прекращается: верните прежнее значение либо переименуйте базы вручную и запустите
  с `--instance-id-force`.
- **Локальная отладка второй ноды** (§70): в `config_debug.yml` задайте `instance.id: kz` (или
  `NEXUS_INSTANCE_ID=kz`) — базы станут `nexus_kz_*`, а в шапке интерфейса появится чип `KZ`.
  PostgreSQL/Redis/Kafka при этом должны быть отдельными от первой ноды.
